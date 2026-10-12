package decision

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

var askUser = Decision{Action: AskUser, Reason: fallbackReason, Source: fallbackSource}

// fakeEngine returns a canned result and counts how often it was called.
type fakeEngine struct {
	dec   Decision
	err   error
	calls int
}

func (f *fakeEngine) Decide(context.Context, Facts) (Decision, error) {
	f.calls++
	return f.dec, f.err
}

// funcEngine adapts a function to Engine for tests that need custom behaviour.
type funcEngine func(context.Context, Facts) (Decision, error)

func (f funcEngine) Decide(ctx context.Context, facts Facts) (Decision, error) {
	return f(ctx, facts)
}

func mv(target string, conf float64) Decision {
	return Decision{Action: Move, Target: target, Confidence: conf, Reason: "test"}
}

func mustChain(t *testing.T, steps ...Step) *Chain {
	t.Helper()
	c, err := NewChain(steps...)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	return c
}

func TestChainDecide(t *testing.T) {
	tests := []struct {
		name       string
		engines    []*fakeEngine
		thresholds []float64
		want       Decision
		wantCalls  []int
	}{
		{
			name:       "first match wins and later engines are not called",
			engines:    []*fakeEngine{{dec: mv("A", 0.9)}, {dec: mv("B", 1.0)}},
			thresholds: []float64{0.5, 0.5},
			want:       Decision{Action: Move, Target: "A", Confidence: 0.9, Reason: "test", Source: "e0"},
			wantCalls:  []int{1, 0},
		},
		{
			name:       "engine-provided source is preserved",
			engines:    []*fakeEngine{{dec: Decision{Action: Move, Target: "A", Confidence: 1, Reason: "test", Source: "rule:invoices"}}},
			thresholds: []float64{1},
			want:       Decision{Action: Move, Target: "A", Confidence: 1, Reason: "test", Source: "rule:invoices"},
			wantCalls:  []int{1},
		},
		{
			name:       "confidence exactly at threshold is accepted",
			engines:    []*fakeEngine{{dec: mv("A", 0.8)}},
			thresholds: []float64{0.8},
			want:       Decision{Action: Move, Target: "A", Confidence: 0.8, Reason: "test", Source: "e0"},
			wantCalls:  []int{1},
		},
		{
			name:       "below threshold falls through to next engine",
			engines:    []*fakeEngine{{dec: mv("A", 0.79)}, {dec: mv("B", 0.6)}},
			thresholds: []float64{0.8, 0.5},
			want:       Decision{Action: Move, Target: "B", Confidence: 0.6, Reason: "test", Source: "e1"},
			wantCalls:  []int{1, 1},
		},
		{
			name:       "each engine uses its own threshold",
			engines:    []*fakeEngine{{dec: mv("A", 0.6)}, {dec: mv("B", 0.6)}},
			thresholds: []float64{0.9, 0.6},
			want:       Decision{Action: Move, Target: "B", Confidence: 0.6, Reason: "test", Source: "e1"},
			wantCalls:  []int{1, 1},
		},
		{
			name: "ErrNoDecision falls through",
			engines: []*fakeEngine{
				{err: ErrNoDecision},
				{dec: mv("B", 0.7)},
			},
			thresholds: []float64{0.5, 0.5},
			want:       Decision{Action: Move, Target: "B", Confidence: 0.7, Reason: "test", Source: "e1"},
			wantCalls:  []int{1, 1},
		},
		{
			name: "wrapped ErrNoDecision falls through",
			engines: []*fakeEngine{
				{err: errors.Join(errors.New("no rule matched"), ErrNoDecision)},
				{dec: mv("B", 0.7)},
			},
			thresholds: []float64{0.5, 0.5},
			want:       Decision{Action: Move, Target: "B", Confidence: 0.7, Reason: "test", Source: "e1"},
			wantCalls:  []int{1, 1},
		},
		{
			name: "all engines say no decision yields AskUser",
			engines: []*fakeEngine{
				{err: ErrNoDecision},
				{err: ErrNoDecision},
			},
			thresholds: []float64{0.5, 0.5},
			want:       askUser,
			wantCalls:  []int{1, 1},
		},
		{
			name:       "all engines below threshold yields AskUser",
			engines:    []*fakeEngine{{dec: mv("A", 0.1)}, {dec: mv("B", 0.2)}},
			thresholds: []float64{0.5, 0.5},
			want:       askUser,
			wantCalls:  []int{1, 1},
		},
		{
			name:       "NaN confidence never clears a threshold",
			engines:    []*fakeEngine{{dec: mv("A", math.NaN())}},
			thresholds: []float64{0},
			want:       askUser,
			wantCalls:  []int{1},
		},
		{
			name:       "zero threshold accepts any valid decision",
			engines:    []*fakeEngine{{dec: Decision{Action: Ignore, Confidence: 0}}},
			thresholds: []float64{0},
			want:       Decision{Action: Ignore, Source: "e0"},
			wantCalls:  []int{1},
		},
		{
			name:       "empty chain yields AskUser",
			engines:    nil,
			thresholds: nil,
			want:       askUser,
			wantCalls:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := make([]Step, len(tt.engines))
			for i, e := range tt.engines {
				steps[i] = Step{Name: "e" + string(rune('0'+i)), Engine: e, MinConfidence: tt.thresholds[i]}
			}
			got, err := mustChain(t, steps...).Decide(context.Background(), Facts{})
			if err != nil {
				t.Fatalf("Decide: unexpected error: %v", err)
			}
			if got.Action != tt.want.Action || got.Target != tt.want.Target ||
				got.Confidence != tt.want.Confidence || got.Reason != tt.want.Reason ||
				got.Source != tt.want.Source {
				t.Errorf("Decide = %+v, want %+v", got, tt.want)
			}
			for i, e := range tt.engines {
				if e.calls != tt.wantCalls[i] {
					t.Errorf("engine %d called %d times, want %d", i, e.calls, tt.wantCalls[i])
				}
			}
		})
	}
}

