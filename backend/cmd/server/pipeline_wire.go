package main

import (
	"log"
	"path/filepath"

	"github.com/ameetmadan/arca/internal/pipeline"
)

// eventHandler returns the handler for watcher events: index the file, then
// (when everything it needs is configured) decide about it and submit the
// decision to the queue.
//
// If the decision queue, the Executor or the decision chain is missing, for
// example because a database could not be opened, files are indexed only. That
// is logged once, here, not per event.
func (s *server) eventHandler() pipeline.Handler {
	idx := s.indexer().Handle

	var missing string
	switch {
	case s.decisions == nil:
		missing = "the decision queue is unavailable"
	case s.exec == nil:
		missing = "the file executor is unavailable"
	case s.app.Decision == nil:
		missing = "no decision engine is configured"
	}
	if missing != "" {
		log.Printf("pipeline: %s; new files are indexed but no decisions are made", missing)
		return idx
	}

	roots := watchedRoots(s.app.Cfg.Watcher.Folders)
	d := &pipeline.Decider{
		Index:      idx,
		Facts:      &pipeline.FactsBuilder{Parser: s.app.Parser, Embed: s.app.EmbedProvider, Roots: roots},
		Engine:     s.app.Decision,
		Queue:      s.decisions,
		Roots:      roots,
		Recent:     s.recent,
		MaxPending: s.app.Cfg.Decision.MaxPendingPerFolder,
	}
	return d.Handle
}

// watchedRoots returns the configured watcher folders as clean absolute paths;
// those are the roots policies and decisions are keyed by.
func watchedRoots(folders []string) []string {
	var roots []string
	for _, f := range folders {
		if !filepath.IsAbs(f) {
			log.Printf("pipeline: ignoring watcher folder %q: not an absolute path", f)
			continue
		}
		roots = append(roots, filepath.Clean(f))
	}
	return roots
}
