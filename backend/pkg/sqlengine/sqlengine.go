package sqlengine

import (
	"context"
	"errors"
)

type QueryResult struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	// SQL is the statement that produced the rows, shown to the user so an
	// answer can be checked.
	SQL string `json:"sql"`
	// Truncated is set when more rows matched than were returned.
	Truncated bool `json:"truncated"`
}

type SQLEngine interface {
	QueryNL(ctx context.Context, question, schemaHint string) (QueryResult, error)
}

// Column describes one column of an imported table.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Table describes a table the user imported.
type Table struct {
	Name    string   `json:"name"`
	Source  string   `json:"source"` // the file it was imported from
	Rows    int64    `json:"rows"`
	Columns []Column `json:"columns"`
}

// TableManager is implemented by engines that can load files into tables.
// It is optional so that an engine over an existing database need not
// support importing.
type TableManager interface {
	// CanImport reports whether ImportFile accepts this kind of file.
	CanImport(path string) bool
	// ImportFile loads a spreadsheet or data file into one table per sheet,
	// replacing tables previously imported under the same names.
	ImportFile(ctx context.Context, path string) ([]Table, error)
	Tables(ctx context.Context) ([]Table, error)
	DropTable(ctx context.Context, name string) error
}

// ErrNoTables is returned by QueryNL when there is nothing to query yet.
var ErrNoTables = errors.New("no tables yet: import a spreadsheet first")

// ErrUnknownTable is returned by DropTable for a name that is not an
// imported table.
var ErrUnknownTable = errors.New("no such table")
