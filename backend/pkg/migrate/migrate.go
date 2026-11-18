// Package migrate upgrades a database's tables in place, so a new Arca release
// can change them without users losing what an older release stored.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// versionTable records how many steps a database has run. The "_arca" prefix
// keeps it out of the tables DuckDB lists for the user and the LLM.
const versionTable = "_arca_schema_version"

// Apply runs the steps db has not run yet, in order and in one transaction,
// and records len(steps) as its version. A failed step leaves db as it was.
//
// Steps are append-only: a database that ran a step never runs it again, so
// editing a released step changes nothing for existing users. Step 1 must
// stay idempotent (IF NOT EXISTS), because databases created before
// versioning have its tables but no recorded version.
func Apply(ctx context.Context, db *sql.DB, steps []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+versionTable+` (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("migrate: create %s: %w", versionTable, err)
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM `+versionTable).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("migrate: read version: %w", err)
	}
	if version > len(steps) {
		return fmt.Errorf("migrate: the database is at version %d but this Arca knows only %d; a newer Arca wrote it", version, len(steps))
	}
	if version == len(steps) {
		return nil
	}
	for i, step := range steps[version:] {
		if _, err := tx.ExecContext(ctx, step); err != nil {
			return fmt.Errorf("migrate: step %d: %w", version+i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+versionTable); err != nil {
		return fmt.Errorf("migrate: record version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+versionTable+` (version) VALUES (?)`, len(steps)); err != nil {
		return fmt.Errorf("migrate: record version: %w", err)
	}
	return tx.Commit()
}
