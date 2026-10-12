package decision

import (
	"context"
	"errors"
	"fmt"
	"math"
)

const (
	fallbackSource = "chain"
	fallbackReason = "no engine was confident enough"
)

// Step is one engine in a Chain together with the minimum confidence a
// decision from that engine needs to be accepted.
type Step struct {
	// Name identifies the engine in Decision.Source and in error messages,
	// e.g. "rules", "semantic" or "llm". It must be unique within a chain.
	Name string
	// Engine is the engine to consult.
	Engine Engine
	// MinConfidence is the lowest Decision.Confidence, in [0, 1], that this
	// engine's answer is accepted at. Lower-confidence answers fall through.
	MinConfidence float64
}

// EngineError reports that a specific engine in a Chain failed. It wraps the
// engine's own error, so errors.Is and errors.As see through it.
type EngineError struct {
	// Name is the Step.Name of the failing engine.
	Name string
	// Err is the underlying error.
	Err error
}

// Error implements error.
func (e *EngineError) Error() string {
	return fmt.Sprintf("decision: engine %q: %v", e.Name, e.Err)
}

// Unwrap returns the underlying error.
func (e *EngineError) Unwrap() error { return e.Err }

// Chain consults engines in order and returns the first decision whose
// confidence meets that engine's threshold. A Chain is immutable once built and
// is safe for concurrent use if its engines are. It implements Engine, so
// chains can be nested.
type Chain struct {
	steps []Step
}

// NewChain builds a Chain from steps, which are consulted in the given order.
// It returns an error if a step has an empty or duplicate name, a nil engine,
// or a MinConfidence outside [0, 1]. The steps slice is copied.
func NewChain(steps ...Step) (*Chain, error) {
	seen := make(map[string]struct{}, len(steps))
	for i, s := range steps {
		switch {
		case s.Name == "":
			return nil, fmt.Errorf("decision: step %d has an empty name", i)
		case s.Engine == nil:
			return nil, fmt.Errorf("decision: step %q has a nil engine", s.Name)
		case math.IsNaN(s.MinConfidence) || s.MinConfidence < 0 || s.MinConfidence > 1:
			return nil, fmt.Errorf("decision: step %q has MinConfidence %v outside [0, 1]", s.Name, s.MinConfidence)
		}
		if _, dup := seen[s.Name]; dup {
			return nil, fmt.Errorf("decision: duplicate step name %q", s.Name)
		}
		seen[s.Name] = struct{}{}
	}
	return &Chain{steps: append([]Step(nil), steps...)}, nil
}

// Decide asks each engine in order and returns the first decision that meets
// its engine's MinConfidence. Decision.Source is kept as the engine set it, so
// a rules engine can report "rule:invoices"; if the engine left it empty, it is
// set to the step's name.
//
// Engines that return ErrNoDecision, or a decision below their threshold, are
// skipped. If no engine produces an acceptable decision, Decide returns an
// AskUser decision with Source "chain" and a nil error.
//
// If an engine fails with any other error, or returns an invalid action,
// Decide stops and returns an *EngineError naming that engine. If ctx is
// cancelled, Decide stops before consulting the next engine and returns the
// context's error.
func (c *Chain) Decide(ctx context.Context, f Facts) (Decision, error) {
	for _, s := range c.steps {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}

		d, err := s.Engine.Decide(ctx, f)
		if cerr := ctx.Err(); cerr != nil {
			// Whatever the engine returned, the caller no longer wants an answer.
			return Decision{}, cerr
		}
		if err != nil {
			if errors.Is(err, ErrNoDecision) {
				continue
			}
			return Decision{}, &EngineError{Name: s.Name, Err: err}
		}
		if !d.Action.Valid() {
			return Decision{}, &EngineError{Name: s.Name, Err: fmt.Errorf("invalid action %q", d.Action)}
		}
		// A NaN confidence compares false here and therefore never clears a threshold.
		if d.Confidence >= s.MinConfidence {
			if d.Source == "" {
				d.Source = s.Name
			}
			return d, nil
		}
	}
	return Decision{Action: AskUser, Reason: fallbackReason, Source: fallbackSource}, nil
}
