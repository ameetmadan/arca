//go:build sqlitevec

package sqlitevec

import (
	"context"
	"path/filepath"
	"testing"
)

func open(t *testing.T, path string) *SQLiteVecStore {
	t.Helper()
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func upsert(t *testing.T, s *SQLiteVecStore, id string, v []float32) {
	t.Helper()
	if err := s.Upsert(context.Background(), id, v, map[string]interface{}{"content": id}); err != nil {
		t.Fatal(err)
	}
}

func top(t *testing.T, s *SQLiteVecStore, q []float32, k int) []string {
	t.Helper()
	res, err := s.Search(context.Background(), q, k)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range res {
		if r.Metadata["content"] != r.ID {
			t.Fatalf("vector %s paired with metadata %v", r.ID, r.Metadata)
		}
		ids = append(ids, r.ID)
	}
	return ids
}

func TestSearchRanksAndLimits(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "v.db"))
	defer s.Close()
	upsert(t, s, "x", []float32{1, 0, 0})
	upsert(t, s, "y", []float32{0, 1, 0})
	upsert(t, s, "z", []float32{0, 0, 1})

	got := top(t, s, []float32{0.9, 0.1, 0}, 2)
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("got %v", got)
	}
}

// Regression: after a restart the store did not know the dimension and
// returned nothing until something new was indexed.
func TestSearchWorksAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.db")
	s := open(t, path)
	upsert(t, s, "x", []float32{1, 0})
	s.Close()

	s = open(t, path)
	defer s.Close()
	if got := top(t, s, []float32{1, 0}, 1); len(got) != 1 || got[0] != "x" {
		t.Fatalf("got %v", got)
	}
	upsert(t, s, "y", []float32{0, 1}) // same dimension still accepted
	if err := s.Upsert(context.Background(), "bad", []float32{1, 2, 3}, nil); err == nil {
		t.Fatal("a different dimension was accepted")
	}
}

// Regression: vec_store and vec_meta assigned rowids independently, so after
// updates and deletes a vector could be returned with another chunk's text.
func TestUpdatesAndDeletesKeepVectorsPairedWithMetadata(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "v.db"))
	defer s.Close()
	ctx := context.Background()
	upsert(t, s, "/a#0", []float32{1, 0, 0})
	upsert(t, s, "/a#1", []float32{0, 1, 0})
	upsert(t, s, "/b#0", []float32{0, 0, 1})
	upsert(t, s, "/a#0", []float32{0, 0.1, 1}) // update moves /a#0 near /b#0
	if err := s.DeleteChunks(ctx, "/a#", 1); err != nil {
		t.Fatal(err)
	}
	upsert(t, s, "/c#0", []float32{0, 1, 0.1})

	got := top(t, s, []float32{0, 0, 1}, 10) // top() checks pairing
	if len(got) != 3 || got[0] != "/b#0" || got[1] != "/a#0" || got[2] != "/c#0" {
		t.Fatalf("got %v", got)
	}
	if err := s.Delete(ctx, "/b#0"); err != nil {
		t.Fatal(err)
	}
	if got := top(t, s, []float32{0, 0, 1}, 10); len(got) != 2 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestSearchEmptyStore(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "v.db"))
	defer s.Close()
	if got := top(t, s, []float32{1}, 3); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
