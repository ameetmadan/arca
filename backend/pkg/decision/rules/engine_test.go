package rules_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/decision/rules"
)

func spec(id, when string) rules.Spec {
	return rules.Spec{ID: id, When: when, Action: decision.Move, Target: "Folder/" + id}
}

func invoiceFacts() decision.Facts {
	return decision.Facts{
		Path:     "/root/inbox/a.pdf",
		RelPath:  "inbox/a.pdf",
		Ext:      ".pdf",
		MIME:     "application/pdf",
		Size:     2048,
		Modified: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		EXIF:     map[string]string{"Make": "Apple"},
		Snippet:  "Invoice no. 42",
	}
}

func TestDecide_Match(t *testing.T) {
	s := rules.Spec{
		ID: "invoices", When: `Ext == ".pdf" && Snippet contains "Invoice"`,
		Action: decision.Move, Target: "Finance/Invoices", Reason: "PDF mentioning an invoice",
	}
	e := rules.New([]rules.Spec{s})
	if got := e.Issues(); len(got) != 0 {
		t.Fatalf("unexpected issues: %v", got)
	}

	d, err := e.Decide(context.Background(), invoiceFacts())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	want := decision.Decision{
		Action: decision.Move, Target: "Finance/Invoices", Confidence: 1,
		Reason: "PDF mentioning an invoice", Source: "rule:invoices",
	}
	if d != want {
		t.Errorf("got %+v, want %+v", d, want)
	}
}

