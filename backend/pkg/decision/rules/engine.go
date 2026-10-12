package rules

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
)

// DefaultEvalTimeout is how long one rule may run against one file before it
// is abandoned and disabled.
const DefaultEvalTimeout = 250 * time.Millisecond

// Option configures an Engine.
type Option func(*Engine)

// WithEvalTimeout sets the per-rule, per-file evaluation deadline. Values
// <= 0 are ignored. See the package documentation for what the deadline can
// and cannot guarantee.
func WithEvalTimeout(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// WithEvalErrorHook registers a function that is called when a rule fails
// while being evaluated (type mismatch, runtime error, panic or timeout). The
// failed rule counts as "did not match" and evaluation continues with the next
// rule. The error never contains file contents, so it is safe to log. The hook
// may be called concurrently and must not block.
func WithEvalErrorHook(fn func(ruleID string, err error)) Option {
	return func(e *Engine) { e.onEvalError = fn }
}

// ruleSet is an immutable snapshot of compiled rules. Reloading swaps the
// whole snapshot, so a Decide call never sees a half-loaded rule file.
type ruleSet struct {
	rules  []*rule
	issues []RuleError
}

// Engine is the rules engine: it evaluates user-written rules against Facts
// and implements [decision.Engine]. The first rule that matches, in file
// order, decides. It is safe for concurrent use, including while rules are
// being reloaded.
type Engine struct {
	current     atomic.Pointer[ruleSet]
	reloadMu    sync.Mutex // serialises Apply so concurrent reloads land in call order
	timeout     time.Duration
	onEvalError func(ruleID string, err error)

	// eval is a seam for tests; it is r.eval in production.
	eval func(r *rule, f decision.Facts) (bool, error)
}

var _ decision.Engine = (*Engine)(nil)

// New builds an Engine from specs. It never fails: rules that are invalid are
// left out and reported by [Engine.Issues], and the others still load.
func New(specs []Spec, opts ...Option) *Engine {
	e := newEngine(opts)
	e.Apply(specs)
	return e
}

// Load builds an Engine from the YAML rules file at path. A missing file means
// "no rules". It returns an error only if the file cannot be read or is not
// valid YAML of the expected shape; invalid individual rules are reported by
// [Engine.Issues] and do not stop the others from loading.
func Load(path string, opts ...Option) (*Engine, error) {
	specs, err := NewFileStore(path).Load()
	if err != nil {
		return nil, err
	}
	return New(specs, opts...), nil
}

func newEngine(opts []Option) *Engine {
	e := &Engine{
		timeout: DefaultEvalTimeout,
		eval:    func(r *rule, f decision.Facts) (bool, error) { return r.eval(f) },
	}
	for _, o := range opts {
		o(e)
	}
	e.current.Store(&ruleSet{})
	return e
}

// Reload re-reads the rules file at path and atomically replaces the active
// rules. If the file cannot be read or parsed, the error is returned and the
// previous rules stay active. Rules that are individually invalid are left out
// and reported by [Engine.Issues]. Reload may be called while Decide is
// running on other goroutines.
func (e *Engine) Reload(path string) error {
	specs, err := NewFileStore(path).Load()
	if err != nil {
		return err
	}
	e.Apply(specs)
	return nil
}

// Apply compiles specs and atomically replaces the active rules with the valid
// ones. It returns the problems found, which are also available from
// [Engine.Issues]. It is what an HTTP handler calls after saving rules through
// a [Store].
func (e *Engine) Apply(specs []Spec) []RuleError {
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()
	compiled, issues := compileAll(specs)
	e.current.Store(&ruleSet{rules: compiled, issues: issues})
	return issues
}

// Issues returns the problems found when the active rules were loaded, in file
// order. The result is a copy and may be modified by the caller.
func (e *Engine) Issues() []RuleError {
	return append([]RuleError(nil), e.current.Load().issues...)
}

// Len returns the number of active (valid) rules.
func (e *Engine) Len() int { return len(e.current.Load().rules) }

// Decide returns the decision of the first rule that matches f, with
// Confidence 1 and Source "rule:<id>". If no rule matches it returns
// [decision.ErrNoDecision].
//
// A rule that fails to evaluate (for example a comparison between mismatched
// types that only shows up at run time) is treated as not matching; it never
// fails the whole decision. If ctx is cancelled, Decide returns ctx.Err().
func (e *Engine) Decide(ctx context.Context, f decision.Facts) (decision.Decision, error) {
	for _, r := range e.current.Load().rules {
		if err := ctx.Err(); err != nil {
			return decision.Decision{}, err
		}
		if r.quarantined.Load() {
			continue
		}
		matched, err := e.run(ctx, r, f)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return decision.Decision{}, cerr
			}
			if e.onEvalError != nil {
				e.onEvalError(r.spec.ID, err)
			}
			continue
		}
		if matched {
			return decision.Decision{
				Action:     r.spec.Action,
				Target:     r.target,
				Confidence: 1,
				Reason:     r.reason,
				Source:     "rule:" + r.spec.ID,
			}, nil
		}
	}
	return decision.Decision{}, decision.ErrNoDecision
}

type evalResult struct {
	matched bool
	err     error
}

// run evaluates one rule with a deadline. The evaluation happens on its own
// goroutine so that Decide can give up waiting; Go cannot kill a goroutine, so
// an evaluation that overruns keeps running in the background until it ends on
// its own. That is bounded by the compile-time restrictions (see compileExpr)
// and by quarantining the rule so it is not started again.
func (e *Engine) run(ctx context.Context, r *rule, f decision.Facts) (bool, error) {
	tctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	eval := e.eval // read here, not in the goroutine, which may outlive this call
	done := make(chan evalResult, 1) // buffered: an abandoned goroutine must not block forever
	go func() {
		defer func() {
			if recover() != nil {
				done <- evalResult{err: errPanic}
			}
		}()
		matched, err := eval(r, f)
		done <- evalResult{matched, err}
	}()

	select {
	case res := <-done:
		return res.matched, res.err
	case <-tctx.Done():
		if ctx.Err() != nil {
			return false, ctx.Err() // the caller gave up; the rule is not at fault
		}
		r.quarantined.Store(true)
		return false, errTimeout
	}
}
