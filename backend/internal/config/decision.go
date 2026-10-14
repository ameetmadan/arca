package config

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ameetmadan/arca/internal/filefacts"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/pkg/decision"
	llmengine "github.com/ameetmadan/arca/pkg/decision/llm"
	"github.com/ameetmadan/arca/pkg/decision/rules"
	"github.com/ameetmadan/arca/pkg/decision/semantic"
	"github.com/ameetmadan/arca/pkg/decision/semantic/sqlitestore"
	"github.com/ameetmadan/arca/pkg/embed"
	"github.com/ameetmadan/arca/pkg/llm"
	"github.com/ameetmadan/arca/pkg/parser"
)

// Engine types that can appear in decision.engines[].type. The type is also
// the engine's name in the chain and the Source of its decisions.
const (
	engineRules    = "rules"
	engineSemantic = "semantic"
	engineLLM      = "llm"
)

// Default chain thresholds (ADR-0006). Rules are exact, so only a full match
// counts. The semantic and LLM engines cap their own confidence below 1
// (0.95 and 0.6), so these never make an engine unreachable.
const (
	defaultRulesMin    = 1.0
	defaultSemanticMin = 0.7
	defaultLLMMin      = 0.5

	// rulesFile is the default rules file name inside the Arca app dir.
	rulesFile = "rules.yaml"
)

// DecisionConfig is the optional `decision:` section. When it is absent, or
// lists no engines, the chain is rules -> semantic -> llm with default thresholds.
type DecisionConfig struct {
	Engines []EngineConfig `yaml:"engines"`
	// MaxPendingPerFolder is the backpressure limit: once a watched folder
	// has more than this many decisions waiting in the queue, the watcher
	// stops generating new ones for it. Zero or omitted means the default
	// (pipeline.DefaultMaxPending, 50).
	MaxPendingPerFolder int `yaml:"max_pending_per_folder"`
}

// EngineConfig configures one step of the decision chain.
type EngineConfig struct {
	// Type is "rules", "semantic" or "llm".
	Type string `yaml:"type"`
	// MinConfidence is the lowest confidence in [0, 1] at which this engine's
	// answer is accepted. If omitted, the engine's default applies; an explicit
	// 0 is kept.
	MinConfidence *float64 `yaml:"min_confidence"`
	// RulesPath is the rules YAML file (type "rules" only). If omitted it is
	// rules.yaml in the Arca app dir. A missing file just means "no rules".
	RulesPath string `yaml:"rules_path"`
}

// decisionDeps are the already-wired pieces the engines are built from.
// Any of LLM and Embed may be nil, in which case an engine that needs it is
// reported as a configuration error.
type decisionDeps struct {
	LLM    llm.LLMProvider
	Embed  embed.EmbeddingProvider
	Parser parser.DocumentParser
	// AppDir is the Arca app dir, where rules.yaml lives by default.
	AppDir string
}

// decisionWiring is what wireDecision builds.
type decisionWiring struct {
	// Engine is the composed chain.
	Engine decision.Engine
	// Steps are the chain's steps in order, for inspection and tests.
	Steps []decision.Step
	// Rules is the rules engine in the chain, or nil if the chain has none.
	Rules *rules.Engine
	// RulesPath is the file Rules was loaded from (empty if there is no rules step).
	RulesPath string
	// Workers are background jobs to run for the lifetime of the server, such
	// as the semantic engine's folder indexer. Each returns when ctx is cancelled.
	Workers []func(ctx context.Context)
	// Closers release resources the wiring opened, such as the semantic
	// folder index. The App runs them on Close.
	Closers []func()
}

