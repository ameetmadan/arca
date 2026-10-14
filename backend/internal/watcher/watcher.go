// Package watcher uses FSEvents (via fsnotify) to watch folders and publish
// file events to a sink, normally pipeline.Pipeline.Submit. It does no
// debouncing or indexing itself; the pipeline owns both.
package watcher

import (
	"context"
	"errors"
	"log"

	"github.com/fsnotify/fsnotify"

	"github.com/ameetmadan/arca/internal/pipeline"
)

// Sink receives file events. It is called from the watcher's single event
// loop, so it must not block; pipeline.Pipeline.Submit never does.
type Sink func(pipeline.Event)

type Watcher struct {
	fsw  *fsnotify.Watcher
	sink Sink
}

// New returns a Watcher that publishes events to sink.
func New(sink Sink) (*Watcher, error) {
	if sink == nil {
		return nil, errors.New("watcher: sink is required")
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{fsw: fsw, sink: sink}, nil
}

func (w *Watcher) AddFolder(path string) error {
	return w.fsw.Add(path)
}

func (w *Watcher) RemoveFolder(path string) error {
	return w.fsw.Remove(path)
}

// Run publishes file system events to the sink until ctx is cancelled, then
// closes the underlying watcher and returns.
func (w *Watcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			w.fsw.Close()
			return

		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if op, relevant := opFor(event); relevant {
				w.sink(pipeline.Event{Op: op, Path: event.Name})
			}

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

// opFor maps an fsnotify event to a pipeline op. When several bits are set the
// most final one wins: Remove, then Create, then Write. Rename is reported for
// the old name only (the new name arrives as Create), so it counts as a
// removal. Chmod is not relevant.
func opFor(e fsnotify.Event) (pipeline.Op, bool) {
	switch {
	case e.Has(fsnotify.Remove), e.Has(fsnotify.Rename):
		return pipeline.Removed, true
	case e.Has(fsnotify.Create):
		return pipeline.Created, true
	case e.Has(fsnotify.Write):
		return pipeline.Modified, true
	}
	return 0, false
}
