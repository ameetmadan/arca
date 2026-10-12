package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/decisions/decisionstest"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

// memJournal is an in-memory executor.Journal so these tests need no CGo.
type memJournal struct {
	mu      sync.Mutex
	entries []executor.JournalEntry
}

func (j *memJournal) Append(_ context.Context, e executor.JournalEntry) (executor.JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e.ID = int64(len(j.entries) + 1)
	j.entries = append(j.entries, e)
	return e, nil
}

func (j *memJournal) Get(_ context.Context, id int64) (executor.JournalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if id < 1 || int(id) > len(j.entries) {
		return executor.JournalEntry{}, executor.ErrJournalEntryNotFound
	}
	return j.entries[id-1], nil
}

func (j *memJournal) MarkUndone(_ context.Context, id int64, at time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries[id-1].UndoneAt = &at
	return nil
}

// fakeEngine answers from fn and counts how often it was asked.
type fakeEngine struct {
	mu    sync.Mutex
	calls []string // Facts.RelPath of each call
	fn    func(decision.Facts) (decision.Decision, error)
}

func (e *fakeEngine) Decide(_ context.Context, f decision.Facts) (decision.Decision, error) {
	e.mu.Lock()
	e.calls = append(e.calls, f.RelPath)
	e.mu.Unlock()
	return e.fn(f)
}

func (e *fakeEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func moveTo(target string, conf float64) func(decision.Facts) (decision.Decision, error) {
	return func(decision.Facts) (decision.Decision, error) {
		return decision.Decision{Action: decision.Move, Target: target, Confidence: conf, Reason: "test", Source: "fake"}, nil
	}
}

type dEnv struct {
	root   string
	store  *decisionstest.MemStore
	svc    *decisions.Service
	recent *RecentMoves
	engine *fakeEngine
	d      *Decider

	mu      sync.Mutex
	indexed []string
	indexFn func(Event) error
}

func newDEnv(t *testing.T) *dEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &dEnv{
		root:   root,
		store:  decisionstest.NewMemStore(),
		recent: NewRecentMoves(time.Minute),
		engine: &fakeEngine{fn: moveTo("Docs", 0.8)},
	}
	exec := e.recent.Wrap(executor.New(&memJournal{}))
	e.svc = decisions.NewService(decisions.Deps{Queue: e.store, Policies: e.store, Stats: e.store, Exec: exec})
	e.d = &Decider{
		Facts:       &FactsBuilder{Roots: []string{root}},
		Engine:      e.engine,
		Queue:       e.svc,
		Roots:       []string{root},
		Recent:      e.recent,
		SettleDelay: time.Millisecond,
		Index: func(_ context.Context, ev Event) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.indexed = append(e.indexed, filepath.Base(ev.Path))
			if e.indexFn != nil {
				return e.indexFn(ev)
			}
			return nil
		},
	}
	return e
}

