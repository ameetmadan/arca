package decisionbench

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision"
)

type byPath map[string]decision.Decision

func (m byPath) Decide(_ context.Context, f decision.Facts) (decision.Decision, error) {
	d, ok := m[f.Path]
	if !ok {
		return decision.Decision{}, errors.New("boom")
	}
	return d, nil
}

func facts(_ context.Context, p string) (decision.Facts, error) {
	if p == "/gone" {
		return decision.Facts{}, errors.New("deleted")
	}
	return decision.Facts{Path: p}, nil
}

func move(t string) decision.Decision { return decision.Decision{Action: decision.Move, Target: t} }

func TestRunScoresAnswers(t *testing.T) {
	eng := byPath{
		"/a": move("Taxes"),
		"/b": move("Photos"),
		"/c": {Action: decision.AskUser},
		"/d": move("Taxes"),
	}
	cases := []Case{{"/a", "Taxes"}, {"/b", "Taxes"}, {"/c", "Taxes"}, {"/d", "Taxes"}, {"/e", "Taxes"}, {"/gone", "Taxes"}}
	r, err := Run(context.Background(), "x", eng, facts, cases)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 6 || r.Correct != 2 || r.Wrong != 1 || r.Asked != 1 || r.Errors != 2 {
		t.Fatalf("result = %+v", r)
	}
	if r.Accuracy() != 2.0/6 || r.Precision() != 2.0/3 || r.Coverage() != 0.5 {
		t.Fatalf("ratios: %v %v %v", r.Accuracy(), r.Precision(), r.Coverage())
	}
	if !strings.Contains(Markdown([]Result{r}), "| x | 6 |") {
		t.Fatalf("markdown: %s", Markdown([]Result{r}))
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, "x", byPath{}, facts, []Case{{"/a", "T"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyResultHasNoNaN(t *testing.T) {
	var r Result
	if r.Accuracy() != 0 || r.Precision() != 0 || r.Coverage() != 0 || r.MeanLatency() != 0 {
		t.Fatal("empty result should score zero")
	}
}

func TestReadLabels(t *testing.T) {
	got, err := ReadLabels(strings.NewReader("# comment\n/in/a.pdf,Taxes/2025/\n\n/in/b.png,Photos\n"))
	if err != nil || len(got) != 2 || got[0] != (Case{"/in/a.pdf", "Taxes/2025"}) {
		t.Fatalf("got %+v err %v", got, err)
	}
	for _, bad := range []string{"rel/path,Taxes\n", "/a,\n", "/a\n", "/a,b,c\n"} {
		if _, err := ReadLabels(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
