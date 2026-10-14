//go:build duckdb

package duckdb

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/ameetmadan/arca/pkg/sqlengine"
)

// scriptedLLM answers every prompt with sql and records the prompt.
type scriptedLLM struct {
	sql    string
	prompt string
}

func (s *scriptedLLM) Generate(context.Context, string) (io.Reader, error) {
	return nil, errors.New("not used")
}

func (s *scriptedLLM) GenerateSync(_ context.Context, prompt string) (string, error) {
	s.prompt = prompt
	return s.sql, nil
}

func newEngine(t *testing.T, llm *scriptedLLM) *DuckDBEngine {
	t.Helper()
	e, err := New(filepath.Join(t.TempDir(), "t.duckdb"), llm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImportCSVAndQuery(t *testing.T) {
	llm := &scriptedLLM{sql: "```sql\nSELECT region, sum(amount) AS total FROM sales_2024 GROUP BY region ORDER BY total DESC;\n```"}
	e := newEngine(t, llm)
	ctx := context.Background()

	if _, err := e.QueryNL(ctx, "anything", ""); !errors.Is(err, sqlengine.ErrNoTables) {
		t.Fatalf("query with no tables: %v", err)
	}

	csv := writeFile(t, "Sales 2024.csv", "region,amount,day\nnorth,10,2024-01-01\nsouth,5,2024-01-02\nnorth,7,2024-01-03\n")
	tables, err := e.ImportFile(ctx, csv)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Name != "sales_2024" || tables[0].Rows != 3 || len(tables[0].Columns) != 3 || tables[0].Source != csv {
		t.Fatalf("tables = %+v", tables)
	}

	res, err := e.QueryNL(ctx, "total by region", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || res.Rows[0][0] != "north" || !strings.HasPrefix(res.SQL, "SELECT region") {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(llm.prompt, "sales_2024 (3 rows)") || !strings.Contains(llm.prompt, "example row: north") {
		t.Fatalf("prompt lacks schema or samples:\n%s", llm.prompt)
	}

	// Re-importing replaces the table rather than failing or duplicating.
	if _, err := e.ImportFile(ctx, csv); err != nil {
		t.Fatal(err)
	}
	all, _ := e.Tables(ctx)
	if len(all) != 1 {
		t.Fatalf("tables after re-import: %+v", all)
	}

	if err := e.DropTable(ctx, "sales_2024"); err != nil {
		t.Fatal(err)
	}
	if err := e.DropTable(ctx, "sales_2024"); !errors.Is(err, sqlengine.ErrUnknownTable) {
		t.Fatalf("second drop: %v", err)
	}
	if err := e.DropTable(ctx, metaTable); !errors.Is(err, sqlengine.ErrUnknownTable) {
		t.Fatalf("dropping the internal table: %v", err)
	}
}

func TestImportXLSXOneTablePerSheet(t *testing.T) {
	f := excelize.NewFile()
	f.SetSheetRow("Sheet1", "A1", &[]any{"name", "score"})
	f.SetSheetRow("Sheet1", "A2", &[]any{"ada", 3})
	f.NewSheet("Q2 Data")
	f.SetSheetRow("Q2 Data", "A1", &[]any{"name", ""})
	f.SetSheetRow("Q2 Data", "A2", &[]any{"bob", 4})
	f.NewSheet("Empty")
	path := filepath.Join(t.TempDir(), "Scores.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}

	e := newEngine(t, &scriptedLLM{})
	tables, err := e.ImportFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, t := range tables {
		names = append(names, t.Name)
	}
	if strings.Join(names, ",") != "scores_q2_data,scores_sheet1" {
		t.Fatalf("tables = %v", names)
	}
	if tables[0].Columns[1].Name != "column2" {
		t.Fatalf("blank header not named: %+v", tables[0].Columns)
	}
}

func TestQueryRefusesUnsafeSQL(t *testing.T) {
	e := newEngine(t, &scriptedLLM{sql: "COPY sales TO '/tmp/out.csv'"})
	if _, err := e.ImportFile(context.Background(), writeFile(t, "sales.csv", "a\n1\n")); err != nil {
		t.Fatal(err)
	}
	res, err := e.QueryNL(context.Background(), "q", "")
	if !errors.Is(err, ErrNotReadOnly) {
		t.Fatalf("err = %v", err)
	}
	if res.SQL == "" {
		t.Fatal("refused SQL should still be reported")
	}
}

func TestQueryCapsRows(t *testing.T) {
	e := newEngine(t, &scriptedLLM{sql: "SELECT * FROM range(1000)"})
	if _, err := e.ImportFile(context.Background(), writeFile(t, "x.csv", "a\n1\n")); err != nil {
		t.Fatal(err)
	}
	res, err := e.QueryNL(context.Background(), "q", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != MaxResultRows || !res.Truncated {
		t.Fatalf("rows=%d truncated=%v", len(res.Rows), res.Truncated)
	}
}

func TestImportRejectsUnsupportedType(t *testing.T) {
	e := newEngine(t, &scriptedLLM{})
	if _, err := e.ImportFile(context.Background(), writeFile(t, "x.pdf", "%PDF")); err == nil {
		t.Fatal("expected an error")
	}
}
