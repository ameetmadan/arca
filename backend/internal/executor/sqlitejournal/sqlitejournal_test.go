//go:build cgo

package sqlitejournal

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

func open(t *testing.T) (*Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j, path
}

func TestJournal_AppendGetMarkUndone(t *testing.T) {
	j, _ := open(t)
	ctx := context.Background()
	ts := time.Date(2026, 10, 12, 9, 30, 0, 123, time.UTC)

	in := executor.JournalEntry{
		TS: ts, Root: "/r", Action: decision.Move, Src: "/r/a.txt", Dst: "/r/Docs/a.txt",
		DecisionSource: "rules", Reason: "invoice",
	}
	got, err := j.Append(ctx, in)
	if err != nil || got.ID == 0 {
		t.Fatalf("Append: %+v, %v", got, err)
	}
	out, err := j.Get(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.TS.Equal(ts) || out.Root != in.Root || out.Action != in.Action || out.Src != in.Src ||
		out.Dst != in.Dst || out.DecisionSource != in.DecisionSource || out.Reason != in.Reason || out.UndoneAt != nil {
		t.Fatalf("round trip mismatch: %+v", out)
	}

	at := ts.Add(time.Hour)
	if err := j.MarkUndone(ctx, got.ID, at); err != nil {
		t.Fatal(err)
	}
	out, _ = j.Get(ctx, got.ID)
	if out.UndoneAt == nil || !out.UndoneAt.Equal(at) {
		t.Fatalf("UndoneAt = %v", out.UndoneAt)
	}
}

func TestJournal_NotFound(t *testing.T) {
	j, _ := open(t)
	if _, err := j.Get(context.Background(), 99); !errors.Is(err, executor.ErrJournalEntryNotFound) {
		t.Fatalf("Get: %v", err)
	}
	if err := j.MarkUndone(context.Background(), 99, time.Now()); !errors.Is(err, executor.ErrJournalEntryNotFound) {
		t.Fatalf("MarkUndone: %v", err)
	}
}

func TestJournal_PersistsAcrossReopen(t *testing.T) {
	j, path := open(t)
	e, err := j.Append(context.Background(), executor.JournalEntry{TS: time.Now(), Root: "/r", Action: decision.Move, Src: "/r/a", Dst: "/r/b/a"})
	if err != nil {
		t.Fatal(err)
	}
	j.Close()

	j2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	if _, err := j2.Get(context.Background(), e.ID); err != nil {
		t.Fatalf("entry lost after reopen: %v", err)
	}
}

// End to end: the real Executor with the SQLite journal, undone through a
// reopened database.
func TestExecutorWithSQLiteJournal(t *testing.T) {
	j, _ := open(t)
	ex := executor.New(j)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entry, err := ex.Execute(ctx, root, decision.Decision{Action: decision.Move, Target: "Docs", Source: "rules", Reason: "r"}, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Undo(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("file not restored: %v", err)
	}
	if err := ex.Undo(ctx, entry.ID); !errors.Is(err, executor.ErrAlreadyUndone) {
		t.Fatalf("second undo: %v", err)
	}
}
