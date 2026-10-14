//go:build cgo

package sqlitestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision/semantic"
	"github.com/ameetmadan/arca/pkg/decision/semantic/storetest"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) semantic.FolderStore {
		return open(t, filepath.Join(t.TempDir(), "folders.db"))
	})
}

func TestFoldersSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "folders.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := semantic.FolderIndex{
		Root: "/r", Rel: "a", FileCount: 2, Fingerprint: "fp1",
		Centroid: []float32{1, 0},
		Examples: []semantic.Example{{Name: "x.txt", Vector: []float32{1, 0}}},
	}
	if err := s.Put(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := open(t, path)
	got, ok, err := s2.Get(ctx, "/r", "a")
	if err != nil || !ok {
		t.Fatalf("Get after reopen = ok %v, err %v", ok, err)
	}
	if got.Fingerprint != "fp1" || len(got.Examples) != 1 || got.Examples[0].Name != "x.txt" {
		t.Errorf("after reopen = %+v", got)
	}
}

func TestDeleteRemovesExamples(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "folders.db"))
	f := semantic.FolderIndex{
		Root: "/r", Rel: "a", Centroid: []float32{1},
		Examples: []semantic.Example{{Name: "x", Vector: []float32{1}}},
	}
	if err := s.Put(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "/r", "a"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM examples`).Scan(&n); err != nil || n != 0 {
		t.Errorf("examples left after Delete: %d, err %v", n, err)
	}
}

func TestCorruptVectorIsAnError(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "folders.db"))
	if _, err := s.db.Exec(
		`INSERT INTO folders (root, rel, file_count, fingerprint, centroid) VALUES ('/r', 'a', 1, 'f', x'010203')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, "/r", "a"); err == nil {
		t.Error("Get of a folder with a truncated vector = nil error")
	}
	if _, err := s.List(ctx); err == nil {
		t.Error("List with a truncated vector = nil error")
	}
}

func TestOpenFailsOnBadPath(t *testing.T) {
	if _, err := Open(filepath.Join(os.DevNull, "nope", "folders.db")); err == nil {
		t.Error("Open on an impossible path = nil error")
	}
}
