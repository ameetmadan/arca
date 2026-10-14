//go:build !duckdb

// Stub — compile without -tags duckdb for API-layer testing.
package duckdb

import (
	"context"
	"errors"

	"github.com/ameetmadan/arca/pkg/llm"
	"github.com/ameetmadan/arca/pkg/sqlengine"
)

var ErrNotBuilt = errors.New("duckdb: binary not built with DuckDB CGo bindings (build tag: duckdb)")

type DuckDBEngine struct {
	DBPath string
	llm    llm.LLMProvider
}

func New(dbPath string, llmProvider llm.LLMProvider) (*DuckDBEngine, error) {
	return &DuckDBEngine{DBPath: dbPath, llm: llmProvider}, nil
}

func (e *DuckDBEngine) QueryNL(_ context.Context, _, _ string) (sqlengine.QueryResult, error) {
	return sqlengine.QueryResult{}, ErrNotBuilt
}

func (e *DuckDBEngine) ImportFile(_ context.Context, _ string) ([]sqlengine.Table, error) {
	return nil, ErrNotBuilt
}

func (e *DuckDBEngine) Tables(_ context.Context) ([]sqlengine.Table, error) {
	return nil, ErrNotBuilt
}

func (e *DuckDBEngine) DropTable(_ context.Context, _ string) error {
	return ErrNotBuilt
}
