// Package decisions is the application layer between the decision engines and
// the Executor: it runs a Decision through the folder's Policy and then either
// executes it or stores it in a persistent queue for the user to accept or
// reject (docs/adr/0006-decision-engine-chain-with-policy-and-executor.md).
//
// The package has no HTTP and no SQL. Storage sits behind the Store and
// PolicyStore interfaces (a SQLite implementation lives in sqlitestore) and
// file changes sit behind FileExecutor (satisfied by *executor.Executor), so
// everything here is testable with fakes. File contents are never handled.
package decisions

import (
	"context"
	"errors"
	"time"

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

// Status is the life-cycle state of a queued decision.
type Status string

const (
	// StatusPending is waiting for the user.
	StatusPending Status = "pending"
	// StatusAccepted means the user accepted it and the Executor carried it out.
	StatusAccepted Status = "accepted"
	// StatusRejected means the user declined it.
	StatusRejected Status = "rejected"
	// StatusApplied means the policy applied it automatically.
	StatusApplied Status = "applied"
	// StatusFailed means the Executor could not carry it out (for example the
	// file had vanished). It leaves the queue and is not counted as feedback.
	StatusFailed Status = "failed"
)

// Valid reports whether s is one of the defined statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusAccepted, StatusRejected, StatusApplied, StatusFailed:
		return true
	}
	return false
}

// Record is one decision in the queue. It holds paths and the engine's
// explanation, never file contents.
type Record struct {
	ID int64
	TS time.Time
	// Root is the absolute path of the watched folder.
	Root string
	// File is the path of the file relative to Root.
	File       string
	Action     decision.Action
	Target     string
	Confidence float64
	Reason     string
	Source     string
	Status     Status
	// JournalID links to the Executor's journal entry once the decision has
	// been carried out; it is nil before that.
	JournalID *int64
}

// Errors returned by Service and by Store implementations.
var (
	// ErrNotFound means no decision (or journal entry) has the given id.
	ErrNotFound = errors.New("decisions: not found")
	// ErrWrongStatus is returned by Store.Transition when the record is not in
	// the expected status.
	ErrWrongStatus = errors.New("decisions: record is not in the expected status")
	// ErrNotPending means the decision was already accepted, rejected, applied or failed.
	ErrNotPending = errors.New("decisions: decision is not pending")
	// ErrInvalid means the caller supplied an invalid value. The wrapped
	// message says which and is safe to show to the user.
	ErrInvalid = errors.New("decisions: invalid input")
	// ErrTargetRequired means an ask_user decision was accepted without a target.
	ErrTargetRequired = errors.New("decisions: this decision has no target, so one must be provided")
	// ErrNotEligible means a folder asked for auto mode before earning it.
	ErrNotEligible = errors.New("decisions: folder is not eligible for auto mode")
	// ErrExecutorUnavailable means file actions are disabled (no journal).
	ErrExecutorUnavailable = errors.New("decisions: file executor unavailable")
)

// ListFilter selects a page of records, newest first.
type ListFilter struct {
	// Status restricts the result to one status; empty means all.
	Status Status
	// Root restricts the result to one watched folder; empty means all.
	Root string
	// File restricts the result to one file (relative to its root); empty
	// means all.
	File   string
	Limit  int
	Offset int
}

// Page is one page of a listing.
type Page struct {
	Items []Record
	// Total is the number of records matching the filter, ignoring Limit and Offset.
	Total  int
	Limit  int
	Offset int
}

// Update lists the optional fields Transition changes along with the status.
type Update struct {
	// JournalID, if non-nil, is stored as the record's journal link.
	JournalID *int64
	// Target, if non-nil, replaces the stored target (used when the user
	// overrides the suggestion on accept).
	Target *string
}

// Store persists the decision queue. Implementations must be safe for
// concurrent use.
type Store interface {
	// Insert stores r (ignoring r.ID) and returns it with its assigned ID.
	Insert(ctx context.Context, r Record) (Record, error)
	// Get returns the record, or ErrNotFound.
	Get(ctx context.Context, id int64) (Record, error)
	// List returns a page of records, newest first.
	List(ctx context.Context, f ListFilter) (Page, error)
	// Transition atomically moves the record from status from to status to and
	// applies u. It returns ErrNotFound for an unknown id and ErrWrongStatus if
	// the record is not currently in from. from may equal to.
	Transition(ctx context.Context, id int64, from, to Status, u Update) error
}

// PolicyStore persists per-folder policy settings.
type PolicyStore interface {
	// GetPolicy returns the settings stored for root; ok is false when none are.
	GetPolicy(ctx context.Context, root string) (s policy.Settings, ok bool, err error)
	// PutPolicy stores (replacing) the settings for root.
	PutPolicy(ctx context.Context, root string, s policy.Settings) error
}

// FileExecutor carries out and undoes file actions. *executor.Executor
// satisfies it.
type FileExecutor interface {
	Execute(ctx context.Context, root string, d decision.Decision, file string) (executor.JournalEntry, error)
	Undo(ctx context.Context, id int64) error
}
