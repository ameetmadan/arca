//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/ameetmadan/arca/pkg/llm"
	"github.com/ameetmadan/arca/pkg/sqlengine"
)

// DuckDBEngine translates natural language to SQL via an LLMProvider
// then executes the result against a local DuckDB database.
type DuckDBEngine struct {
	DBPath string
	db     *sql.DB
	llm    llm.LLMProvider
}

func New(dbPath string, llmProvider llm.LLMProvider) (*DuckDBEngine, error) {
	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("duckdb: open %s: %w", dbPath, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("duckdb: ping %s: %w", dbPath, err)
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
		schema, err = e.introspectSchema(ctx)
		if err != nil {
			return sqlengine.QueryResult{}, fmt.Errorf("duckdb: introspect schema: %w", err)
		}
	}

	generatedSQL, err := e.generateSQL(ctx, question, schema)
	if err != nil {
		return sqlengine.QueryResult{}, fmt.Errorf("duckdb: generate sql: %w", err)
	}

	return e.execSQL(ctx, generatedSQL)
}

// introspectSchema queries DuckDB's information_schema to build a schema description.
func (e *DuckDBEngine) introspectSchema(ctx context.Context) (string, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema NOT IN ('information_schema', 'pg_catalog')
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	tables := make(map[string][]string)
	var order []string
	for rows.Next() {
		var tbl, col, typ string
		if err := rows.Scan(&tbl, &col, &typ); err != nil {
			return "", err
		}
		if _, seen := tables[tbl]; !seen {
			order = append(order, tbl)
		}
		tables[tbl] = append(tables[tbl], col+" "+typ)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, tbl := range order {
		sb.WriteString(fmt.Sprintf("Table %s: %s\n", tbl, strings.Join(tables[tbl], ", ")))
	}
	return sb.String(), nil
}

// generateSQL asks the LLM to produce a single SQL SELECT statement.
func (e *DuckDBEngine) generateSQL(ctx context.Context, question, schema string) (string, error) {
	prompt := fmt.Sprintf(`You are a SQL expert. Given the following DuckDB schema, write a single SQL SELECT statement that answers the user's question.

Schema:
%s

Question: %s

Rules:
- Output ONLY the SQL statement, nothing else.
- Do not include explanations, markdown, or backticks.
- Use standard SQL compatible with DuckDB.
- The query must be a SELECT statement.

SQL:`, schema, question)

	raw, err := e.llm.GenerateSync(ctx, prompt)
	if err != nil {
		return "", err
	}

	return extractSQL(raw), nil
}

// extractSQL pulls the first SQL statement out of an LLM response,
// stripping markdown code fences and leading/trailing whitespace.
var sqlFenceRe = regexp.MustCompile("(?is)```(?:sql)?\\s*(.*?)\\s*```")

func extractSQL(raw string) string {
	if m := sqlFenceRe.FindStringSubmatch(raw); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	// No fence — take first non-empty line that looks like SQL.
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "SELECT") ||
			strings.HasPrefix(upper, "WITH") {
			return trimmed
		}
	}
	return strings.TrimSpace(raw)
}

// execSQL runs the generated SQL and returns a QueryResult.
func (e *DuckDBEngine) execSQL(ctx context.Context, query string) (sqlengine.QueryResult, error) {
	rows, err := e.db.QueryContext(ctx, query)
	if err != nil {
		return sqlengine.QueryResult{}, fmt.Errorf("duckdb: exec %q: %w", query, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return sqlengine.QueryResult{}, err
	}

	var result sqlengine.QueryResult
	result.Columns = cols

	scanBuf := make([]interface{}, len(cols))
	scanPtrs := make([]interface{}, len(cols))
	for i := range scanBuf {
		scanPtrs[i] = &scanBuf[i]
	}

	for rows.Next() {
		if err := rows.Scan(scanPtrs...); err != nil {
			return result, err
		}
		row := make([]interface{}, len(cols))
		copy(row, scanBuf)
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}
