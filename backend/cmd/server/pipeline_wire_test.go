package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/decisions/decisionstest"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/parser"
)

type wireEngine struct{ calls atomic.Int64 }

func (e *wireEngine) Decide(context.Context, decision.Facts) (decision.Decision, error) {
	e.calls.Add(1)
	return decision.Decision{Action: decision.Move, Target: "Invoices", Confidence: 0.8, Reason: "test", Source: "fake"}, nil
}

func wireServer(t *testing.T, root string, eng decision.Engine) *server {
	t.Helper()
	s, _ := wireServerWithStore(t, root, eng)
	return s
}

func wireServerWithStore(t *testing.T, root string, eng decision.Engine) (*server, *decisionstest.MemStore) {
	t.Helper()
	st := decisionstest.NewMemStore()
	ex := executor.New(&applyJournal{})
	app := &config.App{
		Cfg:           &config.File{},
		Parser:        indexParser{chunks: []parser.Chunk{{Content: "x"}}},
		EmbedProvider: indexEmbed{},
		VectorStore:   &indexStore{},
		Decision:      eng,
	}
	app.Cfg.Watcher.Folders = []string{root}
	recent := pipeline.NewRecentMoves(0)
	return &server{
		app: app, exec: ex, recent: recent,
		decisions: decisions.NewService(decisions.Deps{Queue: st, Policies: st, Stats: st, Exec: recent.Wrap(ex)}),
	}, st
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestEventHandler_DecidesWhenFullyConfigured(t *testing.T) {
	_, root := applyDirs(t)
	path := filepath.Join(root, "inv.pdf")
	writeFile(t, path, "invoice")
	eng := &wireEngine{}
	s := wireServer(t, root, eng)

	if err := s.eventHandler()(context.Background(), pipeline.Event{Op: pipeline.Created, Path: path}); err != nil {
		t.Fatal(err)
	}
	page, err := s.decisions.List(context.Background(), decisions.ListFilter{Status: decisions.StatusPending})
	if err != nil || page.Total != 1 || page.Items[0].Root != root || page.Items[0].File != "inv.pdf" {
		t.Fatalf("pending = %+v, %v; want one decision for inv.pdf", page, err)
	}
}

func TestEventHandler_IndexOnlyWhenQueueExecutorOrEngineIsMissing(t *testing.T) {
	for name, strip := range map[string]func(*server){
		"no queue":    func(s *server) { s.decisions = nil },
		"no executor": func(s *server) { s.exec = nil },
		"no engine":   func(s *server) { s.app.Decision = nil },
	} {
		t.Run(name, func(t *testing.T) {
			_, root := applyDirs(t)
			path := filepath.Join(root, "inv.pdf")
			writeFile(t, path, "invoice")
			eng := &wireEngine{}
			s := wireServer(t, root, eng)
			strip(s)
			buf := captureLog(t)

			h := s.eventHandler()
			for i := 0; i < 3; i++ {
				if err := h(context.Background(), pipeline.Event{Op: pipeline.Created, Path: path}); err != nil {
					t.Fatal(err)
				}
			}
			if eng.calls.Load() != 0 {
				t.Fatal("the engine must not be consulted")
			}
			if n := strings.Count(buf.String(), "indexed but no decisions are made"); n != 1 {
				t.Fatalf("logged %d times, want once: %q", n, buf.String())
			}
			if st := s.app.VectorStore.(*indexStore); len(st.ids) != 3 {
				t.Fatalf("indexed %d times, want 3 (indexing must keep working)", len(st.ids))
			}
		})
	}
}

func TestEventHandler_EngineErrorIsReportedNotFatal(t *testing.T) {
	_, root := applyDirs(t)
	path := filepath.Join(root, "inv.pdf")
	writeFile(t, path, "invoice")
	s := wireServer(t, root, errEngine{})
	err := s.eventHandler()(context.Background(), pipeline.Event{Op: pipeline.Created, Path: path})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

type errEngine struct{}

func (errEngine) Decide(context.Context, decision.Facts) (decision.Decision, error) {
	return decision.Decision{}, errors.New("boom")
}

func TestWatchedRoots_KeepsCleanAbsolutePathsOnly(t *testing.T) {
	captureLog(t)
	got := watchedRoots([]string{"/a/b/", "rel/path", "/c/../d"})
	if len(got) != 2 || got[0] != "/a/b" || got[1] != "/d" {
		t.Fatalf("roots = %v", got)
	}
}
