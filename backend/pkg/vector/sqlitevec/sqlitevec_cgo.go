//go:build sqlitevec

package sqlitevec

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
	dim    int // read from an existing vec_store on open, else set on first Upsert
}

// vecDimRe reads the dimension out of vec_store's CREATE statement.
var vecDimRe = regexp.MustCompile(`float\[(\d+)\]`)

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

	store := &SQLiteVecStore{DBPath: dbPath, db: db}
	if err := store.loadSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// loadSchema picks up the dimension of an existing vec_store, so search
// works after a restart before anything new is indexed, and drops vectors
// whose metadata row is gone (left behind by older versions).
func (s *SQLiteVecStore) loadSchema() error {
	var ddl string
	err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'vec_store'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqlitevec: read schema: %w", err)
	}
	m := vecDimRe.FindStringSubmatch(ddl)
	if m == nil {
		return fmt.Errorf("sqlitevec: cannot read the vector dimension from %q", ddl)
	}
	s.dim, _ = strconv.Atoi(m[1])
	if _, err := s.db.Exec(`DELETE FROM vec_store WHERE rowid NOT IN (SELECT rowid FROM vec_meta)`); err != nil {
		return fmt.Errorf("sqlitevec: drop orphaned vectors: %w", err)
	}
	return nil
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

	// vec_meta's rowid is the key both tables share: the vector is stored
	// under the same rowid as its metadata, so the JOIN in Search can never
	// pair a vector with another chunk's text.
	var rowid int64
	if err := tx.QueryRow(`INSERT INTO vec_meta(id, metadata) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET metadata = excluded.metadata
		RETURNING rowid`, id, string(metaJSON)).Scan(&rowid); err != nil {
		return fmt.Errorf("sqlitevec: upsert meta: %w", err)
	}
	// vec0 has no upsert; replace the vector.
	if _, err := tx.Exec(`DELETE FROM vec_store WHERE rowid = ?`, rowid); err != nil {
		return fmt.Errorf("sqlitevec: delete old vec: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO vec_store(rowid, embedding) VALUES (?, ?)`, rowid, blob); err != nil {
		return fmt.Errorf("sqlitevec: insert vec: %w", err)
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
		WHERE v.embedding MATCH ? AND k = ?
		ORDER BY v.distance`, blob, topK)
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

var _ vector.ChunkDeleter = (*SQLiteVecStore)(nil)

// DeleteChunks implements vector.ChunkDeleter.
//
// The prefix is matched with instr(), not LIKE, so "%" and "_" in file paths
// are not wildcards. What follows the prefix must be all digits, so the chunks
// of "/a/b" never match those of a file called "/a/b#1".
func (s *SQLiteVecStore) DeleteChunks(ctx context.Context, prefix string, from int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// Names the rows to delete; vec_store has no id column, only rowids.
	const match = `FROM vec_meta WHERE instr(id, ?1) = 1
		AND substr(id, length(?1) + 1) != ''
		AND substr(id, length(?1) + 1) NOT GLOB '*[^0-9]*'
		AND CAST(substr(id, length(?1) + 1) AS INTEGER) >= ?2`

	// vec_store only exists once an embedding has fixed the dimension. s.dim
	// is not enough to tell: it is zero again after a restart.
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'vec_store'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM vec_store WHERE rowid IN (SELECT rowid `+match+`)`,
			prefix, from); err != nil {
			return fmt.Errorf("sqlitevec: delete chunk vectors: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE `+match, prefix, from); err != nil {
		return fmt.Errorf("sqlitevec: delete chunk metadata: %w", err)
	}
	return tx.Commit()
}

// Ping reports whether the underlying database connection is usable.
func (s *SQLiteVecStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
