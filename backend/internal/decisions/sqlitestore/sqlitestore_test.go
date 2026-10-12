//go:build cgo

package sqlitestore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/decisions/decisionstest"
	"github.com/ameetmadan/arca/pkg/decision"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "decisions.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestContract(t *testing.T) {
	decisionstest.RunContract(t, func(t *testing.T) decisionstest.Backend {
		s, _ := open(t)
		return s
	})
}

func TestPersistsAcrossReopen(t *testing.T) {
	s, path := open(t)
	ctx := context.Background()
	jid := int64(3)
	r, err := s.Insert(ctx, decisions.Record{
		TS: time.Now(), Root: "/r", File: "a.txt", Action: decision.Move, Target: "Docs",
		Confidence: 0.8, Status: decisions.StatusApplied, JournalID: &jid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get(ctx, r.ID)
	if err != nil || got.JournalID == nil || *got.JournalID != 3 || got.Status != decisions.StatusApplied {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestRejectsUnknownStatus(t *testing.T) {
	s, _ := open(t)
	_, err := s.Insert(context.Background(), decisions.Record{
		TS: time.Now(), Root: "/r", File: "a", Action: decision.Move, Status: "bogus",
	})
	if err == nil {
		t.Fatal("the CHECK constraint should reject an unknown status")
	}
}
