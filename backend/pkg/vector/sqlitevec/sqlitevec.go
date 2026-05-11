//go:build !sqlitevec

// Stub — compile without -tags sqlitevec for API-layer testing.
package sqlitevec

import (
	"context"
	"errors"

	"github.com/ameetmadan/arca/pkg/vector"
)

var ErrNotBuilt = errors.New("sqlitevec: binary not built with sqlite-vec CGo extension (build tag: sqlitevec)")

type SQLiteVecStore struct{ DBPath string }

func New(dbPath string) (*SQLiteVecStore, error) {
	return &SQLiteVecStore{DBPath: dbPath}, nil
}

func (s *SQLiteVecStore) Upsert(_ context.Context, _ string, _ []float32, _ map[string]interface{}) error {
	return ErrNotBuilt
}

func (s *SQLiteVecStore) Search(_ context.Context, _ []float32, _ int) ([]vector.SearchResult, error) {
	return nil, ErrNotBuilt
}

func (s *SQLiteVecStore) Delete(_ context.Context, _ string) error {
	return ErrNotBuilt
}
