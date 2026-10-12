// Package llmengine implements the "llm" decision engine (engine 3 in
// ADR-0006): for files that rules and semantic matching could not place, it
// asks the local LLM to pick one folder from a fixed list, or to propose one
// new folder name.
//
// The package lives in backend/pkg/decision/llm but is named llmengine so it
// does not collide with the provider interface package pkg/llm, which it
// imports.
//
// File snippets are untrusted text that ends up inside a prompt, so the
// prompt-side delimiting is only best effort. The real defence is validating
// the model's reply: the folder must be one of the candidates, or a new name
// that is sanitised into a single safe path component.
package llmengine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/llm"
)

const (
	// DefaultMaxConfidence caps the confidence this engine reports. A small
	// local model's self-reported certainty is not calibrated, so the cap is
	// deliberately modest.
	DefaultMaxConfidence = 0.6
	// DefaultSnippetLen is the number of runes of snippet put in the prompt.
	// It mirrors the batch sorter's snippet length (sortSnippetLen, which is
	// unexported in internal/sorter).
	DefaultSnippetLen = 200
	// DefaultTimeout bounds the whole Decide call, including the retry.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxCandidates bounds how many folders are shown to the model.
	DefaultMaxCandidates = 100
)

// CandidateSource supplies the folders the model may choose from. It keeps the
// engine free of filesystem access: production code can back it with the
// folder index, tests with a fixed slice.
type CandidateSource interface {
	// Candidates returns the existing folder names (or paths relative to the
	// root) that are valid targets for the file described by f. Order them by
	// relevance: if there are more than Config.MaxCandidates, the tail is dropped.
	Candidates(ctx context.Context, f decision.Facts) ([]string, error)
}

// CandidateFunc adapts a function to CandidateSource.
type CandidateFunc func(ctx context.Context, f decision.Facts) ([]string, error)

// Candidates implements CandidateSource.
func (fn CandidateFunc) Candidates(ctx context.Context, f decision.Facts) ([]string, error) {
	return fn(ctx, f)
}

// StaticCandidates is a CandidateSource that always returns the same folders.
type StaticCandidates []string

// Candidates implements CandidateSource.
func (s StaticCandidates) Candidates(context.Context, decision.Facts) ([]string, error) {
	return s, nil
}

// Config configures an Engine. Provider and Candidates are required; zero
// values for the other fields select the defaults.
type Config struct {
	// Provider is the local LLM. Wired in internal/config (ADR-0004).
	Provider llm.LLMProvider
	// Candidates supplies the folder list the model chooses from.
	Candidates CandidateSource
	// MaxConfidence caps Decision.Confidence, in (0, 1]. Default 0.6.
	MaxConfidence float64
	// SnippetLen is the maximum number of snippet runes sent to the model. Default 200.
	SnippetLen int
	// Timeout bounds one Decide call, retry included. It is applied on top of
	// any earlier deadline already on ctx. Default 30s.
	Timeout time.Duration
	// MaxCandidates bounds the folder list in the prompt. Default 100.
	MaxCandidates int
}

// Engine is a decision.Engine backed by a local LLM. It is stateless and safe
// for concurrent use if the Provider and CandidateSource are.
type Engine struct {
	cfg Config
}

var _ decision.Engine = (*Engine)(nil)

