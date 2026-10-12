// Package sqlitejournal stores the Executor's journal in SQLite using the
// mattn/go-sqlite3 driver already used elsewhere in the project.
//
// The driver needs CGo. Without CGo the package still compiles, but Open
// returns an error, so callers should treat a failed Open as "journal
// unavailable" rather than crash. Tests for this package live behind the cgo
// build constraint for that reason.
package sqlitejournal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/pkg/decision"
)

const schema = `
CREATE TABLE IF NOT EXISTS journal (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	ts              TEXT NOT NULL,
	root            TEXT NOT NULL,
	action          TEXT NOT NULL,
	src             TEXT NOT NULL,
	dst             TEXT NOT NULL,
	decision_source TEXT NOT NULL DEFAULT '',
	reason          TEXT NOT NULL DEFAULT '',
	undone_at       TEXT
);
CREATE INDEX IF NOT EXISTS journal_root_ts ON journal (root, ts);
`

// timeLayout keeps timestamps sortable and lossless as text.
const timeLayout = time.RFC3339Nano

// Journal is a SQLite-backed executor.Journal.
type Journal struct {
	db *sql.DB
}

var _ executor.Journal = (*Journal)(nil)

// Open opens (creating if needed) the journal database at path.
func Open(path string) (*Journal, error) {
	q := url.Values{}
	q.Set("_busy_timeout", "5000")
	q.Set("_journal_mode", "WAL")
	dsn := "file:" + url.PathEscape(path) + "?" + q.Encode()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	// A single connection serialises writes and keeps SQLite locking simple.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("init journal schema: %w", err)
	}
	return &Journal{db: db}, nil
}

// Close releases the database.
func (j *Journal) Close() error { return j.db.Close() }

// Append implements executor.Journal.
func (j *Journal) Append(ctx context.Context, e executor.JournalEntry) (executor.JournalEntry, error) {
	res, err := j.db.ExecContext(ctx,
		`INSERT INTO journal (ts, root, action, src, dst, decision_source, reason, undone_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS.UTC().Format(timeLayout), e.Root, string(e.Action), e.Src, e.Dst,
		e.DecisionSource, e.Reason, formatNullable(e.UndoneAt))
	if err != nil {
		return executor.JournalEntry{}, fmt.Errorf("insert journal entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return executor.JournalEntry{}, err
	}
	e.ID = id
	return e, nil
}

// Get implements executor.Journal.
func (j *Journal) Get(ctx context.Context, id int64) (executor.JournalEntry, error) {
	var (
		e        executor.JournalEntry
		ts       string
		action   string
		undoneAt sql.NullString
	)
	err := j.db.QueryRowContext(ctx,
		`SELECT id, ts, root, action, src, dst, decision_source, reason, undone_at
		 FROM journal WHERE id = ?`, id).
		Scan(&e.ID, &ts, &e.Root, &action, &e.Src, &e.Dst, &e.DecisionSource, &e.Reason, &undoneAt)
	if errors.Is(err, sql.ErrNoRows) {
		return executor.JournalEntry{}, executor.ErrJournalEntryNotFound
	}
	if err != nil {
		return executor.JournalEntry{}, fmt.Errorf("read journal entry: %w", err)
	}
	e.Action = decision.Action(action)
	if e.TS, err = time.Parse(timeLayout, ts); err != nil {
		return executor.JournalEntry{}, fmt.Errorf("parse journal ts: %w", err)
	}
	if undoneAt.Valid {
		t, err := time.Parse(timeLayout, undoneAt.String)
		if err != nil {
			return executor.JournalEntry{}, fmt.Errorf("parse journal undone_at: %w", err)
		}
		e.UndoneAt = &t
	}
	return e, nil
}

// MarkUndone implements executor.Journal.
func (j *Journal) MarkUndone(ctx context.Context, id int64, at time.Time) error {
	res, err := j.db.ExecContext(ctx, `UPDATE journal SET undone_at = ? WHERE id = ?`,
		at.UTC().Format(timeLayout), id)
	if err != nil {
		return fmt.Errorf("mark journal entry undone: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return executor.ErrJournalEntryNotFound
	}
	return nil
}

func formatNullable(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(timeLayout)
}
