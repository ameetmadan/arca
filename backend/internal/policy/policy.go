// Package policy decides what happens to a Decision once an engine has made
// it: apply it now, put it in the user's queue, or drop it. It is pure domain
// logic with no storage, filesystem or HTTP, so it is testable on its own.
//
// Each watched folder has Settings. The default mode is "suggest", and a
// folder is only trusted with "auto" after the user has accepted at least 90%
// of at least 20 of its decisions (ADR-0006, ADR-0007).
package policy

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/ameetmadan/arca/pkg/decision"
)

// Mode says how much freedom Arca has in a folder.
type Mode string

const (
	// ModeOff means Arca makes no suggestions and does nothing for the folder.
	ModeOff Mode = "off"
	// ModeSuggest queues every actionable decision for the user to accept.
	ModeSuggest Mode = "suggest"
	// ModeAuto applies confident decisions without asking, once the folder
	// has earned it (see Stats.Eligible).
	ModeAuto Mode = "auto"
)

// Valid reports whether m is one of the defined modes.
func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeSuggest, ModeAuto:
		return true
	}
	return false
}

const (
	// DefaultAutoThreshold is the confidence a decision needs for auto-apply
	// unless the user chose another value.
	DefaultAutoThreshold = 0.9

	// MinDecisions is how many resolved decisions a folder needs before it can
	// use auto mode.
	MinDecisions = 20
	// MinAcceptanceRate is the share of resolved decisions the user must have
	// accepted before a folder can use auto mode.
	MinAcceptanceRate = 0.9
)

// ErrInvalidSettings is wrapped by Settings.Validate errors.
var ErrInvalidSettings = errors.New("policy: invalid settings")

// Settings is the policy of one watched folder.
type Settings struct {
	// Mode is off, suggest or auto.
	Mode Mode
	// AutoThreshold is the minimum Decision.Confidence, in (0, 1], for a
	// decision to be applied without asking. It only matters in auto mode.
	AutoThreshold float64
}

// DefaultSettings returns the settings of a folder nobody has configured.
func DefaultSettings() Settings {
	return Settings{Mode: ModeSuggest, AutoThreshold: DefaultAutoThreshold}
}

// Validate checks the settings and returns an error wrapping
// ErrInvalidSettings that says what is wrong.
func (s Settings) Validate() error {
	if !s.Mode.Valid() {
		return fmt.Errorf("%w: mode must be one of off, suggest, auto", ErrInvalidSettings)
	}
	if math.IsNaN(s.AutoThreshold) || s.AutoThreshold <= 0 || s.AutoThreshold > 1 {
		return fmt.Errorf("%w: auto_threshold must be greater than 0 and at most 1", ErrInvalidSettings)
	}
	return nil
}

// Stats summarises how the user responded to a folder's decisions.
type Stats struct {
	// Decided is the number of decisions the user accepted or rejected.
	Decided int
	// Accepted is how many of those were accepted. It never exceeds Decided.
	Accepted int
}

// Rate returns Accepted/Decided, or 0 when nothing has been decided.
func (s Stats) Rate() float64 {
	if s.Decided <= 0 {
		return 0
	}
	return float64(s.Accepted) / float64(s.Decided)
}

// Eligible reports whether the folder has earned auto mode: at least
// MinDecisions resolved decisions with an acceptance rate of at least
// MinAcceptanceRate. Inconsistent stats are never eligible.
func (s Stats) Eligible() bool {
	if s.Accepted < 0 || s.Accepted > s.Decided || s.Decided < MinDecisions {
		return false
	}
	return s.Rate() >= MinAcceptanceRate
}

// StatsProvider reports acceptance statistics for a watched folder. The
// feedback loop will provide the long-term implementation; until then the
// decision queue computes them from its own records.
type StatsProvider interface {
	AcceptanceStats(ctx context.Context, root string) (Stats, error)
}

// Outcome is what to do with a decision.
type Outcome int

const (
	// Drop discards the decision: the folder is off, or the action needs no
	// user attention or cannot be executed.
	Drop Outcome = iota
	// Queue stores the decision for the user to accept or reject.
	Queue
	// ApplyNow executes the decision immediately.
	ApplyNow
)

// String returns the outcome's name.
func (o Outcome) String() string {
	switch o {
	case Drop:
		return "drop"
	case Queue:
		return "queue"
	case ApplyNow:
		return "apply_now"
	}
	return fmt.Sprintf("outcome(%d)", int(o))
}

// Evaluate decides what to do with d for a folder with settings s and
// acceptance history st.
//
//   - off: Drop everything.
//   - Only Move can be executed today, and only Move is ever applied
//     automatically. AskUser always goes to the queue, because it needs a
//     human to choose a target. Tag, Index, Ignore and unknown actions are
//     Dropped: there is nothing for the user to accept.
//   - suggest: Queue.
//   - auto: ApplyNow when confidence reaches the threshold and the folder has
//     earned autonomy; otherwise Queue.
//
// An unknown mode behaves like suggest, so a bad value can never cause an
// unreviewed move.
func Evaluate(s Settings, d decision.Decision, st Stats) Outcome {
	if s.Mode == ModeOff {
		return Drop
	}
	switch d.Action {
	case decision.AskUser:
		return Queue
	case decision.Move:
		// handled below
	default:
		return Drop
	}
	if s.Mode == ModeAuto && d.Confidence >= s.AutoThreshold && st.Eligible() {
		return ApplyNow
	}
	return Queue
}
