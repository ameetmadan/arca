package sqlengine

import "context"

type QueryResult struct {
	Columns []string
	Rows    [][]interface{}
}

type SQLEngine interface {
	QueryNL(ctx context.Context, question, schemaHint string) (QueryResult, error)
}
