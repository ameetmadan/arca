// Package watcher uses FSEvents (via fsnotify) to watch folders and notify the indexer.
package watcher

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	fsw      *fsnotify.Watcher
	indexURL string
}

func New(indexURL string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{fsw: fsw, indexURL: indexURL}, nil
}

func (w *Watcher) AddFolder(path string) error {
	return w.fsw.Add(path)
}

func (w *Watcher) RemoveFolder(path string) error {
	return w.fsw.Remove(path)
}

// Run processes file system events until ctx is cancelled. It returns only
// after all in-flight index notifications have finished or been cancelled.
func (w *Watcher) Run(ctx context.Context) {
	var notifications sync.WaitGroup
	defer notifications.Wait()

	debounce := make(map[string]time.Time)
	const debounceWindow = 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			w.fsw.Close()
			return

		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !isRelevant(event) {
				continue
			}
			last := debounce[event.Name]
			if time.Since(last) < debounceWindow {
				continue
			}
			debounce[event.Name] = time.Now()
			notifications.Add(1)
			go func() {
				defer notifications.Done()
				w.notifyIndex(ctx, event.Name)
			}()

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

func (w *Watcher) notifyIndex(ctx context.Context, path string) {
	body := strings.NewReader(`{"path":"` + path + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.indexURL, body)
	if err != nil {
		log.Printf("watcher: build index request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("watcher: notify index failed: %v", err)
		}
		return
	}
	resp.Body.Close()
}

func isRelevant(e fsnotify.Event) bool {
	return e.Has(fsnotify.Create) || e.Has(fsnotify.Write) || e.Has(fsnotify.Remove)
}
