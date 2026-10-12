package main

import (
	"log"
	"path/filepath"

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/executor/sqlitejournal"
)

// newExecutor opens the SQLite journal in dir and returns an Executor backed
// by it, plus a function that closes the journal. If the journal cannot be
// opened (for example a build without CGo) it logs the reason and returns a
// nil Executor; /api/v1/sort/apply then answers 503 rather than moving files
// without a record.
func newExecutor(dir string) (*executor.Executor, func()) {
	j, err := sqlitejournal.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		log.Printf("executor: journal unavailable, file moves are disabled: %v", err)
		return nil, func() {}
	}
	return executor.New(j), func() {
		if err := j.Close(); err != nil {
			log.Printf("executor: close journal: %v", err)
		}
	}
}
