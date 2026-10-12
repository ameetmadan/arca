package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/pkg/decision"
)

func TestRecentMoves_ContainsWithinTTLOnly(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	r := NewRecentMoves(time.Minute)
	r.now = func() time.Time { return now }

	r.Record("/root/Docs/a.pdf")
	if !r.Contains("/root/Docs/a.pdf") {
		t.Fatal("recorded path should be contained")
	}
	if !r.Contains("/root/Docs/../Docs/a.pdf") {
		t.Fatal("paths must be compared cleaned")
	}
	if r.Contains("/root/Docs/b.pdf") {
		t.Fatal("unrelated path must not be contained")
	}

	now = now.Add(59 * time.Second)
	if !r.Contains("/root/Docs/a.pdf") {
		t.Fatal("still inside the window")
	}
	now = now.Add(2 * time.Second)
	if r.Contains("/root/Docs/a.pdf") {
		t.Fatal("expired entries must be forgotten")
	}
}

func TestRecentMoves_ComparesThroughSymlinkedDirs(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := NewRecentMoves(time.Minute)
	// The Executor reports symlink-resolved paths; the watcher reports the
	// path as configured.
	r.Record(filepath.Join(real, "a.pdf"))
	if !r.Contains(filepath.Join(link, "a.pdf")) {
		t.Fatal("a path through a symlinked folder must match the resolved destination")
	}
}

type fakeExecutor struct {
	entry   executor.JournalEntry
	err     error
	undoErr error
}

func (f *fakeExecutor) Execute(context.Context, string, decision.Decision, string) (executor.JournalEntry, error) {
	return f.entry, f.err
}
func (f *fakeExecutor) Undo(context.Context, int64) error { return f.undoErr }

func TestRecentMoves_WrapRecordsMovesAndUndos(t *testing.T) {
	fe := &fakeExecutor{entry: executor.JournalEntry{ID: 7, Src: "/root/a.pdf", Dst: "/root/Docs/a.pdf"}}

	r := NewRecentMoves(time.Minute)
	w := r.Wrap(fe)
	if _, err := w.Execute(context.Background(), "/root", decision.Decision{Action: decision.Move}, "a.pdf"); err != nil {
		t.Fatal(err)
	}
	if !r.Contains("/root/Docs/a.pdf") || !r.Contains("/root/a.pdf") {
		t.Fatal("both ends of a successful move must be remembered")
	}

	// An undo puts the file back at Src; that Created event must be ignored
	// too, even after the original window has passed.
	r2 := NewRecentMoves(time.Minute)
	clock := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	r2.now = func() time.Time { return clock }
	w2 := r2.Wrap(fe)
	if _, err := w2.Execute(context.Background(), "/root", decision.Decision{Action: decision.Move}, "a.pdf"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if r2.Contains("/root/a.pdf") {
		t.Fatal("window should have expired")
	}
	if err := w2.Undo(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if !r2.Contains("/root/a.pdf") {
		t.Fatal("an undone move must protect the restored file")
	}
}

func TestRecentMoves_WrapIgnoresFailures(t *testing.T) {
	r := NewRecentMoves(time.Minute)
	fe := &fakeExecutor{err: errors.New("boom"), entry: executor.JournalEntry{ID: 1, Src: "/r/a", Dst: "/r/D/a"}}
	w := r.Wrap(fe)
	if _, err := w.Execute(context.Background(), "/r", decision.Decision{}, "a"); err == nil {
		t.Fatal("error must be passed through")
	}
	if r.Contains("/r/D/a") {
		t.Fatal("a failed move must not be remembered")
	}
	fe.undoErr = errors.New("nope")
	if err := w.Undo(context.Background(), 1); err == nil {
		t.Fatal("undo error must be passed through")
	}
}

func TestRecentMoves_NilIsSafe(t *testing.T) {
	var r *RecentMoves
	r.Record("/a")
	if r.Contains("/a") {
		t.Fatal("nil RecentMoves contains nothing")
	}
}
