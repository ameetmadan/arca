package executor

import (
	"context"
	"errors"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
)

// ErrJournalEntryNotFound is returned by Journal.Get for an unknown id.
var ErrJournalEntryNotFound = errors.New("executor: journal entry not found")

// JournalEntry records one action the Executor performed. Paths are absolute
// and symlink-resolved. It never holds file contents.
type JournalEntry struct {
	ID             int64
	TS             time.Time
	Root           string
	Action         decision.Action
	Src            string
	Dst            string
	DecisionSource string
	Reason         string
	// UndoneAt is nil until the action has been undone.
	UndoneAt *time.Time
}

// Journal persists JournalEntry values. The SQLite implementation lives in
// the sqlitejournal subpackage so that this package, and its tests, need no
// CGo. Implementations must be safe for concurrent use.
type Journal interface {
	// Append stores e (ignoring e.ID) and returns it with the assigned ID.
	Append(ctx context.Context, e JournalEntry) (JournalEntry, error)
	// Get returns the entry with the given id, or ErrJournalEntryNotFound.
	Get(ctx context.Context, id int64) (JournalEntry, error)
	// MarkUndone sets UndoneAt on the entry, or returns ErrJournalEntryNotFound.
	MarkUndone(ctx context.Context, id int64, at time.Time) error
}
