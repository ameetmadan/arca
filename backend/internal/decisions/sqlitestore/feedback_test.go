//go:build cgo

package sqlitestore

import (
	"context"
	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/pkg/decision"
	"path/filepath"
	"testing"
	"time"
)

func TestFeedbackCorrectionAndThreshold(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r, err := s.Insert(ctx, decisions.Record{TS: time.Now(), Root: "/root", File: string(rune('a'+i)) + ".txt", Action: decision.Move, Target: "old", Status: decisions.StatusPending, Vector: []float32{0, 1}})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Transition(ctx, r.ID, decisions.StatusPending, decisions.StatusAccepted, decisions.Update{}); err != nil {
			t.Fatal(err)
		}
		target := "new"
		journal := int64(i + 1)
		if err = s.Transition(ctx, r.ID, decisions.StatusAccepted, decisions.StatusAccepted, decisions.Update{Target: &target, JournalID: &journal}); err != nil {
			t.Fatal(err)
		}
		suggestions, err := s.Suggestions(ctx, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(suggestions) != map[bool]int{true: 1, false: 0}[i == 2] {
			t.Fatalf("suggestions at %d: %v", i+1, suggestions)
		}
	}
	stats, err := s.AcceptanceStats(ctx, "/root")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Decided != 3 || stats.Accepted != 0 {
		t.Fatalf("corrections must count as misses: %+v", stats)
	}
	samples, err := s.LearnedExamples(ctx)
	if err != nil || len(samples) != 3 {
		t.Fatalf("samples: %v %v", samples, err)
	}
}

func TestFeedbackSlidingWindowAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 101; i++ {
		r, err := s.Insert(ctx, decisions.Record{TS: time.Now(), Root: "/root", File: "a.txt", Action: decision.Move, Target: "A", Status: decisions.StatusPending})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			journal := int64(1)
			err = s.Transition(ctx, r.ID, decisions.StatusPending, decisions.StatusAccepted, decisions.Update{JournalID: &journal})
		} else {
			err = s.Transition(ctx, r.ID, decisions.StatusPending, decisions.StatusRejected, decisions.Update{})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.AcceptanceStats(ctx, "/root")
	if err != nil {
		t.Fatal(err)
	}
	if st.Decided != 100 || st.Accepted != 0 {
		t.Fatalf("window after reopen: %+v", st)
	}
}

func TestFeedbackFailureRollsBackResolution(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	r, err := s.Insert(ctx, decisions.Record{TS: time.Now(), Root: "/r", File: "a.txt", Action: decision.Move, Target: "A", Status: decisions.StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a storage failure at the transaction's second write.
	if _, err = s.db.Exec(`CREATE TRIGGER fail_feedback BEFORE INSERT ON feedback BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.Transition(ctx, r.ID, decisions.StatusPending, decisions.StatusRejected, decisions.Update{}); err == nil {
		t.Fatal("expected storage failure")
	}
	got, err := s.Get(ctx, r.ID)
	if err != nil || got.Status != decisions.StatusPending {
		t.Fatalf("partial resolution: %+v %v", got, err)
	}
}
