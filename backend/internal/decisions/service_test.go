package decisions_test

import (
	"context"
	"errors"
	"io/fs"
	"syscall"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/decisions/decisionstest"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

type call struct {
	root, file string
	d          decision.Decision
}

// fakeExec records calls and returns canned results.
type fakeExec struct {
	calls     []call
	err       error
	undoErr   error
	undone    []int64
	nextEntry int64
}

func (f *fakeExec) Execute(_ context.Context, root string, d decision.Decision, file string) (executor.JournalEntry, error) {
	f.calls = append(f.calls, call{root, file, d})
	if f.err != nil {
		return executor.JournalEntry{}, f.err
	}
	f.nextEntry++
	return executor.JournalEntry{ID: 100 + f.nextEntry}, nil
}

func (f *fakeExec) Undo(_ context.Context, id int64) error {
	f.undone = append(f.undone, id)
	return f.undoErr
}

type env struct {
	svc   *decisions.Service
	store *decisionstest.MemStore
	exec  *fakeExec
	now   time.Time
}

const root = "/watched"

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{store: decisionstest.NewMemStore(), exec: &fakeExec{}, now: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)}
	e.svc = decisions.NewService(decisions.Deps{
		Queue: e.store, Policies: e.store, Stats: e.store, Exec: e.exec,
		Now: func() time.Time { return e.now },
	})
	return e
}

func move(conf float64) decision.Decision {
	return decision.Decision{Action: decision.Move, Target: "Invoices", Confidence: conf, Reason: "looks like an invoice", Source: "semantic"}
}

func (e *env) setPolicy(t *testing.T, mode policy.Mode, th float64) {
	t.Helper()
	if err := e.store.PutPolicy(context.Background(), root, policy.Settings{Mode: mode, AutoThreshold: th}); err != nil {
		t.Fatal(err)
	}
}

