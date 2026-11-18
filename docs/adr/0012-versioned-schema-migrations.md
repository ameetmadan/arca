# ADR-0012: Each database records a schema version and upgrades in place

- **Status:** Proposed
- **Date:** 2026-11-18
- **Deciders:** Ameet Madan
- **Supersedes / Superseded by:** —

## Context

Arca keeps five databases in `~/Library/Application Support/Arca/`: `decisions.db`, `journal.db`,
`semantic.db` and `vectors.db` (SQLite), and `tables.duckdb` (DuckDB). Each store created its
tables with `CREATE TABLE IF NOT EXISTS` on open and had no record of which schema a file was
written with. That works until the first schema change. A new column then never reaches existing
databases, and beta testers would have to delete their data, including the undo journal, to
upgrade.

## Decision

Each store lists its schema as an ordered, append-only slice of SQL steps. `pkg/migrate.Apply`
runs the steps a database has not run yet, in one transaction, and records the count in a
one-row `_arca_schema_version` table in that database. Step 1 is each store's schema from before
versioning. It stays `IF NOT EXISTS`, so databases written by 0.1.0-beta.1 and beta.2 (no version
table) adopt version 1 without changes. A database with a higher version than the running build
knows is refused with an error rather than opened.

## Alternatives considered

- **`PRAGMA user_version`:** the idiomatic SQLite counter, with no extra table. DuckDB has no
  equivalent, and one mechanism for all five files is easier to follow than two.
- **A migration library (goose, golang-migrate):** files on disk, up/down pairs and a CLI. Arca
  embeds a handful of steps per store and never migrates down, so that is more machinery than
  the problem needs.
- **Recreate derived databases on a schema change:** fine for `vectors.db` and `semantic.db`,
  which can be rebuilt, but not for `decisions.db` and `journal.db`, which hold history and undo.

## Consequences

- **Positive:** a release can add a column or table and existing users keep their data. A failed
  upgrade leaves the database as it was.
- **Negative / trade-offs:** released steps must never be edited, only followed by new ones.
  Opening a newer database with an older build fails instead of guessing, so downgrading after a
  schema change needs the newer build again.
- **Follow-ups:** none.