func TestDecide_DefaultReasonAndTargetCleaned(t *testing.T) {
	s := rules.Spec{ID: "r", When: `true`, Action: decision.Move, Target: "A//B/"}
	d, err := rules.New([]rules.Spec{s}).Decide(context.Background(), decision.Facts{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "A/B" {
		t.Errorf("Target = %q, want A/B", d.Target)
	}
	if d.Reason != "matched rule r" {
		t.Errorf("Reason = %q", d.Reason)
	}
}

func TestDecide_NoMatch(t *testing.T) {
	e := rules.New([]rules.Spec{spec("docx", `Ext == ".docx"`)})
	_, err := e.Decide(context.Background(), invoiceFacts())
	if !errors.Is(err, decision.ErrNoDecision) {
		t.Fatalf("err = %v, want ErrNoDecision", err)
	}
}

func TestDecide_NoRules(t *testing.T) {
	_, err := rules.New(nil).Decide(context.Background(), invoiceFacts())
	if !errors.Is(err, decision.ErrNoDecision) {
		t.Fatalf("err = %v, want ErrNoDecision", err)
	}
}

func TestDecide_FirstMatchInFileOrderWins(t *testing.T) {
	specs := []rules.Spec{
		spec("first-no", `Ext == ".txt"`),
		spec("second", `Ext == ".pdf"`),
		spec("third", `true`),
	}
	d, err := rules.New(specs).Decide(context.Background(), invoiceFacts())
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "rule:second" {
		t.Errorf("Source = %q, want rule:second", d.Source)
	}

	// Reversing the order changes the winner: order is the only tie-breaker.
	specs[1], specs[2] = specs[2], specs[1]
	d, err = rules.New(specs).Decide(context.Background(), invoiceFacts())
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "rule:third" {
		t.Errorf("Source = %q, want rule:third", d.Source)
	}
}

func TestNew_InvalidRulesReportedWithoutBreakingOthers(t *testing.T) {
	specs := []rules.Spec{
		spec("syntax", `Ext ==`),
		spec("unknown-field", `Colour == "red"`),
		spec("not-bool", `Size + 1`),
		{ID: "bad-action", When: `true`, Action: "explode"},
		{ID: "escape", When: `true`, Action: decision.Move, Target: "../../etc"},
		{ID: "abs", When: `true`, Action: decision.Move, Target: "/etc"},
		{ID: "ignore-target", When: `true`, Action: decision.Ignore, Target: "x"},
		{ID: "", When: `true`, Action: decision.Ignore},
		{ID: "bad id!", When: `true`, Action: decision.Ignore},
		spec("good", `Ext == ".pdf"`),
		spec("good", `true`), // duplicate id
	}
	e := rules.New(specs)

	issues := e.Issues()
	if len(issues) != len(specs)-1 {
		t.Fatalf("got %d issues, want %d: %v", len(issues), len(specs)-1, issues)
	}
	wantIDs := []string{"syntax", "unknown-field", "not-bool", "bad-action", "escape", "abs", "ignore-target", "", "bad id!", "good"}
	for i, is := range issues {
		if is.ID != wantIDs[i] {
			t.Errorf("issue %d: ID = %q, want %q (%v)", i, is.ID, wantIDs[i], is)
		}
		if is.Err == nil || is.Error() == "" {
			t.Errorf("issue %d has no message", i)
		}
	}
	if issues[9].Index != 10 {
		t.Errorf("duplicate reported at index %d, want 10", issues[9].Index)
	}
	if !strings.Contains(issues[0].Error(), `"syntax"`) {
		t.Errorf("error should name the rule: %v", issues[0])
	}

	if e.Len() != 1 {
		t.Fatalf("Len = %d, want 1", e.Len())
	}
	d, err := e.Decide(context.Background(), invoiceFacts())
	if err != nil || d.Source != "rule:good" {
		t.Fatalf("good rule should still work: %+v, %v", d, err)
	}
}

func TestNew_TypeMismatchCaughtAtCompileTime(t *testing.T) {
	for _, when := range []string{
		`Size == "big"`,
		`Ext > 3`,
		`Snippet + 1 == 2`,
		`EXIF == "x"`,
	} {
		if issues := rules.Validate([]rules.Spec{spec("m", when)}); len(issues) != 1 {
			t.Errorf("%q: want 1 issue, got %v", when, issues)
		}
	}
}

func TestDecide_RuntimeErrorIsNotAMatchAndDoesNotStopOtherRules(t *testing.T) {
	var mu sync.Mutex
	failed := map[string]error{}
	hook := rules.WithEvalErrorHook(func(id string, err error) {
		mu.Lock()
		defer mu.Unlock()
		failed[id] = err
	})
	specs := []rules.Spec{
		spec("div-by-zero", `Size % 0 == 0`), // compiles, fails when run
		spec("fine", `Ext == ".pdf"`),
	}
	e := rules.New(specs, hook)
	if len(e.Issues()) != 0 {
		t.Fatalf("rule should compile: %v", e.Issues())
	}

	d, err := e.Decide(context.Background(), invoiceFacts())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Source != "rule:fine" {
		t.Errorf("Source = %q, want rule:fine", d.Source)
	}
	mu.Lock()
	defer mu.Unlock()
	if failed["div-by-zero"] == nil {
		t.Error("hook was not told about the failing rule")
	}
}

func TestEvalErrorsDoNotLeakFileContent(t *testing.T) {
	secret := "TOP-SECRET-SNIPPET"
	var got error
	e := rules.New([]rules.Spec{spec("boom", `Size % 0 == 0 && Snippet contains "x"`)},
		rules.WithEvalErrorHook(func(_ string, err error) { got = err }))
	f := invoiceFacts()
	f.Snippet = secret
	_, _ = e.Decide(context.Background(), f)
	if got == nil {
		t.Fatal("expected an evaluation error")
	}
	if strings.Contains(got.Error(), secret) {
		t.Errorf("error leaks content: %v", got)
	}
}

func TestDecide_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rules.New([]rules.Spec{spec("any", `true`)}).Decide(ctx, invoiceFacts())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestExpressionRestrictions(t *testing.T) {
	tests := []struct {
		name string
		when string
		ok   bool
	}{
		{"contains", `Snippet contains "x"`, true},
		{"literal regex", `Snippet matches "^a+$"`, true},
		{"predicate over collection", `any(split(RelPath, "/"), # == "x")`, true},
		{"time method", `Modified.Year() > 2000`, true},
		{"now and duration", `now().Sub(Modified) > duration("1h")`, true},

		{"range", `all(1..1000000000, # > 0)`, false},
		{"range outside predicate", `len(1..9999999) > 0`, false},
		{"nested predicates", `any(split(Snippet, " "), any(split(RelPath, "/"), # == #))`, false},
		{"dynamic regex", `Snippet matches Path`, false},
		{"huge regex", `Snippet matches "` + strings.Repeat("a", 201) + `"`, false},
		{"repeat builtin", `len(repeat("a", 100000000)) > 0`, false},
		{"toJSON builtin", `toJSON(EXIF) != ""`, false},
		{"fromBase64 builtin", `fromBase64(Snippet) != ""`, false},
		{"map builtin", `len(map(Vector, #)) > 0`, false},
		{"unknown function", `readFile(Path) == ""`, false},
		{"os access", `Path.Open() == nil`, false},
		{"too long", `Ext == ".a"` + strings.Repeat(` || Ext == ".a"`, 100), false},
		{"too many nodes", strings.Repeat(`1 + `, 150) + `1 > 0`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := rules.Validate([]rules.Spec{spec("r", tt.when)})
			if tt.ok && len(issues) != 0 {
				t.Errorf("want valid, got %v", issues)
			}
			if !tt.ok && len(issues) != 1 {
				t.Errorf("want rejected, got %v", issues)
			}
		})
	}
}

func TestDecide_RulesSeeFactsFields(t *testing.T) {
	f := invoiceFacts()
	f.Vector = []float32{1, 2, 3}
	tests := []string{
		`Path == "/root/inbox/a.pdf"`,
		`RelPath == "inbox/a.pdf"`,
		`MIME startsWith "application/"`,
		`Size > 1000 && Size < 4096`,
		`EXIF["Make"] == "Apple"`,
		`"Model" not in EXIF`,
		`len(Vector) == 3`,
		`Modified.Year() == 2025`,
		`lower(Snippet) contains "invoice"`,
	}
	for _, when := range tests {
		d, err := rules.New([]rules.Spec{spec("f", when)}).Decide(context.Background(), f)
		if err != nil || d.Source != "rule:f" {
			t.Errorf("%s: got %+v, %v", when, d, err)
		}
	}
}

func TestDecide_DoesNotMutateFacts(t *testing.T) {
	f := invoiceFacts()
	f.Vector = []float32{1, 2}
	before := f.EXIF["Make"]
	_, _ = rules.New([]rules.Spec{spec("r", `EXIF["Make"] == "Apple" && len(Vector) == 2`)}).Decide(context.Background(), f)
	if f.EXIF["Make"] != before || len(f.Vector) != 2 || f.Snippet != "Invoice no. 42" {
		t.Error("facts were modified")
	}
}

func TestDecide_ConcurrentWithApply(t *testing.T) {
	e := rules.New([]rules.Spec{spec("a", `Ext == ".pdf"`)})
	other := []rules.Spec{spec("b", `Ext == ".pdf"`), spec("c", `true`)}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				d, err := e.Decide(context.Background(), invoiceFacts())
				if err != nil {
					t.Errorf("Decide: %v", err)
					return
				}
				if d.Source != "rule:a" && d.Source != "rule:b" {
					t.Errorf("Source = %q", d.Source)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			e.Apply(other)
		} else {
			e.Apply([]rules.Spec{spec("a", `Ext == ".pdf"`)})
		}
	}
	close(stop)
	wg.Wait()
}

func TestEngineIsADecisionEngineInAChain(t *testing.T) {
	e := rules.New([]rules.Spec{spec("pdf", `Ext == ".pdf"`)})
	c, err := decision.NewChain(decision.Step{Name: "rules", Engine: e, MinConfidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Decide(context.Background(), invoiceFacts())
	if err != nil || d.Action != decision.Move {
		t.Fatalf("got %+v, %v", d, err)
	}
	d, err = c.Decide(context.Background(), decision.Facts{Ext: ".txt"})
	if err != nil || d.Action != decision.AskUser {
		t.Fatalf("no match should fall through to ask_user, got %+v, %v", d, err)
	}
}
