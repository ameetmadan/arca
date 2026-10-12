//go:build sqlitevec

package sqlitevec

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/ameetmadan/arca/pkg/vector"
)

func init() {
	vec.Auto() // register the sqlite-vec extension for every new connection
}

// SQLiteVecStore persists embeddings using sqlite-vec's vec0 virtual table.
// The embedding dimension is detected on first Upsert and the schema is
// created lazily so callers don't need to know the dimension up-front.
type SQLiteVecStore struct {
	DBPath string
	db     *sql.DB
	mu     sync.Mutex
	dim    int // set on first Upsert
}

func New(dbPath string) (*SQLiteVecStore, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("sqlitevec: open %s: %w", dbPath, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitevec: ping %s: %w", dbPath, err)
	}

	// Metadata table is always created eagerly.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS vec_meta (
		id       TEXT PRIMARY KEY,
		metadata TEXT NOT NULL DEFAULT '{}'
	)`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitevec: create vec_meta: %w", err)
	}

	return &SQLiteVecStore{DBPath: dbPath, db: db}, nil
}

func (s *SQLiteVecStore) Close() { s.db.Close() }

// ensureSchema creates the vec0 virtual table once we know the embedding dimension.
func (s *SQLiteVecStore) ensureSchema(dim int) error {
	if s.dim == dim {
		return nil
	}
	if s.dim != 0 && s.dim != dim {
		return fmt.Errorf("sqlitevec: dimension mismatch: store is %d, got %d", s.dim, dim)
	}
	_, err := s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS vec_store
		USING vec0(embedding float[%d] distance_metric=cosine)`, dim))
	if err != nil {
		return fmt.Errorf("sqlitevec: create vec_store: %w", err)
	}
	s.dim = dim
	return nil
}

func (s *SQLiteVecStore) Upsert(_ context.Context, id string, embedding []float32, metadata map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureSchema(len(embedding)); err != nil {
		return err
	}

	blob, err := vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("sqlitevec: serialize: %w", err)
	}

	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("sqlitevec: marshal metadata: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// vec0 doesn't support ON CONFLICT directly; delete then insert.
	if _, err := tx.Exec(`DELETE FROM vec_store WHERE rowid = (
		SELECT rowid FROM vec_meta WHERE id = ?)`, id); err != nil {
		return fmt.Errorf("sqlitevec: delete old vec: %w", err)
	}
	res, err := tx.Exec(`INSERT INTO vec_store(embedding) VALUES (?)`, blob)
	if err != nil {
		return fmt.Errorf("sqlitevec: insert vec: %w", err)
	}
	rowid, _ := res.LastInsertId()

	if _, err := tx.Exec(`INSERT INTO vec_meta(id, metadata) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET metadata = excluded.metadata, rowid = ?`,
		id, string(metaJSON), rowid); err != nil {
		// Fallback for SQLite versions that don't support rowid in upsert.
		if _, err2 := tx.Exec(`INSERT OR REPLACE INTO vec_meta(id, metadata) VALUES (?, ?)`,
			id, string(metaJSON)); err2 != nil {
			return fmt.Errorf("sqlitevec: upsert meta: %w", err2)
		}
	}

	return tx.Commit()
}

func (s *SQLiteVecStore) Search(_ context.Context, query []float32, topK int) ([]vector.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dim == 0 {
		return nil, nil // nothing indexed yet
	}

	blob, err := vec.SerializeFloat32(query)
	if err != nil {
		return nil, fmt.Errorf("sqlitevec: serialize query: %w", err)
	}

	rows, err := s.db.Query(`
		SELECT m.id, m.metadata, v.distance
		FROM vec_store v
		JOIN vec_meta m ON m.rowid = v.rowid
		WHERE v.embedding MATCH ?
		ORDER BY v.distance
		LIMIT ?`, blob, topK)
	if err != nil {
		return nil, fmt.Errorf("sqlitevec: search: %w", err)
	}
	defer rows.Close()

	var results []vector.SearchResult
	for rows.Next() {
		var id, metaStr string
		var dist float32
		if err := rows.Scan(&id, &metaStr, &dist); err != nil {
			return nil, err
		}
		var meta map[string]interface{}
		_ = json.Unmarshal([]byte(metaStr), &meta)
		results = append(results, vector.SearchResult{
			ID:       id,
			Score:    1 - dist, // cosine distance → similarity
			Metadata: meta,
		})
	}
	return results, rows.Err()
}

func (s *SQLiteVecStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var rowid int64
	err = tx.QueryRow(`SELECT rowid FROM vec_meta WHERE id = ?`, id).Scan(&rowid)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM vec_store WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM vec_meta WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Ping reports whether the underlying database connection is usable.
func (s *SQLiteVecStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
