package semantic

import (
	"context"
	"sort"
	"sync"
)

// Example is one embedded file kept as evidence for a folder.
type Example struct {
	// Name is the file name inside the folder (no directory, no contents).
	Name string
	// Vector is the unit-length embedding of the file.
	Vector []float32
}

// FolderIndex is what the engine knows about one folder.
//
// Vectors inside a FolderIndex are immutable once stored: producers must not
// modify them after Put and consumers must not modify what List or Get return.
type FolderIndex struct {
	// Root is the absolute path of the watched root folder.
	Root string
	// Rel is the folder's path relative to Root, slash-separated, e.g.
	// "Taxes/2025". It is never empty: the root is not a target.
	Rel string
	// FileCount is the number of files directly inside the folder when it was
	// indexed (not just the sampled ones). It is used in Decision.Reason.
	FileCount int
	// Fingerprint identifies the folder contents and tunables the index was
	// built from, so unchanged folders can be skipped on re-index.
	Fingerprint string
	// Centroid is the unit-length mean of the example vectors.
	Centroid []float32
	// Examples are the embedded sample files.
	Examples []Example
}

// FolderStore persists folder centroids and example vectors for the semantic
// engine. It is deliberately separate from vector.VectorStore: that interface
// is a flat id -> vector nearest-neighbour index, whereas this engine needs
// grouped, replaceable per-folder records (a centroid plus its examples,
// swapped atomically when a folder changes) and an exact scan over a few
// hundred folders rather than an approximate search over millions of files.
//
// Implementations must be safe for concurrent use. Put replaces any previous
// record for the same Root and Rel atomically, so readers never see a folder
// half-updated while the background Indexer runs.
type FolderStore interface {
	// Get returns the record for one folder, and false if there is none.
	Get(ctx context.Context, root, rel string) (FolderIndex, bool, error)
	// Put inserts or replaces the record for f.Root and f.Rel.
	Put(ctx context.Context, f FolderIndex) error
	// Delete removes the record for one folder; a missing record is not an error.
	Delete(ctx context.Context, root, rel string) error
	// List returns every record, ordered by Root then Rel.
	List(ctx context.Context) ([]FolderIndex, error)
}

type folderKey struct{ root, rel string }

// MemoryStore is an in-memory FolderStore. It is the implementation used in
// tests and is also a valid choice when persistence is not needed. The index
// is rebuilt in the background after a restart.
type MemoryStore struct {
	mu      sync.RWMutex
	folders map[folderKey]FolderIndex
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{folders: make(map[folderKey]FolderIndex)}
}

// Get implements FolderStore.
func (s *MemoryStore) Get(ctx context.Context, root, rel string) (FolderIndex, bool, error) {
	if err := ctx.Err(); err != nil {
		return FolderIndex{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.folders[folderKey{root, rel}]
	return f, ok, nil
}

// Put implements FolderStore.
func (s *MemoryStore) Put(ctx context.Context, f FolderIndex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folders[folderKey{f.Root, f.Rel}] = f
	return nil
}

// Delete implements FolderStore.
func (s *MemoryStore) Delete(ctx context.Context, root, rel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.folders, folderKey{root, rel})
	return nil
}

// List implements FolderStore.
func (s *MemoryStore) List(ctx context.Context) ([]FolderIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	out := make([]FolderIndex, 0, len(s.folders))
	for _, f := range s.folders {
		out = append(out, f)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		return out[i].Rel < out[j].Rel
	})
	return out, nil
}