// wireDecision builds the decision chain from cfg.Decision. This is the only
// place decision engines are constructed (ADR-0004).
func wireDecision(cfg *File, deps decisionDeps) (_ *decisionWiring, err error) {
	engines := cfg.Decision.Engines
	if len(engines) == 0 {
		engines = []EngineConfig{{Type: engineRules}, {Type: engineSemantic}, {Type: engineLLM}}
	}

	w := &decisionWiring{}
	defer func() {
		// Don't leak what an engine opened if a later one fails to wire.
		if err != nil {
			for i := len(w.Closers) - 1; i >= 0; i-- {
				w.Closers[i]()
			}
		}
	}()
	seen := make(map[string]bool, len(engines))
	for i, ec := range engines {
		at := fmt.Sprintf("config: decision.engines[%d]", i)
		if ec.Type == "" {
			return nil, fmt.Errorf("%s: type is required (rules, semantic or llm)", at)
		}
		if seen[ec.Type] {
			return nil, fmt.Errorf("%s: engine %q is listed more than once", at, ec.Type)
		}
		seen[ec.Type] = true
		if ec.RulesPath != "" && ec.Type != engineRules {
			return nil, fmt.Errorf("%s: rules_path only applies to the %q engine", at, engineRules)
		}

		var (
			eng        decision.Engine
			defaultMin float64
			err        error
		)
		switch ec.Type {
		case engineRules:
			defaultMin = defaultRulesMin
			eng, err = wireRules(w, ec, deps)
		case engineSemantic:
			defaultMin = defaultSemanticMin
			eng, err = wireSemantic(w, cfg, deps)
		case engineLLM:
			defaultMin = defaultLLMMin
			eng, err = wireLLMEngine(cfg, deps)
		default:
			return nil, fmt.Errorf("%s: unknown engine type %q (want rules, semantic or llm)", at, ec.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}

		min := defaultMin
		if ec.MinConfidence != nil {
			min = *ec.MinConfidence
			if math.IsNaN(min) || min < 0 || min > 1 {
				return nil, fmt.Errorf("%s: min_confidence %v must be between 0 and 1", at, min)
			}
		}
		w.Steps = append(w.Steps, decision.Step{Name: ec.Type, Engine: eng, MinConfidence: min})
	}

	chain, err := decision.NewChain(w.Steps...)
	if err != nil {
		return nil, fmt.Errorf("config: decision chain: %w", err)
	}
	w.Engine = chain
	return w, nil
}

func wireRules(w *decisionWiring, ec EngineConfig, deps decisionDeps) (decision.Engine, error) {
	path := ec.RulesPath
	if path == "" {
		path = filepath.Join(deps.AppDir, rulesFile)
	}
	e, err := rules.Load(path, rules.WithEvalErrorHook(func(id string, err error) {
		// rules guarantees the error carries no file contents.
		log.Printf("decision: rule %q failed to evaluate: %v", id, err)
	}))
	if err != nil {
		return nil, fmt.Errorf("load rules %s: %w", path, err)
	}
	for _, issue := range e.Issues() {
		log.Printf("decision: rules: %v", issue)
	}
	w.Rules = e
	w.RulesPath = path
	return e, nil
}

func wireSemantic(w *decisionWiring, cfg *File, deps decisionDeps) (decision.Engine, error) {
	if deps.Embed == nil {
		return nil, errors.New(`engine "semantic" needs an embedding provider, but none is configured`)
	}
	store := openFolderStore(w, deps.AppDir)
	t := semantic.DefaultTunables()
	eng, err := semantic.NewEngine(store, t)
	if err != nil {
		return nil, err
	}
	idx, err := semantic.NewIndexer(store, deps.Embed, newSemanticSource(deps.Parser), t)
	if err != nil {
		return nil, err
	}

	roots := watchedRoots(cfg)
	w.Workers = append(w.Workers, func(ctx context.Context) {
		// Folders whose fingerprint is unchanged since the last run are
		// skipped, so with a persistent store this is cheap after a restart.
		// The engine reads whatever is stored so far and never waits for this.
		for _, root := range roots {
			err := idx.IndexRoot(ctx, root, nil)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				// Only the count is logged: per-folder errors name user folders.
				failed := 1
				if joined, ok := err.(interface{ Unwrap() []error }); ok {
					failed = len(joined.Unwrap())
				}
				log.Printf("decision: semantic index of %s incomplete (%d error(s))", root, failed)
			}
		}
	})
	return eng, nil
}

// folderIndexFile is the semantic folder index's file name inside the Arca app dir.
const folderIndexFile = "semantic.db"

// openFolderStore returns the semantic engine's folder store. It prefers a
// SQLite file in dir so the index survives restarts, and falls back to memory
// (rebuilt in the background on every start) if dir is unset or SQLite is
// unavailable, for example in a build without CGo.
func openFolderStore(w *decisionWiring, dir string) semantic.FolderStore {
	if dir == "" {
		return semantic.NewMemoryStore()
	}
	s, err := sqlitestore.Open(filepath.Join(dir, folderIndexFile))
	if err != nil {
		log.Printf("decision: semantic folder index is in-memory only: %v", err)
		return semantic.NewMemoryStore()
	}
	w.Closers = append(w.Closers, func() {
		if err := s.Close(); err != nil {
			log.Printf("decision: close semantic folder index: %v", err)
		}
	})
	return s
}

func wireLLMEngine(cfg *File, deps decisionDeps) (decision.Engine, error) {
	if deps.LLM == nil {
		return nil, errors.New(`engine "llm" needs an LLM provider, but none is configured`)
	}
	roots := watchedRoots(cfg)
	src := &semantic.OSSource{}
	return llmengine.New(llmengine.Config{
		Provider: deps.LLM,
		Candidates: llmengine.CandidateFunc(func(ctx context.Context, f decision.Facts) ([]string, error) {
			root, ok := rootOf(roots, f.Path)
			if !ok {
				return nil, nil
			}
			folders, err := src.Folders(ctx, root)
			if err != nil {
				return nil, err
			}
			// The engine drops the tail when there are too many, so keep the
			// shallow folders, which are the broadest choices.
			sort.SliceStable(folders, func(i, j int) bool {
				return strings.Count(folders[i], "/") < strings.Count(folders[j], "/")
			})
			return folders, nil
		}),
	})
}

// newSemanticSource returns the folder source for the semantic indexer. Its
// text for a file comes from filefacts.Text with the Facts builder's default
// snippet length, the same function and limit pipeline.FactsBuilder uses for
// Facts.Vector, so file and folder vectors are comparable.
func newSemanticSource(p parser.DocumentParser) *semantic.OSSource {
	return &semantic.OSSource{
		Parser:       p,
		MaxTextBytes: pipeline.DefaultSnippetLen,
		Describe: func(path string, maxBytes int) string {
			return filefacts.Text(p, path, maxBytes)
		},
	}
}

// watchedRoots returns the clean absolute watcher folders; the engines work
// per root and reject anything else.
func watchedRoots(cfg *File) []string {
	var roots []string
	for _, f := range cfg.Watcher.Folders {
		if !filepath.IsAbs(f) {
			log.Printf("decision: ignoring watcher folder %q: not an absolute path", f)
			continue
		}
		roots = append(roots, filepath.Clean(f))
	}
	return roots
}

// rootOf returns the root that contains path.
func rootOf(roots []string, path string) (string, bool) {
	for _, r := range roots {
		rel, err := filepath.Rel(r, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return r, true
		}
	}
	return "", false
}
