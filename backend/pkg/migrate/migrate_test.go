//go:build cgo

package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func version(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT version FROM ` + versionTable).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func columns(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return strings.Join(names, ",")
}

// The steps are deliberately not idempotent, so running one twice fails.
var steps = []string{
	`CREATE TABLE notes (id INTEGER PRIMARY KEY)`,
	`ALTER TABLE notes ADD COLUMN body TEXT NOT NULL DEFAULT ''`,
}

func TestApplyRunsEveryStepOnAFreshDatabase(t *testing.T) {
	db := openDB(t)
	if err := Apply(context.Background(), db, steps); err != nil {
		t.Fatal(err)
	}
	if got := version(t, db); got != 2 {
		t.Fatalf("version = %d, want 2", got)
	}
	if got := columns(t, db, "notes"); got != "id,body" {
		t.Fatalf("columns = %q", got)
	}
}

func TestApplyRunsOnlyNewSteps(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := Apply(ctx, db, steps[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes (id) VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, db, steps); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if err := Apply(ctx, db, steps); err != nil {
		t.Fatalf("second open of an up-to-date database: %v", err)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM notes WHERE id = 7`).Scan(&body); err != nil {
		t.Fatalf("row written before the upgrade: %v", err)
	}
	if got := version(t, db); got != 2 {
		t.Fatalf("version = %d, want 2", got)
	}
}

func TestApplyAdoptsADatabaseFromBeforeVersioning(t *testing.T) {
	db := openDB(t)
	schema := `CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY)`
	if _, err := db.Exec(schema + `; INSERT INTO notes (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), db, []string{schema}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, err = %v; want the existing row kept", n, err)
	}
	if got := version(t, db); got != 1 {
		t.Fatalf("version = %d, want 1", got)
	}
}

func TestApplyRollsBackAFailedUpgrade(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := Apply(ctx, db, steps[:1]); err != nil {
		t.Fatal(err)
	}
	broken := append(append([]string{}, steps...), `ALTER TABLE missing ADD COLUMN x TEXT`)
	err := Apply(ctx, db, broken)
	if err == nil || !strings.Contains(err.Error(), "step 3") {
		t.Fatalf("err = %v, want a step 3 failure", err)
	}
	if got := version(t, db); got != 1 {
		t.Fatalf("version = %d, want 1", got)
	}
	if got := columns(t, db, "notes"); got != "id" {
		t.Fatalf("columns = %q, want step 2 rolled back", got)
	}
}

func TestApplyRefusesADatabaseFromANewerRelease(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := Apply(ctx, db, steps); err != nil {
		t.Fatal(err)
	}
	err := Apply(ctx, db, steps[:1])
	if err == nil || !strings.Contains(err.Error(), "newer Arca") {
		t.Fatalf("err = %v, want a newer-release error", err)
	}
}
