// Package watcher uses FSEvents (via fsnotify) to watch folders and notify the indexer.
package watcher

import (
	"context"
	"log"
	"net/http"
	"strings"
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

// Run processes file system events until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
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
			go w.notifyIndex(event.Name)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

func (w *Watcher) notifyIndex(path string) {
	body := strings.NewReader(`{"path":"` + path + `"}`)
	resp, err := http.Post(w.indexURL, "application/json", body) //nolint:noctx
	if err != nil {
		log.Printf("watcher: notify index failed: %v", err)
		return
	}
	resp.Body.Close()
}

func isRelevant(e fsnotify.Event) bool {
	return e.Has(fsnotify.Create) || e.Has(fsnotify.Write) || e.Has(fsnotify.Remove)
}
