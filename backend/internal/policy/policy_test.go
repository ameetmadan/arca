package policy

import (
	"errors"
	"math"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision"
)

var eligible = Stats{Decided: 20, Accepted: 18} // exactly at the bar

func mv(conf float64) decision.Decision {
	return decision.Decision{Action: decision.Move, Target: "Docs", Confidence: conf}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name  string
		set   Settings
		d     decision.Decision
		stats Stats
		want  Outcome
	}{
		// mode off: nothing is ever suggested or applied
		{"off drops move", Settings{Mode: ModeOff, AutoThreshold: 0.9}, mv(1), eligible, Drop},
		{"off drops ask_user", Settings{Mode: ModeOff, AutoThreshold: 0.9}, decision.Decision{Action: decision.AskUser}, eligible, Drop},

		// mode suggest: always queue, however confident
		{"suggest queues low confidence", Settings{Mode: ModeSuggest, AutoThreshold: 0.9}, mv(0.1), eligible, Queue},
		{"suggest queues high confidence", Settings{Mode: ModeSuggest, AutoThreshold: 0.9}, mv(1), eligible, Queue},

		// mode auto: confidence x eligibility
		{"auto applies above threshold when eligible", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, mv(0.95), eligible, ApplyNow},
		{"auto applies at threshold when eligible", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, mv(0.9), eligible, ApplyNow},
		{"auto queues below threshold", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, mv(0.89), eligible, Queue},
		{"auto queues when too few decisions", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, mv(1), Stats{Decided: 19, Accepted: 19}, Queue},
		{"auto queues when acceptance too low", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, mv(1), Stats{Decided: 100, Accepted: 89}, Queue},
		{"auto queues with no history", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, mv(1), Stats{}, Queue},
		{"auto queues NaN confidence", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, mv(math.NaN()), eligible, Queue},

		// unknown mode fails safe
		{"unknown mode queues", Settings{Mode: "bogus", AutoThreshold: 0.9}, mv(1), eligible, Queue},

		// non-move actions are never applied automatically
		{"suggest queues ask_user", Settings{Mode: ModeSuggest, AutoThreshold: 0.9}, decision.Decision{Action: decision.AskUser}, eligible, Queue},
		{"auto still queues ask_user", Settings{Mode: ModeAuto, AutoThreshold: 0.5}, decision.Decision{Action: decision.AskUser, Confidence: 1}, eligible, Queue},
		{"index needs no review", Settings{Mode: ModeSuggest, AutoThreshold: 0.9}, decision.Decision{Action: decision.Index, Confidence: 1}, eligible, Drop},
		{"ignore needs no review", Settings{Mode: ModeSuggest, AutoThreshold: 0.9}, decision.Decision{Action: decision.Ignore, Confidence: 1}, eligible, Drop},
		{"tag cannot be executed yet", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, decision.Decision{Action: decision.Tag, Target: "x", Confidence: 1}, eligible, Drop},
		{"invalid action dropped", Settings{Mode: ModeAuto, AutoThreshold: 0.9}, decision.Decision{Action: "explode", Confidence: 1}, eligible, Drop},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.set, tc.d, tc.stats); got != tc.want {
				t.Fatalf("Evaluate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStatsEligible(t *testing.T) {
	tests := []struct {
		s    Stats
		want bool
	}{
		{Stats{}, false},
		{Stats{Decided: 19, Accepted: 19}, false},
		{Stats{Decided: 20, Accepted: 17}, false}, // 85%
		{Stats{Decided: 20, Accepted: 18}, true},  // 90%
		{Stats{Decided: 20, Accepted: 20}, true},
		{Stats{Decided: 30, Accepted: 27}, true},
		{Stats{Decided: 1000, Accepted: 899}, false},
		{Stats{Decided: 20, Accepted: 21}, false}, // inconsistent input is not trusted
		{Stats{Decided: 20, Accepted: -1}, false},
	}
	for _, tc := range tests {
		if got := tc.s.Eligible(); got != tc.want {
			t.Errorf("%+v.Eligible() = %v, want %v", tc.s, got, tc.want)
		}
	}
}

func TestStatsRate(t *testing.T) {
	if r := (Stats{}).Rate(); r != 0 {
		t.Fatalf("empty rate = %v", r)
	}
	if r := (Stats{Decided: 4, Accepted: 3}).Rate(); r != 0.75 {
		t.Fatalf("rate = %v", r)
	}
}

func TestSettingsValidate(t *testing.T) {
	ok := []Settings{
		{Mode: ModeOff, AutoThreshold: 0.9},
		{Mode: ModeSuggest, AutoThreshold: 0.5},
		{Mode: ModeAuto, AutoThreshold: 1},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: unexpected error %v", s, err)
		}
	}
	bad := []Settings{
		{Mode: "", AutoThreshold: 0.9},
		{Mode: "AUTO", AutoThreshold: 0.9},
		{Mode: ModeAuto, AutoThreshold: 0},
		{Mode: ModeAuto, AutoThreshold: -0.1},
		{Mode: ModeAuto, AutoThreshold: 1.01},
		{Mode: ModeAuto, AutoThreshold: math.NaN()},
		{Mode: ModeAuto, AutoThreshold: math.Inf(1)},
	}
	for _, s := range bad {
		err := s.Validate()
		if err == nil || !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("%+v: got %v, want ErrInvalidSettings", s, err)
		}
	}
}

func TestDefaultSettings(t *testing.T) {
	d := DefaultSettings()
	if d.Mode != ModeSuggest {
		t.Fatalf("default mode = %q, want suggest", d.Mode)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOutcomeString(t *testing.T) {
	if ApplyNow.String() != "apply_now" || Queue.String() != "queue" || Drop.String() != "drop" {
		t.Fatal("unexpected outcome names")
	}
}
