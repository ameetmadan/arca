//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/pkg/decision"
)

// With the real SQLite store, a queued decision survives a restart and can
// then be accepted through the Executor.
func TestNewDecisionService_PersistsQueueAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	_, root := applyDirs(t)
	writeFile(t, filepath.Join(root, "a.pdf"), "A")
	ctx := context.Background()

	svc, closeDB := newDecisionService(dir, executor.New(&applyJournal{}))
	if svc == nil {
		t.Fatal("service is nil, expected SQLite to open")
	}
	res, err := svc.Submit(ctx, root, "a.pdf", decision.Decision{Action: decision.Move, Target: "Invoices", Confidence: 0.7, Source: "semantic"})
	if err != nil || res.Record == nil {
		t.Fatalf("Submit: %+v %v", res, err)
	}
	closeDB()

	svc, closeDB = newDecisionService(dir, executor.New(&applyJournal{}))
	defer closeDB()
	page, err := svc.List(ctx, decisions.ListFilter{Status: decisions.StatusPending})
	if err != nil || page.Total != 1 {
		t.Fatalf("after restart: %+v %v", page, err)
	}
	got, err := svc.Accept(ctx, res.Record.ID, "")
	if err != nil || got.Status != decisions.StatusAccepted {
		t.Fatalf("Accept: %+v %v", got, err)
	}
	if fileContent(t, filepath.Join(root, "Invoices", "a.pdf")) != "A" {
		t.Fatal("file not moved")
	}
}

func TestNewDecisionService_NilExecutorKeepsQueueUsable(t *testing.T) {
	svc, closeDB := newDecisionService(t.TempDir(), nil)
	defer closeDB()
	if svc == nil {
		t.Fatal("service is nil")
	}
	if _, err := svc.Accept(context.Background(), 1, ""); err != decisions.ErrExecutorUnavailable {
		t.Fatalf("err = %v, want ErrExecutorUnavailable (a typed-nil executor would panic instead)", err)
	}
}
