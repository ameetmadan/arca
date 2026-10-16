//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/xuri/excelize/v2"

	"github.com/ameetmadan/arca/pkg/llm"
	"github.com/ameetmadan/arca/pkg/sqlengine"
)

// sampleRows is how many rows of each table the LLM sees with the schema.
const sampleRows = 3

// DuckDBEngine translates natural language to SQL via an LLMProvider
// then executes the result against a local DuckDB database.
type DuckDBEngine struct {
	DBPath string
	db     *sql.DB
	llm    llm.LLMProvider
}

var _ sqlengine.TableManager = (*DuckDBEngine)(nil)

func New(dbPath string, llmProvider llm.LLMProvider) (*DuckDBEngine, error) {
	// Never download or auto-load extensions: everything stays on the Mac.
	dsn := dbPath + "?autoinstall_known_extensions=false&autoload_known_extensions=false"
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("duckdb: open %s: %w", dbPath, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("duckdb: ping %s: %w", dbPath, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + metaTable + ` (
		name TEXT PRIMARY KEY, source TEXT NOT NULL, imported_at TIMESTAMP NOT NULL)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("duckdb: create %s: %w", metaTable, err)
	}
	return &DuckDBEngine{DBPath: dbPath, db: db, llm: llmProvider}, nil
}

func (e *DuckDBEngine) Close() { e.db.Close() }

// QueryNL translates the natural language question to SQL using the LLM,
// then executes it and returns structured results.
func (e *DuckDBEngine) QueryNL(ctx context.Context, question, schemaHint string) (sqlengine.QueryResult, error) {
	schema := schemaHint
	if schema == "" {
		var err error
		schema, err = e.describeTables(ctx)
		if err != nil {
			return sqlengine.QueryResult{}, fmt.Errorf("duckdb: describe tables: %w", err)
		}
		if schema == "" {
			return sqlengine.QueryResult{}, sqlengine.ErrNoTables
		}
	}

	raw, err := e.llm.GenerateSync(ctx, sqlPrompt(schema, question))
	if err != nil {
		return sqlengine.QueryResult{}, fmt.Errorf("duckdb: generate sql: %w", err)
	}
	query, err := checkReadOnly(extractSQL(raw))
	if err != nil {
		return sqlengine.QueryResult{SQL: extractSQL(raw)}, err
	}
	return e.execSQL(ctx, query)
}

// describeTables lists the user's tables with their columns and a few
// sample rows, which helps a small model pick the right columns and values.
func (e *DuckDBEngine) describeTables(ctx context.Context) (string, error) {
	tables, err := e.Tables(ctx)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, t := range tables {
		cols := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			cols[i] = fmt.Sprintf("%q %s", c.Name, c.Type)
		}
		fmt.Fprintf(&sb, "Table %s (%d rows): %s\n", t.Name, t.Rows, strings.Join(cols, ", "))
		sample, err := e.execSQL(ctx, fmt.Sprintf(`SELECT * FROM %q LIMIT %d`, t.Name, sampleRows))
		if err != nil {
			continue
		}
		for _, row := range sample.Rows {
			vals := make([]string, len(row))
			for i, v := range row {
				vals[i] = fmt.Sprint(v)
			}
			fmt.Fprintf(&sb, "  example row: %s\n", strings.Join(vals, " | "))
		}
	}
	return sb.String(), nil
}

// execSQL runs a read-only query, returning at most MaxResultRows rows.
func (e *DuckDBEngine) execSQL(ctx context.Context, query string) (sqlengine.QueryResult, error) {
	result := sqlengine.QueryResult{SQL: query}
	wrapped := fmt.Sprintf("SELECT * FROM (%s) AS q LIMIT %d", query, MaxResultRows+1)
	rows, err := e.db.QueryContext(ctx, wrapped)
	if err != nil {
		return result, fmt.Errorf("duckdb: the query failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return result, err
	}
	result.Columns = cols

	scanBuf := make([]interface{}, len(cols))
	scanPtrs := make([]interface{}, len(cols))
	for i := range scanBuf {
		scanPtrs[i] = &scanBuf[i]
	}
	for rows.Next() {
		if len(result.Rows) == MaxResultRows {
			result.Truncated = true
			break
		}
		if err := rows.Scan(scanPtrs...); err != nil {
			return result, err
		}
		row := make([]interface{}, len(cols))
		for i, v := range scanBuf {
			row[i] = jsonValue(v)
		}
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}

// jsonValue converts a DuckDB value to something encoding/json writes as a
// plain string, number, bool or null.
func jsonValue(v interface{}) interface{} {
	switch x := v.(type) {
	case nil, bool, string, int8, int16, int32, int64, int, uint8, uint16, uint32, uint64:
		return x
	case float32:
		return jsonValue(float64(x))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return x
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format("2006-01-02")
		}
		return x.Format(time.RFC3339)
	case []byte:
		return string(x)
	case interface{ Float64() float64 }: // duckdb.Decimal
		return jsonValue(x.Float64())
	case fmt.Stringer: // *big.Int (HUGEINT), UUID, ...
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

// ImportFile loads a CSV, TSV, Parquet or JSON file into one table, or an
// .xlsx workbook into one table per non-empty sheet.
func (e *DuckDBEngine) ImportFile(ctx context.Context, path string) ([]sqlengine.Table, error) {
	if !canImport(path) {
		return nil, fmt.Errorf("duckdb: cannot import %s files (use .csv, .tsv, .xlsx, .parquet or .json)", filepath.Ext(path))
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("duckdb: %s cannot be read", filepath.Base(path))
	}
	var names []string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx":
		var err error
		if names, err = e.importXLSX(ctx, path); err != nil {
			return nil, err
		}
	case ".parquet":
		name := tableName(path, "")
		if err := e.createFrom(ctx, name, path, "read_parquet("+quoteLiteral(path)+")"); err != nil {
			return nil, err
		}
		names = []string{name}
	case ".json":
		name := tableName(path, "")
		if err := e.createFrom(ctx, name, path, "read_json_auto("+quoteLiteral(path)+")"); err != nil {
			return nil, err
		}
		names = []string{name}
	default: // .csv, .tsv
		name := tableName(path, "")
		if err := e.createFrom(ctx, name, path, "read_csv_auto("+quoteLiteral(path)+", header=true)"); err != nil {
			return nil, err
		}
		names = []string{name}
	}

	all, err := e.Tables(ctx)
	if err != nil {
		return nil, err
	}
	var out []sqlengine.Table
	for _, t := range all {
		for _, n := range names {
			if t.Name == n {
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// importXLSX writes each non-empty sheet to a temporary CSV and loads that,
// which avoids DuckDB's excel extension (it would have to be downloaded).
func (e *DuckDBEngine) importXLSX(ctx context.Context, path string) ([]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("duckdb: open workbook: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	tmpDir, err := os.MkdirTemp("", "arca-import-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	var names []string
	for i, sheet := range sheets {
		rows, err := f.GetRows(sheet)
		if err != nil || len(rows) < 2 { // need a header and at least one row
			continue
		}
		width := 0
		for _, r := range rows {
			width = max(width, len(r))
		}
		csvPath := filepath.Join(tmpDir, fmt.Sprintf("sheet%d.csv", i))
		if err := writeCSV(csvPath, rows, width); err != nil {
			return nil, err
		}
		sheetSuffix := ""
		if len(sheets) > 1 {
			sheetSuffix = sheet
		}
		name := tableName(path, sheetSuffix)
		if err := e.createFrom(ctx, name, path, "read_csv_auto("+quoteLiteral(csvPath)+", header=true)"); err != nil {
			return nil, fmt.Errorf("sheet %q: %w", sheet, err)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, errors.New("duckdb: the workbook has no sheet with a header row and data")
	}
	return names, nil
}

func writeCSV(path string, rows [][]string, width int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	for i, r := range rows {
		padded := make([]string, width)
		copy(padded, r)
		if i == 0 { // DuckDB needs a name for every column
			for j := range padded {
				if strings.TrimSpace(padded[j]) == "" {
					padded[j] = fmt.Sprintf("column%d", j+1)
				}
			}
		}
		if err := w.Write(padded); err != nil {
			f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// createFrom (re)creates table name from a table function and records its
// source file.
func (e *DuckDBEngine) createFrom(ctx context.Context, name, source, from string) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE OR REPLACE TABLE %q AS SELECT * FROM %s`, name, from)); err != nil {
		return fmt.Errorf("duckdb: import %s: %w", filepath.Base(source), err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO `+metaTable+` (name, source, imported_at) VALUES (?, ?, ?)`,
		name, source, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// Tables lists the imported tables with their columns and row counts.
func (e *DuckDBEngine) Tables(ctx context.Context) ([]sqlengine.Table, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT c.table_name, c.column_name, c.data_type, coalesce(m.source, '')
		FROM information_schema.columns c
		LEFT JOIN `+metaTable+` m ON m.name = c.table_name
		WHERE c.table_schema = 'main' AND c.table_name NOT LIKE '\_arca%' ESCAPE '\'
		ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []sqlengine.Table
	for rows.Next() {
		var tbl, col, typ, src string
		if err := rows.Scan(&tbl, &col, &typ, &src); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Name != tbl {
			out = append(out, sqlengine.Table{Name: tbl, Source: src})
		}
		t := &out[len(out)-1]
		t.Columns = append(t.Columns, sqlengine.Column{Name: col, Type: typ})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		_ = e.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %q`, out[i].Name)).Scan(&out[i].Rows)
	}
	return out, nil
}

// DropTable removes an imported table.
func (e *DuckDBEngine) DropTable(ctx context.Context, name string) error {
	tables, err := e.Tables(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, t := range tables {
		found = found || t.Name == name
	}
	if !found {
		return sqlengine.ErrUnknownTable
	}
	if _, err := e.db.ExecContext(ctx, fmt.Sprintf(`DROP TABLE %q`, name)); err != nil {
		return err
	}
	_, err = e.db.ExecContext(ctx, `DELETE FROM `+metaTable+` WHERE name = ?`, name)
	return err
}
