package duckdb

import (
	"errors"
	"testing"
)

func TestExtractSQL(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"fence":         {"Here you go:\n```sql\nSELECT 1\nFROM t\n```\nthanks", "SELECT 1\nFROM t"},
		"bare fence":    {"```\nSELECT a FROM t\n```", "SELECT a FROM t"},
		"multi-line":    {"Sure.\nSELECT a,\n  b\nFROM t\nWHERE a > 1;\n\nThis selects a.", "SELECT a,\n  b\nFROM t\nWHERE a > 1;"},
		"with":          {"WITH x AS (SELECT 1) SELECT * FROM x", "WITH x AS (SELECT 1) SELECT * FROM x"},
		"no sql at all": {"I cannot answer that", "I cannot answer that"},
	} {
		if got := extractSQL(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestCheckReadOnlyAccepts(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM sales;",
		"select region, sum(amount) from sales group by region order by 2 desc",
		`WITH t AS (SELECT "Update Date" FROM sales) SELECT * FROM t`,
		"SELECT updated_at, deleted FROM sales", // words inside identifiers are fine
	} {
		if _, err := checkReadOnly(q); err != nil {
			t.Errorf("%q rejected: %v", q, err)
		}
	}
}

func TestCheckReadOnlyRejects(t *testing.T) {
	for q, want := range map[string]error{
		"":                                              ErrEmptySQL,
		"DROP TABLE sales":                              ErrNotReadOnly,
		"SELECT 1; DROP TABLE sales":                    ErrMultipleStmts,
		"COPY sales TO '/tmp/x.csv'":                    ErrNotReadOnly,
		"SELECT * FROM read_csv('/etc/passwd')":         ErrNotReadOnly,
		"SELECT * FROM '/Users/me/secret.csv'":          ErrNotReadOnly,
		"SELECT * FROM 'https://example.com/x'":         ErrNotReadOnly,
		"WITH x AS (SELECT 1) INSERT INTO t VALUES (1)": ErrNotReadOnly,
		"SELECT getenv('HOME')":                         ErrNotReadOnly,
		"SELECT * FROM parquet_scan('x')":               ErrNotReadOnly,
		"PRAGMA show_tables":                            ErrNotReadOnly,
		"SELECT 1 FROM t WHERE a = 1 -- ; SET x=1":      ErrMultipleStmts,
	} {
		_, err := checkReadOnly(q)
		if !errors.Is(err, want) {
			t.Errorf("%q: err = %v, want %v", q, err, want)
		}
	}
}

func TestTableName(t *testing.T) {
	for _, tc := range []struct{ path, sheet, want string }{
		{"/x/Sales Report 2024.xlsx", "", "sales_report_2024"},
		{"/x/Sales.xlsx", "Q1 Data", "sales_q1_data"},
		{"/x/2024-budget.csv", "", "t_2024_budget"},
		{"/x/über.csv", "", "ber"},
		{"/x/___.csv", "", "table"},
		{"/x/arca_tables.csv", "", "t_arca_tables"},
	} {
		if got := tableName(tc.path, tc.sheet); got != tc.want {
			t.Errorf("tableName(%q, %q) = %q, want %q", tc.path, tc.sheet, got, tc.want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	if got := quoteLiteral("/a/it's.csv"); got != "'/a/it''s.csv'" {
		t.Fatal(got)
	}
}
