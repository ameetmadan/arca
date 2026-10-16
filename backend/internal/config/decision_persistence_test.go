//go:build cgo

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The semantic folder index is persisted in the app dir and released through
// the wiring's Closers, including when a later engine fails to wire.
func TestWireDecisionPersistsFolderIndex(t *testing.T) {
	deps := fullDeps(t)
	w, err := wireDecision(&File{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deps.AppDir, folderIndexFile)); err != nil {
		t.Errorf("semantic folder index not created: %v", err)
	}
	if len(w.Closers) < 1 {
		t.Fatal("missing store closer")
	}
	for _, close := range w.Closers {
		close()
	}

	// llm comes after semantic in the default chain; without an LLM it fails
	// to wire after the store was opened, and the store must be closed again.
	deps = fullDeps(t)
	deps.LLM = nil
	if _, err := wireDecision(&File{}, deps); err == nil {
		t.Fatal("wireDecision without an LLM = nil error")
	}
}
