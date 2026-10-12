package decisionstest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

// Backend is what a storage implementation must provide.
type Backend interface {
	decisions.Store
	decisions.PolicyStore
	policy.StatsProvider
}

// RunContract checks that the Backend returned by newBackend behaves as the
// decisions package expects. newBackend must return a fresh, empty backend.
func RunContract(t *testing.T, newBackend func(t *testing.T) Backend) {
	ctx := context.Background()
	ts := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	rec := func(root, file string, st decisions.Status) decisions.Record {
		return decisions.Record{
			TS: ts, Root: root, File: file, Action: decision.Move, Target: "Docs",
			Confidence: 0.75, Reason: "looks like an invoice", Source: "semantic", Status: st,
		}
	}

	t.Run("insert and get round trip", func(t *testing.T) {
		b := newBackend(t)
		in := rec("/r", "a.pdf", decisions.StatusPending)
		got, err := b.Insert(ctx, in)
		if err != nil || got.ID == 0 {
			t.Fatalf("Insert: %+v, %v", got, err)
		}
		out, err := b.Get(ctx, got.ID)
		if err != nil {
			t.Fatal(err)
		}
		in.ID = got.ID
		if !out.TS.Equal(in.TS) || out.Root != in.Root || out.File != in.File || out.Action != in.Action ||
			out.Target != in.Target || out.Confidence != in.Confidence || out.Reason != in.Reason ||
			out.Source != in.Source || out.Status != in.Status || out.JournalID != nil {
			t.Fatalf("round trip mismatch: %+v vs %+v", out, in)
		}
	})

	t.Run("get unknown id", func(t *testing.T) {
		b := newBackend(t)
		if _, err := b.Get(ctx, 42); !errors.Is(err, decisions.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("list is newest first, filtered and paginated", func(t *testing.T) {
		b := newBackend(t)
		for i := 0; i < 5; i++ {
			if _, err := b.Insert(ctx, rec("/r", string(rune('a'+i))+".txt", decisions.StatusPending)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := b.Insert(ctx, rec("/r", "done.txt", decisions.StatusRejected)); err != nil {
			t.Fatal(err)
		}

		p, err := b.List(ctx, decisions.ListFilter{Status: decisions.StatusPending, Limit: 2, Offset: 0})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 5 || len(p.Items) != 2 || p.Items[0].File != "e.txt" || p.Items[1].File != "d.txt" {
			t.Fatalf("page 1 = %+v", p)
		}
		p, _ = b.List(ctx, decisions.ListFilter{Status: decisions.StatusPending, Limit: 2, Offset: 4})
		if p.Total != 5 || len(p.Items) != 1 || p.Items[0].File != "a.txt" {
			t.Fatalf("last page = %+v", p)
		}
		p, _ = b.List(ctx, decisions.ListFilter{Status: decisions.StatusPending, Limit: 2, Offset: 50})
		if p.Total != 5 || p.Items == nil || len(p.Items) != 0 {
			t.Fatalf("past the end = %+v (items must be an empty, non-nil slice)", p)
		}
		p, _ = b.List(ctx, decisions.ListFilter{Limit: 100})
		if p.Total != 6 || len(p.Items) != 6 {
			t.Fatalf("all statuses = %+v", p)
		}
	})

	t.Run("list filters by root and file", func(t *testing.T) {
		b := newBackend(t)
		for _, r := range []decisions.Record{
			rec("/r", "a.pdf", decisions.StatusPending),
			rec("/r", "b.pdf", decisions.StatusPending),
			rec("/r", "a.pdf", decisions.StatusRejected),
			rec("/other", "a.pdf", decisions.StatusPending),
		} {
			if _, err := b.Insert(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		count := func(f decisions.ListFilter) int {
			t.Helper()
			f.Limit = 100
			p, err := b.List(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if p.Total != len(p.Items) {
				t.Fatalf("total %d != %d items", p.Total, len(p.Items))
			}
			return p.Total
		}
		if n := count(decisions.ListFilter{Root: "/r"}); n != 3 {
			t.Fatalf("root /r = %d, want 3", n)
		}
		if n := count(decisions.ListFilter{Root: "/r", Status: decisions.StatusPending}); n != 2 {
			t.Fatalf("pending in /r = %d, want 2", n)
		}
		if n := count(decisions.ListFilter{Root: "/r", File: "a.pdf", Status: decisions.StatusPending}); n != 1 {
			t.Fatalf("pending a.pdf in /r = %d, want 1", n)
		}
		if n := count(decisions.ListFilter{File: "a.pdf"}); n != 3 {
			t.Fatalf("a.pdf anywhere = %d, want 3", n)
		}
	})

	t.Run("transition is guarded by status", func(t *testing.T) {
		b := newBackend(t)
		r, _ := b.Insert(ctx, rec("/r", "a.txt", decisions.StatusPending))
		jid := int64(7)
		target := "Elsewhere"
		if err := b.Transition(ctx, r.ID, decisions.StatusPending, decisions.StatusAccepted, decisions.Update{}); err != nil {
			t.Fatal(err)
		}
		if err := b.Transition(ctx, r.ID, decisions.StatusPending, decisions.StatusRejected, decisions.Update{}); !errors.Is(err, decisions.ErrWrongStatus) {
			t.Fatalf("err = %v, want ErrWrongStatus", err)
		}
		if err := b.Transition(ctx, 999, decisions.StatusPending, decisions.StatusRejected, decisions.Update{}); !errors.Is(err, decisions.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		// from == to is allowed and applies the update.
		if err := b.Transition(ctx, r.ID, decisions.StatusAccepted, decisions.StatusAccepted,
			decisions.Update{JournalID: &jid, Target: &target}); err != nil {
			t.Fatal(err)
		}
		got, _ := b.Get(ctx, r.ID)
		if got.Status != decisions.StatusAccepted || got.JournalID == nil || *got.JournalID != 7 || got.Target != "Elsewhere" {
			t.Fatalf("after update: %+v", got)
		}
	})

	t.Run("policy store", func(t *testing.T) {
		b := newBackend(t)
		if _, ok, err := b.GetPolicy(ctx, "/r"); err != nil || ok {
			t.Fatalf("empty store: ok=%v err=%v", ok, err)
		}
		want := policy.Settings{Mode: policy.ModeAuto, AutoThreshold: 0.95}
		if err := b.PutPolicy(ctx, "/r", want); err != nil {
			t.Fatal(err)
		}
		if err := b.PutPolicy(ctx, "/other", policy.Settings{Mode: policy.ModeOff, AutoThreshold: 0.5}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := b.GetPolicy(ctx, "/r")
		if err != nil || !ok || got != want {
			t.Fatalf("got %+v ok=%v err=%v, want %+v", got, ok, err, want)
		}
		want2 := policy.Settings{Mode: policy.ModeSuggest, AutoThreshold: 0.8}
		if err := b.PutPolicy(ctx, "/r", want2); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := b.GetPolicy(ctx, "/r"); got != want2 {
			t.Fatalf("replace: got %+v", got)
		}
	})

	t.Run("acceptance stats count only user decisions in the folder", func(t *testing.T) {
		b := newBackend(t)
		add := func(root string, st decisions.Status, n int) {
			for i := 0; i < n; i++ {
				if _, err := b.Insert(ctx, rec(root, "f.txt", st)); err != nil {
					t.Fatal(err)
				}
			}
		}
		add("/r", decisions.StatusAccepted, 3)
		add("/r", decisions.StatusRejected, 1)
		add("/r", decisions.StatusApplied, 5) // automatic, not user feedback
		add("/r", decisions.StatusFailed, 2)
		add("/r", decisions.StatusPending, 4)
		add("/other", decisions.StatusAccepted, 9)

		st, err := b.AcceptanceStats(ctx, "/r")
		if err != nil {
			t.Fatal(err)
		}
		if st.Decided != 4 || st.Accepted != 3 {
			t.Fatalf("stats = %+v, want 3 of 4", st)
		}
		if st, _ := b.AcceptanceStats(ctx, "/nothing"); st != (policy.Stats{}) {
			t.Fatalf("empty stats = %+v", st)
		}
	})
}