func (e *dEnv) write(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(e.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *dEnv) pending(t *testing.T) []decisions.Record {
	t.Helper()
	p, err := e.svc.List(context.Background(), decisions.ListFilter{Status: decisions.StatusPending, Limit: decisions.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	return p.Items
}

func (e *dEnv) setMode(t *testing.T, m policy.Mode) {
	t.Helper()
	if err := e.store.PutPolicy(context.Background(), e.root, policy.Settings{Mode: m, AutoThreshold: 0.9}); err != nil {
		t.Fatal(err)
	}
}

// earnAuto records enough accepted decisions for the folder to be eligible.
func (e *dEnv) earnAuto(t *testing.T) {
	t.Helper()
	for i := 0; i < policy.MinDecisions; i++ {
		if _, err := e.store.Insert(context.Background(), decisions.Record{Root: e.root, File: "x", Action: decision.Move, Status: decisions.StatusAccepted}); err != nil {
			t.Fatal(err)
		}
	}
	e.setMode(t, policy.ModeAuto)
}

func created(path string) Event { return Event{Op: Created, Path: path} }

func TestDecider_SuggestModeQueuesDecisionWithRelativeTarget(t *testing.T) {
	e := newDEnv(t)
	path := e.write(t, "invoice.pdf", "total due")

	if err := e.d.Handle(context.Background(), created(path)); err != nil {
		t.Fatal(err)
	}

	got := e.pending(t)
	if len(got) != 1 {
		t.Fatalf("pending = %+v, want 1", got)
	}
	r := got[0]
	if r.Root != e.root || r.File != "invoice.pdf" || r.Action != decision.Move || r.Target != "Docs" ||
		r.Source != "fake" || r.Status != decisions.StatusPending {
		t.Fatalf("record = %+v", r)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("suggest mode must not move the file")
	}
	if len(e.indexed) != 1 {
		t.Fatalf("indexed = %v, want the file indexed first", e.indexed)
	}
}

func TestDecider_AutoModeAppliesMoveThroughExecutor(t *testing.T) {
	e := newDEnv(t)
	e.earnAuto(t)
	e.engine.fn = moveTo("Docs", 0.95)
	path := e.write(t, "invoice.pdf", "total due")

	if err := e.d.Handle(context.Background(), created(path)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(e.root, "Docs", "invoice.pdf")); err != nil {
		t.Fatalf("file was not moved: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source should be gone")
	}
	if n := len(e.pending(t)); n != 0 {
		t.Fatalf("auto-applied decisions must not stay pending, got %d", n)
	}
}

func TestDecider_MovedFileDoesNotTriggerNewDecision(t *testing.T) {
	e := newDEnv(t)
	e.earnAuto(t)
	e.engine.fn = moveTo("Docs", 0.95)
	path := e.write(t, "invoice.pdf", "total due")
	ctx := context.Background()

	if err := e.d.Handle(ctx, created(path)); err != nil {
		t.Fatal(err)
	}
	if e.engine.callCount() != 1 {
		t.Fatalf("engine calls = %d, want 1", e.engine.callCount())
	}

	// The watcher now reports what the move did: a removal at the source and
	// a creation at the destination (e.g. if the target folder is watched).
	dst := filepath.Join(e.root, "Docs", "invoice.pdf")
	if err := e.d.Handle(ctx, Event{Op: Removed, Path: path}); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Handle(ctx, created(dst)); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Handle(ctx, Event{Op: Modified, Path: dst}); err != nil {
		t.Fatal(err)
	}
	if n := e.engine.callCount(); n != 1 {
		t.Fatalf("engine calls = %d after the move's own events, want still 1 (feedback loop)", n)
	}
	if n := len(e.pending(t)); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestDecider_UndoneFileDoesNotTriggerNewDecision(t *testing.T) {
	e := newDEnv(t)
	e.earnAuto(t)
	e.engine.fn = moveTo("Docs", 0.95)
	path := e.write(t, "invoice.pdf", "total due")
	ctx := context.Background()

	if err := e.d.Handle(ctx, created(path)); err != nil {
		t.Fatal(err)
	}
	// Find the journal id the move got, then undo it as the user would.
	recs, err := e.svc.List(ctx, decisions.ListFilter{Status: decisions.StatusApplied})
	if err != nil || len(recs.Items) != 1 || recs.Items[0].JournalID == nil {
		t.Fatalf("applied = %+v, %v", recs, err)
	}
	// Let the first window pass so only the undo can protect the file.
	clock := time.Now().Add(time.Hour)
	e.recent.now = func() time.Time { return clock }
	if e.recent.Contains(path) {
		t.Fatal("window should have expired")
	}
	if err := e.svc.Undo(ctx, *recs.Items[0].JournalID); err != nil {
		t.Fatal(err)
	}

	if err := e.d.Handle(ctx, created(path)); err != nil {
		t.Fatal(err)
	}
	if n := e.engine.callCount(); n != 1 {
		t.Fatalf("engine calls = %d, want 1: an undone move must not be re-suggested at once", n)
	}
}

func TestDecider_FileAlreadyInTargetFolderIsSkipped(t *testing.T) {
	e := newDEnv(t)
	path := e.write(t, "Docs/old.pdf", "already filed")

	if err := e.d.Handle(context.Background(), created(path)); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pending(t)); n != 0 {
		t.Fatalf("pending = %d, want 0: moving a file to the folder it is in is a no-op", n)
	}
}

func TestDecider_EngineErrorDoesNotStopThePipeline(t *testing.T) {
	e := newDEnv(t)
	e.engine.fn = func(f decision.Facts) (decision.Decision, error) {
		if f.RelPath == "bad.txt" {
			return decision.Decision{}, errors.New("engine exploded")
		}
		return moveTo("Docs", 0.8)(f)
	}
	bad := e.write(t, "bad.txt", "x")
	good := e.write(t, "good.txt", "y")

	var mu sync.Mutex
	var errs []error
	p := New(e.d.Handle, Options{Workers: 1, OnError: func(_ Event, err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	p.Submit(created(bad))
	p.Submit(created(good))

	waitFor(t, func() bool { return len(e.pending(t)) == 1 })
	if got := e.pending(t)[0].File; got != "good.txt" {
		t.Fatalf("queued %q, want good.txt", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "engine exploded") {
		t.Fatalf("errs = %v, want the engine error reported once", errs)
	}
}

func TestDecider_FileStillBeingWrittenIsNotDecidedUntilItSettles(t *testing.T) {
	e := newDEnv(t)
	path := e.write(t, "download.bin", "part")
	growing := true
	e.d.Sleep = func(_ context.Context, _ time.Duration) error {
		if growing { // a writer appends while we wait
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Error(err)
				return nil
			}
			_, _ = f.WriteString("more")
			f.Close()
		}
		return nil
	}

	if err := e.d.Handle(context.Background(), created(path)); err != nil {
		t.Fatal(err)
	}
	if e.engine.callCount() != 0 || len(e.pending(t)) != 0 {
		t.Fatal("a file whose size is changing must not be decided on")
	}

	growing = false // the writer is done; the next event finds it stable
	if err := e.d.Handle(context.Background(), Event{Op: Modified, Path: path}); err != nil {
		t.Fatal(err)
	}
	if len(e.pending(t)) != 1 {
		t.Fatal("a settled file must be decided on")
	}
}

func TestDecider_SettleCheckHonoursCancellation(t *testing.T) {
	e := newDEnv(t)
	path := e.write(t, "a.txt", "x")
	e.d.SettleDelay = time.Hour
	e.d.Sleep = nil // use the real, cancellable sleep

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.d.Handle(ctx, created(path)) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle did not return after cancellation")
	}
}

func TestDecider_BackpressureStopsDecidingForAFullFolder(t *testing.T) {
	e := newDEnv(t)
	e.d.MaxPending = 2
	ctx := context.Background()

	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if err := e.d.Handle(ctx, created(e.write(t, name, name))); err != nil {
			t.Fatal(err)
		}
	}
	// "More than N": 0, 1, 2 pending still allow a new one, so 3 get queued.
	if n := len(e.pending(t)); n != 3 {
		t.Fatalf("pending = %d, want 3 (N=2 means stop once more than 2 are waiting)", n)
	}
	if n := e.engine.callCount(); n != 3 {
		t.Fatalf("engine calls = %d, want 3: a full folder must not even reach the engine", n)
	}

	// Working through the queue frees the folder again.
	if _, err := e.svc.Reject(ctx, e.pending(t)[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reject(ctx, e.pending(t)[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Handle(ctx, created(e.write(t, "d.txt", "d"))); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pending(t)); n != 2 {
		t.Fatalf("pending = %d, want 2 after rejecting two and deciding d.txt", n)
	}
}

func TestDecider_BackpressureIsPerFolder(t *testing.T) {
	e := newDEnv(t)
	e.d.MaxPending = 1
	other := t.TempDir()
	e.d.Roots = append(e.d.Roots, other)
	ctx := context.Background()

	for _, name := range []string{"a.txt", "b.txt"} {
		if err := e.d.Handle(ctx, created(e.write(t, name, name))); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(other, "z.txt")
	if err := os.WriteFile(p, []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.d.Facts = &FactsBuilder{Roots: e.d.Roots}
	if err := e.d.Handle(ctx, created(p)); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pending(t)); n != 3 {
		t.Fatalf("pending = %d, want 3: a full folder must not block another folder", n)
	}
}

func TestDecider_NoDuplicateWhilePending(t *testing.T) {
	e := newDEnv(t)
	path := e.write(t, "a.txt", "x")
	ctx := context.Background()
	for _, op := range []Op{Created, Modified, Modified} {
		if err := e.d.Handle(ctx, Event{Op: op, Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.pending(t)); n != 1 {
		t.Fatalf("pending = %d, want 1: repeated writes must not queue the same file again", n)
	}
	if n := e.engine.callCount(); n != 1 {
		t.Fatalf("engine calls = %d, want 1", n)
	}
}

func TestDecider_OffModeNeverReachesTheEngine(t *testing.T) {
	e := newDEnv(t)
	e.setMode(t, policy.ModeOff)
	if err := e.d.Handle(context.Background(), created(e.write(t, "a.txt", "x"))); err != nil {
		t.Fatal(err)
	}
	if e.engine.callCount() != 0 {
		t.Fatal("a folder in off mode must not spend an engine call")
	}
	if len(e.indexed) != 1 {
		t.Fatal("off mode still indexes")
	}
}

func TestDecider_IndexOnlyCases(t *testing.T) {
	e := newDEnv(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	for name, ev := range map[string]Event{
		"outside every watched folder": created(outside),
		"hidden file":                  created(e.write(t, ".DS_Store", "x")),
		"partial download":             created(e.write(t, "movie.crdownload", "x")),
		"directory":                    created(filepath.Join(e.root, "sub")),
		"removed":                      {Op: Removed, Path: filepath.Join(e.root, "gone.txt")},
		"vanished":                     created(filepath.Join(e.root, "vanished.txt")),
	} {
		if err := e.d.Handle(context.Background(), ev); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if e.engine.callCount() != 0 || len(e.pending(t)) != 0 {
		t.Fatalf("calls=%d pending=%d, want no decisions for index-only paths", e.engine.callCount(), len(e.pending(t)))
	}
}

func TestDecider_IndexFailureDoesNotPreventDecision(t *testing.T) {
	e := newDEnv(t)
	e.indexFn = func(Event) error { return errors.New("cannot parse image") }
	path := e.write(t, "photo.jpg", "x")

	err := e.d.Handle(context.Background(), created(path))
	if err == nil || !strings.Contains(err.Error(), "cannot parse image") {
		t.Fatalf("err = %v, want the index error reported", err)
	}
	if len(e.pending(t)) != 1 {
		t.Fatal("a file the parser cannot read should still get a decision")
	}
}

func TestDecider_DecisionFailureStillIndexes(t *testing.T) {
	e := newDEnv(t)
	e.engine.fn = func(decision.Facts) (decision.Decision, error) { return decision.Decision{}, errors.New("llm down") }
	err := e.d.Handle(context.Background(), created(e.write(t, "a.txt", "x")))
	if err == nil || !strings.Contains(err.Error(), "llm down") {
		t.Fatalf("err = %v", err)
	}
	if len(e.indexed) != 1 {
		t.Fatal("indexing must run even when deciding fails")
	}
}

func TestDecider_NonMoveDecisionsAreNotQueued(t *testing.T) {
	e := newDEnv(t)
	e.engine.fn = func(decision.Facts) (decision.Decision, error) {
		return decision.Decision{Action: decision.Ignore, Confidence: 1, Reason: "r", Source: "fake"}, nil
	}
	if err := e.d.Handle(context.Background(), created(e.write(t, "a.txt", "x"))); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pending(t)); n != 0 {
		t.Fatalf("pending = %d, want 0: policy drops actions the Executor cannot perform", n)
	}
}

func TestDecider_AskUserFallbackIsQueued(t *testing.T) {
	e := newDEnv(t)
	e.engine.fn = func(decision.Facts) (decision.Decision, error) {
		return decision.Decision{Action: decision.AskUser, Reason: "no engine was confident enough", Source: "chain"}, nil
	}
	if err := e.d.Handle(context.Background(), created(e.write(t, "a.txt", "x"))); err != nil {
		t.Fatal(err)
	}
	got := e.pending(t)
	if len(got) != 1 || got[0].Action != decision.AskUser {
		t.Fatalf("pending = %+v, want one ask_user", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
