package config

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/executor/sqlitejournal"
)

// journalFile is the SQLite journal's file name inside the Arca app dir.
const journalFile = "journal.db"

// wireExecutor opens the SQLite journal in dir and returns an Executor backed
// by it, plus a function that closes the journal (safe to call when the
// Executor is nil). If the journal cannot be opened, for example in a build
// without CGo, it returns a nil Executor and the reason, so callers can keep
// serving but refuse to move files without a record (ADR-0006).
func wireExecutor(dir string) (*executor.Executor, func(), error) {
	j, err := sqlitejournal.Open(filepath.Join(dir, journalFile))
	if err != nil {
		return nil, func() {}, fmt.Errorf("config: open journal: %w", err)
	}
	return executor.New(j), func() {
		if err := j.Close(); err != nil {
			log.Printf("executor: close journal: %v", err)
		}
	}, nil
}
