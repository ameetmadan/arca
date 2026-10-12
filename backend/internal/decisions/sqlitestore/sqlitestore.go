// Package sqlitestore keeps the decision queue and the per-folder policies in
// SQLite, using the mattn/go-sqlite3 driver already used elsewhere in the
// project.
//
// The driver needs CGo. Without CGo the package still compiles, but Open
// returns an error, so callers should treat a failed Open as "queue
// unavailable" rather than crash. Tests live behind the cgo build constraint
// for that reason.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

const schema = `
CREATE TABLE IF NOT EXISTS decisions (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         TEXT NOT NULL,
	root       TEXT NOT NULL,
	file       TEXT NOT NULL,
	action     TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL,
	reason     TEXT NOT NULL DEFAULT '',
	source     TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL CHECK (status IN ('pending','accepted','rejected','applied','failed')),
	journal_id INTEGER
);
CREATE INDEX IF NOT EXISTS decisions_status_id ON decisions (status, id);
CREATE INDEX IF NOT EXISTS decisions_root_status ON decisions (root, status);

CREATE TABLE IF NOT EXISTS folder_policies (
	root           TEXT PRIMARY KEY,
	mode           TEXT NOT NULL CHECK (mode IN ('off','suggest','auto')),
	auto_threshold REAL NOT NULL
);
`

// timeLayout keeps timestamps sortable and lossless as text.
const timeLayout = time.RFC3339Nano

// Store is a SQLite-backed decisions.Store, decisions.PolicyStore and
// policy.StatsProvider.
type Store struct {
	db *sql.DB
}

var (
	_ decisions.Store       = (*Store)(nil)
	_ decisions.PolicyStore = (*Store)(nil)
	_ policy.StatsProvider  = (*Store)(nil)
)

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Set("_busy_timeout", "5000")
	q.Set("_journal_mode", "WAL")
	dsn := "file:" + url.PathEscape(path) + "?" + q.Encode()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open decisions db: %w", err)
	}
	// A single connection serialises writes and keeps SQLite locking simple.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("init decisions schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Insert implements decisions.Store.
func (s *Store) Insert(ctx context.Context, r decisions.Record) (decisions.Record, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO decisions (ts, root, file, action, target, confidence, reason, source, status, journal_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TS.UTC().Format(timeLayout), r.Root, r.File, string(r.Action), r.Target, r.Confidence,
		r.Reason, r.Source, string(r.Status), nullableInt(r.JournalID))
	if err != nil {
		return decisions.Record{}, fmt.Errorf("insert decision: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return decisions.Record{}, err
	}
	r.ID = id
	return r, nil
}

const selectCols = `id, ts, root, file, action, target, confidence, reason, source, status, journal_id`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (decisions.Record, error) {
	var (
		r       decisions.Record
		ts      string
		action  string
		status  string
		journal sql.NullInt64
	)
	if err := row.Scan(&r.ID, &ts, &r.Root, &r.File, &action, &r.Target, &r.Confidence,
		&r.Reason, &r.Source, &status, &journal); err != nil {
		return decisions.Record{}, err
	}
	t, err := time.Parse(timeLayout, ts)
	if err != nil {
		return decisions.Record{}, fmt.Errorf("parse decision ts: %w", err)
	}
	r.TS = t
	r.Action = decision.Action(action)
	r.Status = decisions.Status(status)
	if journal.Valid {
		v := journal.Int64
		r.JournalID = &v
	}
	return r, nil
}

// Get implements decisions.Store.
func (s *Store) Get(ctx context.Context, id int64) (decisions.Record, error) {
	r, err := scan(s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM decisions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return decisions.Record{}, decisions.ErrNotFound
	}
	if err != nil {
		return decisions.Record{}, fmt.Errorf("read decision: %w", err)
	}
	return r, nil
}

// List implements decisions.Store.
func (s *Store) List(ctx context.Context, f decisions.ListFilter) (decisions.Page, error) {
	var conds []string
	args := []any{}
	if f.Status != "" {
		conds, args = append(conds, "status = ?"), append(args, string(f.Status))
	}
	if f.Root != "" {
		conds, args = append(conds, "root = ?"), append(args, f.Root)
	}
	if f.File != "" {
		conds, args = append(conds, "file = ?"), append(args, f.File)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	page := decisions.Page{Items: []decisions.Record{}, Limit: f.Limit, Offset: f.Offset}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions`+where, args...).Scan(&page.Total); err != nil {
		return decisions.Page{}, fmt.Errorf("count decisions: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM decisions`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, f.Offset)...)
	if err != nil {
		return decisions.Page{}, fmt.Errorf("list decisions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return decisions.Page{}, fmt.Errorf("read decision: %w", err)
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return decisions.Page{}, fmt.Errorf("list decisions: %w", err)
	}
	return page, nil
}

// Transition implements decisions.Store.
func (s *Store) Transition(ctx context.Context, id int64, from, to decisions.Status, u decisions.Update) error {
	// COALESCE keeps the stored value when the update leaves a field nil.
	res, err := s.db.ExecContext(ctx,
		`UPDATE decisions
		    SET status = ?, journal_id = COALESCE(?, journal_id), target = COALESCE(?, target)
		  WHERE id = ? AND status = ?`,
		string(to), nullableInt(u.JournalID), nullableString(u.Target), id, string(from))
	if err != nil {
		return fmt.Errorf("update decision: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM decisions WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return decisions.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read decision: %w", err)
	}
	return decisions.ErrWrongStatus
}

// GetPolicy implements decisions.PolicyStore.
func (s *Store) GetPolicy(ctx context.Context, root string) (policy.Settings, bool, error) {
	var (
		st   policy.Settings
		mode string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT mode, auto_threshold FROM folder_policies WHERE root = ?`, root).Scan(&mode, &st.AutoThreshold)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Settings{}, false, nil
	}
	if err != nil {
		return policy.Settings{}, false, fmt.Errorf("read policy: %w", err)
	}
	st.Mode = policy.Mode(mode)
	return st, true, nil
}

// PutPolicy implements decisions.PolicyStore.
func (s *Store) PutPolicy(ctx context.Context, root string, st policy.Settings) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO folder_policies (root, mode, auto_threshold) VALUES (?, ?, ?)
		 ON CONFLICT(root) DO UPDATE SET mode = excluded.mode, auto_threshold = excluded.auto_threshold`,
		root, string(st.Mode), st.AutoThreshold)
	if err != nil {
		return fmt.Errorf("store policy: %w", err)
	}
	return nil
}

// AcceptanceStats implements policy.StatsProvider. Only decisions the user
// resolved count: accepted ones as acceptances, rejected ones as the
// remainder. Automatically applied, failed and pending decisions say nothing
// about whether the user agrees with the suggestions.
func (s *Store) AcceptanceStats(ctx context.Context, root string) (policy.Stats, error) {
	var st policy.Stats
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(status = 'accepted'), 0)
		   FROM decisions WHERE root = ? AND status IN ('accepted','rejected')`, root).
		Scan(&st.Decided, &st.Accepted)
	if err != nil {
		return policy.Stats{}, fmt.Errorf("acceptance stats: %w", err)
	}
	return st, nil
}

func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