// earnAuto records enough accepted decisions for the folder to be eligible.
func (e *env) earnAuto(t *testing.T, accepted, rejected int) {
	t.Helper()
	for i := 0; i < accepted+rejected; i++ {
		st := decisions.StatusAccepted
		if i >= accepted {
			st = decisions.StatusRejected
		}
		if _, err := e.store.Insert(context.Background(), decisions.Record{Root: root, File: "x", Action: decision.Move, Status: st}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubmit_DefaultModeQueues(t *testing.T) {
	e := newEnv(t)
	res, err := e.svc.Submit(context.Background(), root, "a.pdf", move(1))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != policy.Queue || res.Record == nil {
		t.Fatalf("result = %+v", res)
	}
	r := res.Record
	if r.ID == 0 || r.Status != decisions.StatusPending || r.Root != root || r.File != "a.pdf" || r.Target != "Invoices" ||
		r.Source != "semantic" || r.Confidence != 1 || !r.TS.Equal(e.now) || r.JournalID != nil {
		t.Fatalf("record = %+v", r)
	}
	if len(e.exec.calls) != 0 {
		t.Fatal("suggest mode must not execute")
	}
}

func TestSubmit_OffDrops(t *testing.T) {
	e := newEnv(t)
	e.setPolicy(t, policy.ModeOff, 0.9)
	res, err := e.svc.Submit(context.Background(), root, "a.pdf", move(1))
	if err != nil || res.Outcome != policy.Drop || res.Record != nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if p, _ := e.store.List(context.Background(), decisions.ListFilter{Limit: 10}); p.Total != 0 {
		t.Fatal("dropped decision must not be stored")
	}
}

func TestSubmit_AutoAppliesThroughExecutorWhenEligible(t *testing.T) {
	e := newEnv(t)
	e.setPolicy(t, policy.ModeAuto, 0.9)
	e.earnAuto(t, 19, 1) // 95% over 20

	res, err := e.svc.Submit(context.Background(), root, "a.pdf", move(0.95))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != policy.ApplyNow || res.Record == nil || res.Record.Status != decisions.StatusApplied {
		t.Fatalf("result = %+v", res)
	}
	if res.Record.JournalID == nil || *res.Record.JournalID != 101 {
		t.Fatalf("journal id = %v", res.Record.JournalID)
	}
	if len(e.exec.calls) != 1 || e.exec.calls[0].root != root || e.exec.calls[0].file != "a.pdf" || e.exec.calls[0].d.Target != "Invoices" {
		t.Fatalf("exec calls = %+v", e.exec.calls)
	}
}

func TestSubmit_AutoQueuesWhenNotEligibleOrNotConfident(t *testing.T) {
	e := newEnv(t)
	e.setPolicy(t, policy.ModeAuto, 0.9)

	// no history: not eligible
	res, err := e.svc.Submit(context.Background(), root, "a.pdf", move(1))
	if err != nil || res.Outcome != policy.Queue {
		t.Fatalf("no history: res=%+v err=%v", res, err)
	}
	e.earnAuto(t, 20, 0)
	// eligible but below threshold
	res, err = e.svc.Submit(context.Background(), root, "b.pdf", move(0.5))
	if err != nil || res.Outcome != policy.Queue {
		t.Fatalf("low confidence: res=%+v err=%v", res, err)
	}
	if len(e.exec.calls) != 0 {
		t.Fatal("nothing should have been executed")
	}
}

func TestSubmit_AutoApplyFailureIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.setPolicy(t, policy.ModeAuto, 0.9)
	e.earnAuto(t, 20, 0)
	e.exec.err = fs.ErrNotExist

	res, err := e.svc.Submit(context.Background(), root, "gone.pdf", move(1))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	if res.Record == nil || res.Record.Status != decisions.StatusFailed || res.Record.JournalID != nil {
		t.Fatalf("record = %+v", res.Record)
	}
}

func TestSubmit_AskUserQueuesEvenInAuto(t *testing.T) {
	e := newEnv(t)
	e.setPolicy(t, policy.ModeAuto, 0.5)
	e.earnAuto(t, 20, 0)
	res, err := e.svc.Submit(context.Background(), root, "a.pdf", decision.Decision{Action: decision.AskUser, Confidence: 1, Source: "chain"})
	if err != nil || res.Outcome != policy.Queue || res.Record.Action != decision.AskUser {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestSubmit_ValidatesInput(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct{ root, file string }{
		"empty root":        {"", "a.pdf"},
		"relative root":     {"rel/dir", "a.pdf"},
		"unclean root":      {"/a/../b", "a.pdf"},
		"empty file":        {root, ""},
		"absolute file":     {root, "/etc/passwd"},
		"NUL in file":       {root, "a\x00b"},
		"file too long":     {root, string(make([]byte, 5000))},
		"NUL in root":       {"/a\x00b", "a.pdf"},
		"whitespace file":   {root, "   "},
		"trailing sep root": {"/a/b/", "a.pdf"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.Submit(context.Background(), tc.root, tc.file, move(1))
			if !errors.Is(err, decisions.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	bad := move(1)
	bad.Confidence = 1.5
	if _, err := e.svc.Submit(context.Background(), root, "a.pdf", bad); !errors.Is(err, decisions.ErrInvalid) {
		t.Fatalf("confidence 1.5: err = %v", err)
	}
	bad = move(1)
	bad.Target = ""
	if _, err := e.svc.Submit(context.Background(), root, "a.pdf", bad); !errors.Is(err, decisions.ErrInvalid) {
		t.Fatalf("empty move target: err = %v", err)
	}
}

func queued(t *testing.T, e *env, d decision.Decision) decisions.Record {
	t.Helper()
	res, err := e.svc.Submit(context.Background(), root, "a.pdf", d)
	if err != nil || res.Record == nil {
		t.Fatalf("Submit: %+v %v", res, err)
	}
	return *res.Record
}

func TestAccept_RunsExecutorAndRecordsJournal(t *testing.T) {
	e := newEnv(t)
	r := queued(t, e, move(0.7))

	got, err := e.svc.Accept(context.Background(), r.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != decisions.StatusAccepted || got.JournalID == nil || *got.JournalID != 101 || got.Target != "Invoices" {
		t.Fatalf("record = %+v", got)
	}
	c := e.exec.calls[0]
	if c.root != root || c.file != "a.pdf" || c.d.Action != decision.Move || c.d.Target != "Invoices" || c.d.Source != "semantic" {
		t.Fatalf("exec call = %+v", c)
	}
}

func TestAccept_TargetOverride(t *testing.T) {
	e := newEnv(t)
	r := queued(t, e, move(0.7))

	got, err := e.svc.Accept(context.Background(), r.ID, "Taxes/2026")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "Taxes/2026" {
		t.Fatalf("stored target = %q, want the override", got.Target)
	}
	c := e.exec.calls[0]
	if c.d.Target != "Taxes/2026" || c.d.Source != "user" {
		t.Fatalf("exec call = %+v", c)
	}
}

func TestAccept_AskUserNeedsTarget(t *testing.T) {
	e := newEnv(t)
	r := queued(t, e, decision.Decision{Action: decision.AskUser, Source: "chain", Reason: "no engine was confident enough"})

	if _, err := e.svc.Accept(context.Background(), r.ID, ""); !errors.Is(err, decisions.ErrTargetRequired) {
		t.Fatalf("err = %v, want ErrTargetRequired", err)
	}
	if got, _ := e.store.Get(context.Background(), r.ID); got.Status != decisions.StatusPending {
		t.Fatalf("status = %s, a rejected request must leave it pending", got.Status)
	}
	got, err := e.svc.Accept(context.Background(), r.ID, "Inbox")
	if err != nil || got.Status != decisions.StatusAccepted {
		t.Fatalf("got %+v err %v", got, err)
	}
	if e.exec.calls[0].d.Action != decision.Move {
		t.Fatal("accepting ask_user with a target must be a move")
	}
}

func TestAccept_InvalidOverride(t *testing.T) {
	e := newEnv(t)
	r := queued(t, e, move(0.7))
	for name, target := range map[string]string{
		"absolute": "/etc",
		"NUL":      "a\x00b",
		"long":     string(make([]byte, 2000)),
		"blank":    "   ",
	} {
		if _, err := e.svc.Accept(context.Background(), r.ID, target); !errors.Is(err, decisions.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if len(e.exec.calls) != 0 {
		t.Fatal("invalid overrides must not reach the executor")
	}
}

func TestAccept_UnknownAndAlreadyResolved(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Accept(context.Background(), 99, ""); !errors.Is(err, decisions.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	r := queued(t, e, move(0.7))
	if _, err := e.svc.Accept(context.Background(), r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Accept(context.Background(), r.ID, ""); !errors.Is(err, decisions.ErrNotPending) {
		t.Fatalf("second accept: err = %v, want ErrNotPending", err)
	}
	if _, err := e.svc.Reject(context.Background(), r.ID); !errors.Is(err, decisions.ErrNotPending) {
		t.Fatalf("reject after accept: err = %v, want ErrNotPending", err)
	}
	if len(e.exec.calls) != 1 {
		t.Fatalf("executor ran %d times, want 1", len(e.exec.calls))
	}
}

func TestAccept_StaleFileMarksFailed(t *testing.T) {
	for name, execErr := range map[string]error{
		"file vanished":    &fs.PathError{Op: "lstat", Path: "/watched/a.pdf", Err: fs.ErrNotExist},
		"already in place": executor.ErrAlreadyInPlace,
		"root vanished":    executor.ErrInvalidRoot,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			r := queued(t, e, move(0.7))
			e.exec.err = execErr

			_, err := e.svc.Accept(context.Background(), r.ID, "")
			if !errors.Is(err, execErr) {
				t.Fatalf("err = %v, want it to wrap %v", err, execErr)
			}
			got, _ := e.store.Get(context.Background(), r.ID)
			if got.Status != decisions.StatusFailed || got.JournalID != nil {
				t.Fatalf("record = %+v", got)
			}
		})
	}
}

func TestAccept_RecoverableFailureLeavesPending(t *testing.T) {
	for name, execErr := range map[string]error{
		"outside root":   executor.ErrOutsideRoot,
		"target is file": syscall.ENOTDIR,
		"other":          errors.New("disk on fire"),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			r := queued(t, e, move(0.7))
			e.exec.err = execErr

			if _, err := e.svc.Accept(context.Background(), r.ID, ""); !errors.Is(err, execErr) {
				t.Fatalf("err = %v", err)
			}
			if got, _ := e.store.Get(context.Background(), r.ID); got.Status != decisions.StatusPending {
				t.Fatalf("status = %s, want pending so the user can retry with another target", got.Status)
			}
			// and it really can be retried
			e.exec.err = nil
			if got, err := e.svc.Accept(context.Background(), r.ID, "Elsewhere"); err != nil || got.Status != decisions.StatusAccepted {
				t.Fatalf("retry: %+v %v", got, err)
			}
		})
	}
}

func TestAccept_NoExecutor(t *testing.T) {
	st := decisionstest.NewMemStore()
	svc := decisions.NewService(decisions.Deps{Queue: st, Policies: st, Stats: st})
	res, err := svc.Submit(context.Background(), root, "a.pdf", move(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Accept(context.Background(), res.Record.ID, ""); !errors.Is(err, decisions.ErrExecutorUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := st.Get(context.Background(), res.Record.ID); got.Status != decisions.StatusPending {
		t.Fatal("must stay pending")
	}
	if err := svc.Undo(context.Background(), 1); !errors.Is(err, decisions.ErrExecutorUnavailable) {
		t.Fatalf("undo err = %v", err)
	}
}

func TestReject(t *testing.T) {
	e := newEnv(t)
	r := queued(t, e, move(0.7))
	got, err := e.svc.Reject(context.Background(), r.ID)
	if err != nil || got.Status != decisions.StatusRejected {
		t.Fatalf("got %+v err %v", got, err)
	}
	if len(e.exec.calls) != 0 {
		t.Fatal("reject must not touch files")
	}
	if _, err := e.svc.Reject(context.Background(), 99); !errors.Is(err, decisions.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestList_Validation(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		queued(t, e, move(0.5))
	}
	p, err := e.svc.List(context.Background(), decisions.ListFilter{Status: decisions.StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	if p.Limit != decisions.DefaultLimit || p.Total != 3 || len(p.Items) != 3 {
		t.Fatalf("defaults not applied: %+v", p)
	}
	for name, f := range map[string]decisions.ListFilter{
		"bad status":     {Status: "done"},
		"negative limit": {Limit: -1},
		"huge limit":     {Limit: decisions.MaxLimit + 1},
		"neg offset":     {Offset: -5},
	} {
		if _, err := e.svc.List(context.Background(), f); !errors.Is(err, decisions.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := e.svc.List(context.Background(), decisions.ListFilter{Limit: decisions.MaxLimit}); err != nil {
		t.Errorf("limit == max must be allowed: %v", err)
	}
}

func TestUndo(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.Undo(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if len(e.exec.undone) != 1 || e.exec.undone[0] != 5 {
		t.Fatalf("undone = %v", e.exec.undone)
	}
	e.exec.undoErr = executor.ErrJournalEntryNotFound
	if err := e.svc.Undo(context.Background(), 6); !errors.Is(err, decisions.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	e.exec.undoErr = executor.ErrAlreadyUndone
	if err := e.svc.Undo(context.Background(), 7); !errors.Is(err, executor.ErrAlreadyUndone) {
		t.Fatalf("err = %v", err)
	}
	if err := e.svc.Undo(context.Background(), 0); !errors.Is(err, decisions.ErrInvalid) {
		t.Fatalf("id 0: err = %v", err)
	}
}

func TestPolicy_DefaultAndRoundTrip(t *testing.T) {
	e := newEnv(t)
	v, err := e.svc.GetPolicy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if v.Settings != policy.DefaultSettings() || v.Eligible || v.Stats != (policy.Stats{}) || v.Root != root {
		t.Fatalf("default view = %+v", v)
	}
	v, err = e.svc.PutPolicy(context.Background(), root, policy.Settings{Mode: policy.ModeOff, AutoThreshold: 0.8})
	if err != nil || v.Settings.Mode != policy.ModeOff || v.Settings.AutoThreshold != 0.8 {
		t.Fatalf("put: %+v %v", v, err)
	}
	if v, _ := e.svc.GetPolicy(context.Background(), root); v.Settings.Mode != policy.ModeOff {
		t.Fatalf("not persisted: %+v", v)
	}
}

func TestPolicy_AutoRequiresEligibility(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.PutPolicy(context.Background(), root, policy.Settings{Mode: policy.ModeAuto, AutoThreshold: 0.9})
	if !errors.Is(err, decisions.ErrNotEligible) {
		t.Fatalf("err = %v, want ErrNotEligible", err)
	}
	if v, _ := e.svc.GetPolicy(context.Background(), root); v.Settings.Mode == policy.ModeAuto {
		t.Fatal("auto must not have been stored")
	}

	e.earnAuto(t, 18, 2)
	v, err := e.svc.PutPolicy(context.Background(), root, policy.Settings{Mode: policy.ModeAuto, AutoThreshold: 0.9})
	if err != nil || !v.Eligible || v.Settings.Mode != policy.ModeAuto || v.Stats.Decided != 20 {
		t.Fatalf("v=%+v err=%v", v, err)
	}
}

func TestPolicy_Validation(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.PutPolicy(context.Background(), root, policy.Settings{Mode: "turbo", AutoThreshold: 0.9}); !errors.Is(err, policy.ErrInvalidSettings) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.svc.PutPolicy(context.Background(), "relative", policy.DefaultSettings()); !errors.Is(err, decisions.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.svc.GetPolicy(context.Background(), "relative"); !errors.Is(err, decisions.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestCountPending(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, f := range []string{"a.pdf", "b.pdf", "b.pdf"} {
		if res, err := e.svc.Submit(ctx, root, f, move(0.5)); err != nil || res.Record == nil {
			t.Fatalf("Submit: %+v %v", res, err)
		}
	}
	if _, err := e.svc.Reject(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.CountPending(ctx, root, ""); err != nil || n != 2 {
		t.Fatalf("pending in root = %d, %v; want 2", n, err)
	}
	if n, _ := e.svc.CountPending(ctx, root, "b.pdf"); n != 2 {
		t.Fatalf("pending b.pdf = %d, want 2", n)
	}
	if n, _ := e.svc.CountPending(ctx, root, "a.pdf"); n != 0 {
		t.Fatalf("pending a.pdf = %d, want 0 (rejected)", n)
	}
	if n, _ := e.svc.CountPending(ctx, "/elsewhere", ""); n != 0 {
		t.Fatalf("pending elsewhere = %d, want 0", n)
	}
	if _, err := e.svc.CountPending(ctx, "relative", ""); !errors.Is(err, decisions.ErrInvalid) {
		t.Fatalf("relative root: err = %v, want ErrInvalid", err)
	}
}

func TestCalibrationConfirmationDoesNotMoveFile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	result, err := e.svc.SubmitWithFacts(ctx, root, "Notes/a.txt", decision.Decision{Action: decision.AskUser, Target: "Notes", Source: "calibration"}, decision.Facts{Vector: []float32{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := e.svc.Accept(ctx, result.Record.ID, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.exec.calls) != 0 || rec.Status != decisions.StatusAccepted || rec.JournalID != nil {
		t.Fatalf("confirmation moved file: %+v", rec)
	}
}
