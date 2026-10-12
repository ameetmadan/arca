package main

import (
	"log"
	"path/filepath"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/decisions/sqlitestore"
)

// newDecisionService opens decisions.db in dir and returns the decision
// service backed by it, plus a function that closes the database. If the
// database cannot be opened (for example a build without CGo) it logs the
// reason and returns a nil service; the decision endpoints then answer 503.
// A nil exec leaves the queue usable but disables accept and undo; it must be
// a nil interface, not an interface holding a nil pointer.
func newDecisionService(dir string, exec decisions.FileExecutor) (*decisions.Service, func()) {
	st, err := sqlitestore.Open(filepath.Join(dir, "decisions.db"))
	if err != nil {
		log.Printf("decisions: queue unavailable: %v", err)
		return nil, func() {}
	}
	deps := decisions.Deps{Queue: st, Policies: st, Stats: st}
	deps.Exec = exec
	return decisions.NewService(deps), func() {
		if err := st.Close(); err != nil {
			log.Printf("decisions: close queue: %v", err)
		}
	}
}