func TestChainDecideAskUserFallback(t *testing.T) {
	got, err := mustChain(t).Decide(context.Background(), Facts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != AskUser || got.Source != "chain" || got.Reason == "" || got.Target != "" {
		t.Errorf("unexpected fallback decision: %+v", got)
	}
}

func TestChainDecidePassesFactsToEngine(t *testing.T) {
	want := Facts{Path: "/r/a.pdf", RelPath: "a.pdf", Ext: ".pdf"}
	var got Facts
	e := funcEngine(func(_ context.Context, f Facts) (Decision, error) {
		got = f
		return mv("A", 1), nil
	})
	if _, err := mustChain(t, Step{Name: "e", Engine: e}).Decide(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if got.Path != want.Path || got.RelPath != want.RelPath || got.Ext != want.Ext {
		t.Errorf("engine got %+v, want %+v", got, want)
	}
}

func TestChainDecideEngineError(t *testing.T) {
	boom := errors.New("boom")
	first := &fakeEngine{err: ErrNoDecision}
	failing := &fakeEngine{err: boom}
	last := &fakeEngine{dec: mv("C", 1)}

	c := mustChain(t,
		Step{Name: "rules", Engine: first},
		Step{Name: "semantic", Engine: failing},
		Step{Name: "llm", Engine: last},
	)
	_, err := c.Decide(context.Background(), Facts{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the engine's error", err)
	}
	var ee *EngineError
	if !errors.As(err, &ee) {
		t.Fatalf("error %v is not an *EngineError", err)
	}
	if ee.Name != "semantic" {
		t.Errorf("EngineError.Name = %q, want %q", ee.Name, "semantic")
	}
	if !strings.Contains(err.Error(), "semantic") {
		t.Errorf("error message %q does not name the engine", err.Error())
	}
	if last.calls != 0 {
		t.Errorf("engines after the failing one were called %d times, want 0", last.calls)
	}
}

func TestChainDecideInvalidDecisionIsEngineError(t *testing.T) {
	e := &fakeEngine{dec: Decision{Action: Action("delete"), Confidence: 1}}
	_, err := mustChain(t, Step{Name: "bad", Engine: e}).Decide(context.Background(), Facts{})
	var ee *EngineError
	if !errors.As(err, &ee) || ee.Name != "bad" {
		t.Fatalf("want *EngineError for %q, got %v", "bad", err)
	}
}

func TestChainDecideContextCancellation(t *testing.T) {
	t.Run("already cancelled context calls no engine", func(t *testing.T) {
		e := &fakeEngine{dec: mv("A", 1)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := mustChain(t, Step{Name: "e", Engine: e}).Decide(ctx, Facts{})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if e.calls != 0 {
			t.Errorf("engine called %d times, want 0", e.calls)
		}
	})

	t.Run("cancel between engines stops the chain", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first := funcEngine(func(context.Context, Facts) (Decision, error) {
			cancel()
			return Decision{}, ErrNoDecision
		})
		second := &fakeEngine{dec: mv("B", 1)}
		_, err := mustChain(t,
			Step{Name: "first", Engine: first},
			Step{Name: "second", Engine: second},
		).Decide(ctx, Facts{})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if second.calls != 0 {
			t.Errorf("second engine called %d times, want 0", second.calls)
		}
	})

	t.Run("blocking engine returns promptly on deadline", func(t *testing.T) {
		blocking := funcEngine(func(ctx context.Context, _ Facts) (Decision, error) {
			<-ctx.Done()
			return Decision{}, ctx.Err()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := mustChain(t, Step{Name: "slow", Engine: blocking}).Decide(ctx, Facts{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("Decide took %v after cancellation, want prompt return", elapsed)
		}
	})

	t.Run("engine error caused by cancellation is reported as the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		e := funcEngine(func(context.Context, Facts) (Decision, error) {
			cancel()
			return Decision{}, errors.New("aborted")
		})
		_, err := mustChain(t, Step{Name: "e", Engine: e}).Decide(ctx, Facts{})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

func TestNewChainValidation(t *testing.T) {
	ok := &fakeEngine{}
	tests := []struct {
		name string
		step Step
	}{
		{"empty name", Step{Name: "", Engine: ok}},
		{"nil engine", Step{Name: "e", Engine: nil}},
		{"negative threshold", Step{Name: "e", Engine: ok, MinConfidence: -0.1}},
		{"threshold above one", Step{Name: "e", Engine: ok, MinConfidence: 1.1}},
		{"NaN threshold", Step{Name: "e", Engine: ok, MinConfidence: math.NaN()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewChain(tt.step); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}

	t.Run("duplicate names", func(t *testing.T) {
		_, err := NewChain(Step{Name: "e", Engine: ok}, Step{Name: "e", Engine: ok})
		if err == nil {
			t.Error("expected error, got nil")
		}
	})
}

func TestNewChainCopiesSteps(t *testing.T) {
	steps := []Step{{Name: "a", Engine: &fakeEngine{dec: mv("A", 1)}}}
	c := mustChain(t, steps...)
	steps[0].Engine = &fakeEngine{dec: mv("Z", 1)}
	got, err := c.Decide(context.Background(), Facts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "A" {
		t.Errorf("Target = %q; chain was affected by caller mutating its slice", got.Target)
	}
}

func TestChainIsAnEngine(t *testing.T) {
	var _ Engine = (*Chain)(nil)
}
