// Package decisionbench scores decision engines against a labelled set of
// files: for each file the user says which folder it belongs in, and every
// engine is asked where it would put it. It exists to compare the local chain
// with the optional hosted engine (ADR-0008) before anyone turns the latter on.
//
// Reports hold only aggregate numbers. They never include paths or content.
package decisionbench

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
)

// Case is one labelled file.
type Case struct {
	// Path is the absolute path of the file.
	Path string
	// Want is the folder the file belongs in, as the engines name folders:
	// relative to the watched root, e.g. "Taxes/2025".
	Want string
}

// FactsFunc builds the Facts for a path (pipeline.FactsBuilder.Build in production).
type FactsFunc func(ctx context.Context, path string) (decision.Facts, error)

// Result is one engine's aggregate score.
type Result struct {
	Name  string
	Total int
	// Correct counts Move decisions to the labelled folder.
	Correct int
	// Wrong counts Move decisions to another folder (or other non-ask actions).
	Wrong int
	// Asked counts files left to the user (AskUser).
	Asked int
	// Errors counts engine failures.
	Errors int
	// Elapsed is the total time spent in the engine.
	Elapsed time.Duration
}

// Accuracy is Correct over Total (0 when empty).
func (r Result) Accuracy() float64 { return ratio(r.Correct, r.Total) }

// Precision is Correct over the files the engine answered (not Asked, not
// Errors): how often a confident answer is right. 0 when it answered nothing.
func (r Result) Precision() float64 { return ratio(r.Correct, r.Correct+r.Wrong) }

// Coverage is the share of files the engine answered.
func (r Result) Coverage() float64 { return ratio(r.Correct+r.Wrong, r.Total) }

// MeanLatency is the average time per file.
func (r Result) MeanLatency() time.Duration {
	if r.Total == 0 {
		return 0
	}
	return r.Elapsed / time.Duration(r.Total)
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// Run asks engine about every case and scores the answers. Facts are built
// once per call to Run. A file whose Facts cannot be built counts as an error
// for that engine. Run stops early only if ctx is cancelled.
func Run(ctx context.Context, name string, engine decision.Engine, facts FactsFunc, cases []Case) (Result, error) {
	res := Result{Name: name, Total: len(cases)}
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		f, err := facts(ctx, c.Path)
		if err != nil {
			res.Errors++
			continue
		}
		start := time.Now()
		d, err := engine.Decide(ctx, f)
		res.Elapsed += time.Since(start)
		switch {
		case err != nil && ctx.Err() != nil:
			return res, ctx.Err()
		case err != nil:
			res.Errors++
		case d.Action == decision.AskUser:
			res.Asked++
		case d.Action == decision.Move && d.Target == c.Want:
			res.Correct++
		default:
			res.Wrong++
		}
	}
	return res, nil
}

// ReadLabels parses a two-column CSV of path,folder. Blank lines and lines
// starting with # are skipped. Paths must be absolute.
func ReadLabels(r io.Reader) ([]Case, error) {
	cr := csv.NewReader(r)
	cr.Comment = '#'
	cr.FieldsPerRecord = 2
	var out []Case
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decisionbench: labels: %w", err)
		}
		path, want := strings.TrimSpace(rec[0]), strings.Trim(strings.TrimSpace(rec[1]), "/")
		if !strings.HasPrefix(path, "/") || want == "" {
			return nil, errors.New("decisionbench: labels: every row needs an absolute path and a folder")
		}
		out = append(out, Case{Path: path, Want: want})
	}
}

// Markdown renders results as a table ready to paste into an ADR.
func Markdown(results []Result) string {
	var b strings.Builder
	b.WriteString("| Engine | Files | Accuracy | Precision | Coverage | Asked | Errors | Mean latency |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| %s | %d | %.1f%% | %.1f%% | %.1f%% | %d | %d | %s |\n",
			r.Name, r.Total, 100*r.Accuracy(), 100*r.Precision(), 100*r.Coverage(),
			r.Asked, r.Errors, r.MeanLatency().Round(time.Millisecond))
	}
	return b.String()
}
