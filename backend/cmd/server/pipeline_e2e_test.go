package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/internal/watcher"
	"github.com/ameetmadan/arca/pkg/decision"
)

// runWatchedPipeline starts the real watcher and pipeline over root with the
// handler of s, and stops them when the test ends.
func runWatchedPipeline(t *testing.T, s *server, root string) {
	t.Helper()
	events := pipeline.New(s.eventHandler(), pipeline.Options{Debounce: 50 * time.Millisecond})
	w, err := watcher.New(events.Submit)
	if err != nil {
		t.Skipf("file watching unavailable: %v", err)
	}
	if err := w.AddFolder(root); err != nil {
		t.Skipf("cannot watch %s: %v", root, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { events.Run(ctx); done <- struct{}{} }()
	go func() { w.Run(ctx); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// End to end with a fake engine, parser, embedder and vector store but the
// real watcher, pipeline, decider, queue, policy and Executor: a new file
// becomes a queued decision in suggest mode and an applied move in auto mode,
// and the move's own file system events do not produce another decision.
func TestEndToEnd_FileEventToQueuedAndAppliedDecision(t *testing.T) {
	_, root := applyDirs(t)
	eng := &wireEngine{}
	s, st := wireServerWithStore(t, root, eng)
	ctx := context.Background()
	count := func(status decisions.Status) int {
		p, err := s.decisions.List(ctx, decisions.ListFilter{Status: status, Root: root})
		if err != nil {
			t.Fatal(err)
		}
		return p.Total
	}
	runWatchedPipeline(t, s, root)

	// suggest mode (the default): queued, file untouched.
	first := filepath.Join(root, "inv1.pdf")
	writeFile(t, first, "invoice one")
	eventually(t, "a queued decision", func() bool { return count(decisions.StatusPending) == 1 })
	if !exists(first) {
		t.Fatal("suggest mode must not move the file")
	}

	// auto mode, once the folder has earned it: the move is applied.
	for i := 0; i < policy.MinDecisions; i++ {
		if _, err := st.Insert(ctx, decisions.Record{Root: root, File: "x", Action: decision.Move, Status: decisions.StatusAccepted}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutPolicy(ctx, root, policy.Settings{Mode: policy.ModeAuto, AutoThreshold: 0.5}); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(root, "inv2.pdf")
	writeFile(t, second, "invoice two")
	eventually(t, "an applied move", func() bool { return count(decisions.StatusApplied) == 1 })
	if !exists(filepath.Join(root, "Invoices", "inv2.pdf")) || exists(second) {
		t.Fatal("auto mode should have moved inv2.pdf into Invoices")
	}

	// Let the move's own events (remove at the source, create of the new
	// Invoices folder) pass through the pipeline: no further engine calls.
	time.Sleep(700 * time.Millisecond)
	if n := eng.calls.Load(); n != 2 {
		t.Fatalf("engine calls = %d, want 2 (one per new file; moves must not loop)", n)
	}
	if count(decisions.StatusPending) != 1 || count(decisions.StatusApplied) != 1 {
		t.Fatal("the move produced extra decisions")
	}
}