// New validates cfg, applies defaults and returns an Engine.
func New(cfg Config) (*Engine, error) {
	if cfg.Provider == nil {
		return nil, errors.New("llmengine: nil Provider")
	}
	if cfg.Candidates == nil {
		return nil, errors.New("llmengine: nil Candidates")
	}
	if cfg.MaxConfidence == 0 {
		cfg.MaxConfidence = DefaultMaxConfidence
	}
	if math.IsNaN(cfg.MaxConfidence) || cfg.MaxConfidence < 0 || cfg.MaxConfidence > 1 {
		return nil, fmt.Errorf("llmengine: MaxConfidence %v outside (0, 1]", cfg.MaxConfidence)
	}
	if cfg.SnippetLen < 0 || cfg.Timeout < 0 || cfg.MaxCandidates < 0 {
		return nil, errors.New("llmengine: SnippetLen, Timeout and MaxCandidates must not be negative")
	}
	if cfg.SnippetLen == 0 {
		cfg.SnippetLen = DefaultSnippetLen
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxCandidates == 0 {
		cfg.MaxCandidates = DefaultMaxCandidates
	}
	return &Engine{cfg: cfg}, nil
}

// maxAttempts is the first try plus one retry with a stricter prompt.
const maxAttempts = 2

// Decide asks the LLM where to put the file and returns a Move decision.
//
// It returns decision.ErrNoDecision (possibly wrapped) when the model's reply
// cannot be turned into a valid folder after one retry, or when the engine's
// own timeout expires. If the caller's ctx is cancelled or times out, Decide
// returns that context error instead. Provider and candidate-source failures
// are returned as ordinary errors.
func (e *Engine) Decide(ctx context.Context, f decision.Facts) (decision.Decision, error) {
	if err := ctx.Err(); err != nil {
		return decision.Decision{}, err
	}

	candidates, err := e.cfg.Candidates.Candidates(ctx, f)
	if err != nil {
		return decision.Decision{}, fmt.Errorf("llmengine: list candidate folders: %w", err)
	}
	candidates = normalizeCandidates(candidates, e.cfg.MaxCandidates)

	callCtx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()

	name := filepath.Base(f.Path)
	if f.Path == "" {
		name = filepath.Base(f.RelPath)
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		prompt := buildPrompt(f, name, candidates, e.cfg.SnippetLen, attempt > 1)
		raw, err := e.cfg.Provider.GenerateSync(callCtx, prompt)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return decision.Decision{}, cerr
			}
			if callCtx.Err() != nil {
				return decision.Decision{}, fmt.Errorf("%w: llm call exceeded %s", decision.ErrNoDecision, e.cfg.Timeout)
			}
			return decision.Decision{}, fmt.Errorf("llmengine: generate: %w", err)
		}
		if cerr := ctx.Err(); cerr != nil {
			return decision.Decision{}, cerr
		}

		if d, ok := e.validate(raw, candidates); ok {
			return d, nil
		}
	}
	return decision.Decision{}, fmt.Errorf("%w: llm gave no valid folder after %d attempts", decision.ErrNoDecision, maxAttempts)
}

// validate turns a raw model reply into a decision, or reports false if the
// reply is malformed, names a folder that is not a candidate, or proposes a
// new name that sanitises to nothing.
func (e *Engine) validate(raw string, candidates []string) (decision.Decision, bool) {
	a, err := parseAnswer(raw)
	if err != nil {
		return decision.Decision{}, false
	}

	if !a.New {
		folder, ok := matchCandidate(a.Folder, candidates)
		if !ok {
			return decision.Decision{}, false
		}
		return e.move(folder, "local LLM chose an existing folder"), true
	}

	name, ok := sanitizeNewName(a.Folder)
	if !ok {
		return decision.Decision{}, false
	}
	// A "new" name that is really an existing folder must not create a duplicate.
	if folder, ok := matchCandidate(name, candidates); ok {
		return e.move(folder, "local LLM chose an existing folder"), true
	}
	return e.move(name, "local LLM proposed a new folder"), true
}

func (e *Engine) move(target, reason string) decision.Decision {
	return decision.Decision{
		Action:     decision.Move,
		Target:     target,
		Confidence: e.cfg.MaxConfidence,
		Reason:     reason,
		Source:     "llm",
	}
}

// matchCandidate returns the canonical candidate for name: an exact match, or
// a case-insensitive match if that is unambiguous.
func matchCandidate(name string, candidates []string) (string, bool) {
	for _, c := range candidates {
		if c == name {
			return c, true
		}
	}
	found := ""
	n := 0
	for _, c := range candidates {
		if strings.EqualFold(c, name) {
			found = c
			n++
		}
	}
	return found, n == 1
}

// normalizeCandidates trims, drops empty and duplicate entries, and applies the cap.
func normalizeCandidates(in []string, limit int) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
		if len(out) == limit {
			break
		}
	}
	return out
}
