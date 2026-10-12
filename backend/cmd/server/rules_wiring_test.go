package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/pkg/decision/rules"
)

func TestRulesReloader_NilWhenNoEngine(t *testing.T) {
	if got := rulesReloader(&config.App{}); got != nil {
		t.Fatalf("rulesReloader with no engine = %#v, want a true nil interface", got)
	}
}

func TestRulesStore_UsesTheEnginesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-rules.yaml")
	store := rulesStore(&config.App{RulesPath: path})

	specs := []rules.Spec{{ID: "pdfs", When: `Ext == ".pdf"`, Action: "move", Target: "Docs"}}
	if err := store.Save(specs); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the store to write %s: %v", path, err)
	}
}
