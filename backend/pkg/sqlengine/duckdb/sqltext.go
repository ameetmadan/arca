package duckdb

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Text handling shared by the CGo engine and its tests. Nothing here needs
// DuckDB, so it is built and tested without the duckdb tag.

// MaxResultRows caps the rows a natural-language query returns.
const MaxResultRows = 500

// metaTable records where each imported table came from. Tables whose name
// starts with "_arca" are internal and never shown to the user or the LLM.
const metaTable = "_arca_tables"

var sqlFenceRe = regexp.MustCompile("(?is)```(?:sql)?\\s*(.*?)\\s*```")

// extractSQL pulls the first SQL statement out of an LLM response: the body
// of a ``` fence if there is one, otherwise everything from the first line
// that starts with SELECT or WITH up to a blank line.
func extractSQL(raw string) string {
	if m := sqlFenceRe.FindStringSubmatch(raw); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
			continue
		}
		var stmt []string
		for _, l := range lines[i:] {
			if strings.TrimSpace(l) == "" {
				break
			}
			stmt = append(stmt, l)
		}
		return strings.TrimSpace(strings.Join(stmt, "\n"))
	}
	return strings.TrimSpace(raw)
}

// Generated SQL may only read the imported tables. These words would let it
// change data, reach the file system or the network, or load extensions.
var forbiddenSQL = regexp.MustCompile(`(?i)\b(` +
	`insert|update|delete|merge|create|drop|alter|truncate|replace|copy|export|import|` +
	`attach|detach|install|load|pragma|set|reset|call|checkpoint|vacuum|use|begin|commit|rollback` +
	`)\b`)

// quotedRe matches "quoted identifiers" and 'string literals' (with doubled
// quotes as escapes).
var quotedRe = regexp.MustCompile(`"(?:[^"]|"")*"|'(?:[^']|'')*'`)

// Table functions that read files or URLs.
var forbiddenFuncs = regexp.MustCompile(`(?i)\b(read_\w+|\w+_scan|glob|sniff_csv|query|query_table|getenv)\s*\(`)

// Errors from checkReadOnly.
var (
	ErrEmptySQL      = errors.New("the model did not produce a SQL query")
	ErrNotReadOnly   = errors.New("the generated query was not a plain SELECT, so it was not run")
	ErrMultipleStmts = errors.New("the generated SQL contained more than one statement, so it was not run")
)

// checkReadOnly returns sql without trailing semicolons if it is a single
// SELECT (or WITH ... SELECT) statement that only reads tables. It is a
// conservative filter: a query whose string literals contain a forbidden
// word is refused too, which is acceptable for generated SQL.
func checkReadOnly(sql string) (string, error) {
	s := strings.TrimSpace(sql)
	s = strings.TrimRight(s, "; \n\t")
	if s == "" {
		return "", ErrEmptySQL
	}
	// Keywords are looked for outside quoted identifiers and literals, so a
	// column called "Update Date" is fine.
	bare := quotedRe.ReplaceAllString(s, `""`)
	if strings.Contains(bare, ";") {
		return "", ErrMultipleStmts
	}
	upper := strings.ToUpper(bare)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return "", ErrNotReadOnly
	}
	if m := forbiddenSQL.FindString(bare); m != "" {
		return "", fmt.Errorf("%w (it uses %s)", ErrNotReadOnly, strings.ToUpper(m))
	}
	if m := forbiddenFuncs.FindString(bare); m != "" {
		return "", fmt.Errorf("%w (it calls %s)", ErrNotReadOnly, strings.TrimSpace(strings.TrimSuffix(m, "(")))
	}
	if strings.Contains(s, "'/") || strings.Contains(strings.ToLower(s), "'http") || strings.Contains(s, "'~") {
		return "", fmt.Errorf("%w (it refers to a file or URL)", ErrNotReadOnly)
	}
	return s, nil
}

// tableName derives a SQL identifier from a file name and, for workbooks
// with several sheets, the sheet name: lower case, [a-z0-9_] only, never
// starting with a digit or "_arca".
func tableName(path, sheet string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if sheet != "" {
		base += "_" + sheet
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(base) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "table"
	}
	if name[0] >= '0' && name[0] <= '9' || strings.HasPrefix(name, "arca") {
		name = "t_" + name
	}
	if len(name) > 60 {
		name = strings.TrimRight(name[:60], "_")
	}
	return name
}

// quoteLiteral returns s as a SQL string literal.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// importExts are the file types ImportFile accepts.
var importExts = map[string]bool{".csv": true, ".tsv": true, ".xlsx": true, ".parquet": true, ".json": true}

// canImport reports whether ImportFile accepts the file at path.
func canImport(path string) bool {
	return importExts[strings.ToLower(filepath.Ext(path))]
}

// CanImport implements sqlengine.TableManager.
func (e *DuckDBEngine) CanImport(path string) bool { return canImport(path) }

// sqlPrompt is the instruction given to the LLM to write a query.
func sqlPrompt(schema, question string) string {
	return fmt.Sprintf(`You write DuckDB SQL. Given these tables, write ONE SQL SELECT statement that answers the question.

Tables:
%s
Question: %s

Rules:
- Output only the SQL statement: no explanation, no markdown.
- Use only the tables and columns listed above. Quote column names with double quotes if they contain spaces or capitals.
- The statement must be a SELECT (or WITH ... SELECT).

SQL:`, schema, question)
}
