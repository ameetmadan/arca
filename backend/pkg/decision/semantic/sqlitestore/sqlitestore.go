// Package sqlitestore is a SQLite-backed semantic.FolderStore, so the folder
// index survives restarts instead of being rebuilt in the background each time.
//
// It uses the mattn/go-sqlite3 driver already used by the executor journal.
// The driver needs CGo. Without CGo the package still compiles, but Open
// returns an error, so callers should fall back to semantic.MemoryStore.
//
// Each folder is one row in folders plus its example vectors in examples.
// Put replaces both in a single transaction, so readers never see a folder
// half-updated. The folder's Fingerprint is stored with it: the Indexer
// compares it on the next run and re-embeds only folders that changed.
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"

	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver

	"github.com/ameetmadan/arca/pkg/decision/semantic"
)

const schema = `
CREATE TABLE IF NOT EXISTS folders (
	root        TEXT NOT NULL,
	rel         TEXT NOT NULL,
	file_count  INTEGER NOT NULL,
	fingerprint TEXT NOT NULL,
	centroid    BLOB NOT NULL,
	PRIMARY KEY (root, rel)
);
CREATE TABLE IF NOT EXISTS examples (
	root   TEXT NOT NULL,
	rel    TEXT NOT NULL,
	pos    INTEGER NOT NULL,
	name   TEXT NOT NULL,
	vector BLOB NOT NULL,
	PRIMARY KEY (root, rel, pos),
	FOREIGN KEY (root, rel) REFERENCES folders (root, rel) ON DELETE CASCADE
);
`

// Store is a SQLite-backed semantic.FolderStore.
type Store struct {
	db *sql.DB
}

var _ semantic.FolderStore = (*Store)(nil)

// Open opens (creating if needed) the folder index database at path.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Set("_busy_timeout", "5000")
	q.Set("_journal_mode", "WAL")
	q.Set("_foreign_keys", "on")
	dsn := "file:" + url.PathEscape(path) + "?" + q.Encode()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open folder store: %w", err)
	}
	// A single connection serialises writes and keeps SQLite locking simple.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("init folder store schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Get implements semantic.FolderStore.
func (s *Store) Get(ctx context.Context, root, rel string) (semantic.FolderIndex, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return semantic.FolderIndex{}, false, fmt.Errorf("get folder: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var (
		f        semantic.FolderIndex
		centroid []byte
	)
	err = tx.QueryRowContext(ctx,
		`SELECT root, rel, file_count, fingerprint, centroid FROM folders WHERE root = ? AND rel = ?`,
		root, rel).Scan(&f.Root, &f.Rel, &f.FileCount, &f.Fingerprint, &centroid)
	if errors.Is(err, sql.ErrNoRows) {
		return semantic.FolderIndex{}, false, nil
	}
	if err != nil {
		return semantic.FolderIndex{}, false, fmt.Errorf("get folder: %w", err)
	}
	if f.Centroid, err = decodeVector(centroid); err != nil {
		return semantic.FolderIndex{}, false, fmt.Errorf("get folder: centroid: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT name, vector FROM examples WHERE root = ? AND rel = ? ORDER BY pos`, root, rel)
	if err != nil {
		return semantic.FolderIndex{}, false, fmt.Errorf("get folder examples: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ex  semantic.Example
			raw []byte
		)
		if err := rows.Scan(&ex.Name, &raw); err != nil {
			return semantic.FolderIndex{}, false, fmt.Errorf("get folder examples: %w", err)
		}
		if ex.Vector, err = decodeVector(raw); err != nil {
			return semantic.FolderIndex{}, false, fmt.Errorf("get folder examples: %w", err)
		}
		f.Examples = append(f.Examples, ex)
	}
	if err := rows.Err(); err != nil {
		return semantic.FolderIndex{}, false, fmt.Errorf("get folder examples: %w", err)
	}
	return f, true, nil
}

// Put implements semantic.FolderStore.
func (s *Store) Put(ctx context.Context, f semantic.FolderIndex) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("put folder: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Replace the folder and its examples as one unit.
	if _, err := tx.ExecContext(ctx, `DELETE FROM folders WHERE root = ? AND rel = ?`, f.Root, f.Rel); err != nil {
		return fmt.Errorf("put folder: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO folders (root, rel, file_count, fingerprint, centroid) VALUES (?, ?, ?, ?, ?)`,
		f.Root, f.Rel, f.FileCount, f.Fingerprint, encodeVector(f.Centroid)); err != nil {
		return fmt.Errorf("put folder: %w", err)
	}
	for i, ex := range f.Examples {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO examples (root, rel, pos, name, vector) VALUES (?, ?, ?, ?, ?)`,
			f.Root, f.Rel, i, ex.Name, encodeVector(ex.Vector)); err != nil {
			return fmt.Errorf("put folder example: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("put folder: %w", err)
	}
	return nil
}

// Delete implements semantic.FolderStore. Examples go with the folder through
// the foreign key's ON DELETE CASCADE.
func (s *Store) Delete(ctx context.Context, root, rel string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM folders WHERE root = ? AND rel = ?`, root, rel); err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	return nil
}

// List implements semantic.FolderStore. SQLite's default BINARY collation
// orders text bytewise, which matches Go's string comparison.
func (s *Store) List(ctx context.Context) ([]semantic.FolderIndex, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var out []semantic.FolderIndex
	pos := map[[2]string]int{}
	rows, err := tx.QueryContext(ctx,
		`SELECT root, rel, file_count, fingerprint, centroid FROM folders ORDER BY root, rel`)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	for rows.Next() {
		var (
			f   semantic.FolderIndex
			raw []byte
		)
		if err := rows.Scan(&f.Root, &f.Rel, &f.FileCount, &f.Fingerprint, &raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list folders: %w", err)
		}
		if f.Centroid, err = decodeVector(raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list folders: centroid: %w", err)
		}
		pos[[2]string{f.Root, f.Rel}] = len(out)
		out = append(out, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}

	// One query for all examples rather than one per folder.
	erows, err := tx.QueryContext(ctx, `SELECT root, rel, name, vector FROM examples ORDER BY root, rel, pos`)
	if err != nil {
		return nil, fmt.Errorf("list folder examples: %w", err)
	}
	defer erows.Close()
	for erows.Next() {
		var (
			root, rel string
			ex        semantic.Example
			raw       []byte
		)
		if err := erows.Scan(&root, &rel, &ex.Name, &raw); err != nil {
			return nil, fmt.Errorf("list folder examples: %w", err)
		}
		if ex.Vector, err = decodeVector(raw); err != nil {
			return nil, fmt.Errorf("list folder examples: %w", err)
		}
		if i, ok := pos[[2]string{root, rel}]; ok {
			out[i].Examples = append(out[i].Examples, ex)
		}
	}
	if err := erows.Err(); err != nil {
		return nil, fmt.Errorf("list folder examples: %w", err)
	}
	return out, nil
}

// encodeVector stores a vector as little-endian float32s. A nil vector is an
// empty blob.
func encodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("corrupt vector: %d bytes is not a multiple of 4", len(b))
	}
	if len(b) == 0 {
		return nil, nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
