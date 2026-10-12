package rules

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
)

func moveSpec(id, when string) Spec {
	return Spec{ID: id, When: when, Action: decision.Move, Target: "T/" + id}
}

// A rule that would run for ages must be refused when it is loaded, before it
// can be evaluated at all.
func TestRunaway_RejectedAtCompileTime(t *testing.T) {
	for name, when := range map[string]string{
		"billion element range":  `all(1..1000000000, # > 0)`,
		"nested loops":           `all(split(Snippet, ""), all(split(Snippet, ""), # == #))`,
		"repeat to exhaust RAM":  `len(repeat("abcdefgh", 1000000000)) > 0`,
		"dynamic regex per eval": `Snippet matches Snippet`,
	} {
		e := New([]Spec{moveSpec("runaway", when)})
		if e.Len() != 0 || len(e.Issues()) != 1 {
			t.Errorf("%s: want 1 issue and 0 rules, got %d issues, %d rules", name, len(e.Issues()), e.Len())
		}
	}
}

// If an evaluation does overrun anyway (here simulated by blocking), Decide
// must return promptly, skip the rule, keep evaluating the others and never
// start the stuck rule again.
func TestRunaway_DeadlineStopsWaitingAndQuarantines(t *testing.T) {
	release := make(chan struct{})
	var stuckStarts atomic.Int32
	var gotErr atomic.Value

	e := New(
		[]Spec{moveSpec("stuck", `true`), moveSpec("after", `true`)},
		WithEvalTimeout(20*time.Millisecond),
		WithEvalErrorHook(func(id string, err error) {
			if id == "stuck" {
				gotErr.Store(err)
			}
		}),
	)
	real := e.eval
	e.eval = func(r *rule, f decision.Facts) (bool, error) {
		if r.spec.ID == "stuck" {
			stuckStarts.Add(1)
			<-release
			return true, nil
		}
		return real(r, f)
	}

	start := time.Now()
	d, err := e.Decide(context.Background(), decision.Facts{})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Decide took %v; the deadline did not stop the wait", elapsed)
	}
	if err != nil || d.Source != "rule:after" {
		t.Fatalf("got %+v, %v; want the next rule to decide", d, err)
	}
	if got, _ := gotErr.Load().(error); !errors.Is(got, errTimeout) {
		t.Errorf("hook error = %v, want timeout", got)
	}

	// Second call: the stuck rule is quarantined and not started again.
	if _, err := e.Decide(context.Background(), decision.Facts{}); err != nil {
		t.Fatal(err)
	}
	if n := stuckStarts.Load(); n != 1 {
		t.Errorf("stuck rule started %d times, want 1", n)
	}

	// A reload clears the quarantine.
	e.Apply([]Spec{moveSpec("stuck", `true`)})
	e.eval = real
	if d, err := e.Decide(context.Background(), decision.Facts{}); err != nil || d.Source != "rule:stuck" {
		t.Errorf("after reload got %+v, %v", d, err)
	}
	close(release) // let the abandoned goroutine finish
}

func TestRunaway_CallerCancellationDoesNotQuarantine(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	e := New([]Spec{moveSpec("slow", `true`)}, WithEvalTimeout(time.Minute))
	e.eval = func(*rule, decision.Facts) (bool, error) { <-release; return true, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.Decide(ctx, decision.Facts{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if e.current.Load().rules[0].quarantined.Load() {
		t.Error("rule was quarantined although the caller gave up")
	}
}

func TestRunaway_PanicIsContained(t *testing.T) {
	e := New([]Spec{moveSpec("panics", `true`), moveSpec("ok", `true`)})
	real := e.eval
	e.eval = func(r *rule, f decision.Facts) (bool, error) {
		if r.spec.ID == "panics" {
			panic("boom")
		}
		return real(r, f)
	}
	d, err := e.Decide(context.Background(), decision.Facts{})
	if err != nil || d.Source != "rule:ok" {
		t.Fatalf("got %+v, %v", d, err)
	}
}

// An expression that passes the compile-time checks but is genuinely slow on
// large Facts (a quadratic scan: one regexp over Snippet per Vector element)
// is stopped by the real deadline, with no test double involved. The abandoned
// evaluation finishes by itself shortly after the test.
func TestRunaway_RealSlowEvaluationHitsDeadline(t *testing.T) {
	f := decision.Facts{
		Snippet: strings.Repeat("lorem ipsum dolor sit amet ", 400), // about 10 KB
		Vector:  make([]float32, 8192),
	}
	slow := `count(Vector, # == 0 && Snippet matches "(a|b)*c") > 0`
	var hooked atomic.Value
	e := New(
		[]Spec{moveSpec("slow", slow), moveSpec("after", `true`)},
		WithEvalTimeout(5*time.Millisecond),
		WithEvalErrorHook(func(id string, err error) { hooked.Store(id + ": " + err.Error()) }),
	)
	if len(e.Issues()) != 0 {
		t.Fatalf("expression should be allowed at compile time: %v", e.Issues())
	}

	start := time.Now()
	d, err := e.Decide(context.Background(), f)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Decide took %v; the deadline did not stop the wait", elapsed)
	}
	if err != nil || d.Source != "rule:after" {
		t.Fatalf("got %+v, %v", d, err)
	}
	if got, _ := hooked.Load().(string); !strings.HasPrefix(got, "slow: ") {
		t.Errorf("hook = %q", got)
	}
	if !e.current.Load().rules[0].quarantined.Load() {
		t.Error("slow rule was not quarantined")
	}
}
