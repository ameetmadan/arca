package config

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/parser"
)

// fakeEmbed maps text to a vector by the first keyword it contains, and
// records every text it was asked to embed.
type fakeEmbed struct {
	seen []string
}

func (e *fakeEmbed) Embed(_ context.Context, text string) ([]float32, error) {
	e.seen = append(e.seen, text)
	switch {
	case strings.Contains(text, "alpha"):
		return []float32{1, 0, 0}, nil
	case strings.Contains(text, "beta"):
		return []float32{0, 1, 0}, nil
	}
	return []float32{0, 0, 1}, nil
}

// fakeLLM replies with a fixed string.
type fakeLLM struct{ reply string }

func (l fakeLLM) GenerateSync(context.Context, string) (string, error) { return l.reply, nil }
func (l fakeLLM) Generate(context.Context, string) (io.Reader, error) {
	return strings.NewReader(l.reply), nil
}

func parseDecisionYAML(t *testing.T, y string) DecisionConfig {
	t.Helper()
	var f File
	if err := yaml.Unmarshal([]byte(y), &f); err != nil {
		t.Fatal(err)
	}
	return f.Decision
}

func fullDeps(t *testing.T) decisionDeps {
	t.Helper()
	return decisionDeps{
		LLM:    fakeLLM{reply: `{"folder":"Inbox","new":false}`},
		Embed:  &fakeEmbed{},
		Parser: parser.NewMultiParser(),
		AppDir: t.TempDir(),
	}
}

type stepInfo struct {
	Name string
	Min  float64
}

func stepsOf(w *decisionWiring) []stepInfo {
	var out []stepInfo
	for _, s := range w.Steps {
		out = append(out, stepInfo{s.Name, s.MinConfidence})
	}
	return out
}

func TestWireDecisionDefaultsWhenSectionAbsent(t *testing.T) {
	cfg := &File{}
	w, err := wireDecision(cfg, fullDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []stepInfo{{"rules", 1.0}, {"semantic", 0.7}, {"llm", 0.5}}
	if got := stepsOf(w); !reflect.DeepEqual(got, want) {
		t.Errorf("default chain = %+v, want %+v", got, want)
	}
	if w.Engine == nil {
		t.Error("Engine is nil")
	}
	if w.Rules == nil {
		t.Error("Rules engine is nil although the chain starts with rules")
	}
}

func TestWireDecisionEmptySectionMeansDefaults(t *testing.T) {
	for _, y := range []string{"decision: {}\n", "decision:\n  engines: []\n"} {
		cfg := &File{Decision: parseDecisionYAML(t, y)}
		w, err := wireDecision(cfg, fullDeps(t))
		if err != nil {
			t.Fatalf("%q: %v", y, err)
		}
		if got := len(w.Steps); got != 3 {
			t.Errorf("%q: %d steps, want the 3 defaults", y, got)
		}
	}
}

func TestWireDecisionCustomOrderAndThresholds(t *testing.T) {
	cfg := &File{Decision: parseDecisionYAML(t, `
decision:
  engines:
    - {type: llm, min_confidence: 0.4}
    - {type: semantic}
    - {type: rules, min_confidence: 0.9}
`)}
	w, err := wireDecision(cfg, fullDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	// An omitted min_confidence takes the engine's default; a given one wins.
	want := []stepInfo{{"llm", 0.4}, {"semantic", 0.7}, {"rules", 0.9}}
	if got := stepsOf(w); !reflect.DeepEqual(got, want) {
		t.Errorf("chain = %+v, want %+v", got, want)
	}
}

func TestWireDecisionExplicitZeroThresholdIsKept(t *testing.T) {
	cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - {type: llm, min_confidence: 0}\n")}
	w, err := wireDecision(cfg, fullDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := stepsOf(w); !reflect.DeepEqual(got, []stepInfo{{"llm", 0}}) {
		t.Errorf("chain = %+v", got)
	}
}

func TestWireDecisionRejectsBadEngineLists(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown type", "{type: jev}", `unknown engine type "jev"`},
		{"missing type", "{min_confidence: 0.5}", "type is required"},
		{"threshold above one", "{type: llm, min_confidence: 1.5}", "min_confidence"},
		{"negative threshold", "{type: llm, min_confidence: -0.1}", "min_confidence"},
		{"rules_path on another engine", "{type: llm, rules_path: /x.yaml}", "rules_path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - "+tt.yaml+"\n")}
			_, err := wireDecision(cfg, fullDeps(t))
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "decision.engines[0]") {
				t.Errorf("err = %v, want it to mention decision.engines[0] and %q", err, tt.want)
			}
		})
	}
}

func TestWireDecisionRejectsDuplicateEngine(t *testing.T) {
	cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - {type: llm}\n    - {type: llm}\n")}
	_, err := wireDecision(cfg, fullDeps(t))
	if err == nil || !strings.Contains(err.Error(), "decision.engines[1]") || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("err = %v", err)
	}
}

func TestWireDecisionMissingPieceIsAnErrorNotAPanic(t *testing.T) {
	tests := []struct {
		name string
		deps func(decisionDeps) decisionDeps
		yaml string
		want string
	}{
		{"semantic without embedder", func(d decisionDeps) decisionDeps { d.Embed = nil; return d },
			"{type: semantic}", `engine "semantic" needs an embedding provider`},
		{"llm without provider", func(d decisionDeps) decisionDeps { d.LLM = nil; return d },
			"{type: llm}", `engine "llm" needs an LLM provider`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - "+tt.yaml+"\n")}
			_, err := wireDecision(cfg, tt.deps(fullDeps(t)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}

	// The same engines are fine when the section leaves them out.
	cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - {type: rules}\n")}
	d := fullDeps(t)
	d.Embed, d.LLM = nil, nil
	if _, err := wireDecision(cfg, d); err != nil {
		t.Errorf("rules-only chain without providers: %v", err)
	}
}

func TestWireDecisionMissingRulesFileIsNotFatal(t *testing.T) {
	deps := fullDeps(t)
	cfg := &File{Decision: parseDecisionYAML(t,
		"decision:\n  engines:\n    - {type: rules, rules_path: "+filepath.Join(deps.AppDir, "nope", "rules.yaml")+"}\n")}
	w, err := wireDecision(cfg, deps)
	if err != nil {
		t.Fatalf("missing rules file: %v", err)
	}
	if w.Rules.Len() != 0 {
		t.Errorf("Rules.Len = %d, want 0", w.Rules.Len())
	}
	d, err := w.Engine.Decide(context.Background(), decision.Facts{Path: "/r/a.txt", Ext: ".txt"})
	if err != nil || d.Action != decision.AskUser {
		t.Errorf("Decide = %+v, %v; want AskUser", d, err)
	}
}

func TestWireDecisionDefaultRulesPathIsInAppDir(t *testing.T) {
	deps := fullDeps(t)
	rulesYAML := `rules:
  - id: invoices
    when: 'Ext == ".pdf"'
    action: move
    target: Finance/Invoices
`
	if err := os.WriteFile(filepath.Join(deps.AppDir, "rules.yaml"), []byte(rulesYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := wireDecision(&File{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.Engine.Decide(context.Background(), decision.Facts{Path: "/r/a.pdf", Ext: ".pdf"})
	if err != nil {
		t.Fatal(err)
	}
	// The chain keeps the engine's own Source, so the user can see which rule fired.
	if d.Action != decision.Move || d.Target != "Finance/Invoices" || d.Source != "rule:invoices" {
		t.Errorf("Decide = %+v", d)
	}
}

func TestWireDecisionCustomRulesPath(t *testing.T) {
	deps := fullDeps(t)
	custom := filepath.Join(t.TempDir(), "mine.yaml")
	rulesYAML := "rules:\n  - {id: txt, when: 'Ext == \".txt\"', action: ignore}\n"
	if err := os.WriteFile(custom, []byte(rulesYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - {type: rules, rules_path: "+custom+"}\n")}
	w, err := wireDecision(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if w.Rules.Len() != 1 {
		t.Errorf("Rules.Len = %d, want 1", w.Rules.Len())
	}
}

func TestWireDecisionMalformedRulesFileIsAnError(t *testing.T) {
	deps := fullDeps(t)
	if err := os.WriteFile(filepath.Join(deps.AppDir, "rules.yaml"), []byte("rules: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wireDecision(&File{}, deps); err == nil {
		t.Error("malformed rules file: nil error")
	}
}

func TestWireDecisionLLMUsesExistingFoldersAsCandidates(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Taxes", "Photos"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "scan.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := fullDeps(t)
	deps.LLM = fakeLLM{reply: `{"folder":"Taxes","new":false}`}
	cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - {type: llm, min_confidence: 0.5}\n")}
	cfg.Watcher.Folders = []string{root}

	w, err := wireDecision(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.Engine.Decide(context.Background(), decision.Facts{Path: file, RelPath: "scan.txt", Ext: ".txt"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != decision.Move || d.Target != "Taxes" || d.Source != "llm" {
		t.Errorf("Decide = %+v", d)
	}

	// A folder the model invents but that is not on disk is only accepted as "new".
	deps.LLM = fakeLLM{reply: `{"folder":"Elsewhere","new":false}`}
	w, err = wireDecision(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	d, err = w.Engine.Decide(context.Background(), decision.Facts{Path: file, RelPath: "scan.txt", Ext: ".txt"})
	if err != nil || d.Action != decision.AskUser {
		t.Errorf("Decide with a non-candidate folder = %+v, %v; want AskUser", d, err)
	}
}

func TestWireDecisionSemanticLearnsFromWatchedFolders(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "A", "one.txt"), "alpha alpha")
	writeFile(t, filepath.Join(root, "A", "two.txt"), "alpha again")
	writeFile(t, filepath.Join(root, "B", "one.txt"), "beta beta")
	writeFile(t, filepath.Join(root, "B", "two.txt"), "beta again")

	deps := fullDeps(t)
	cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - {type: semantic, min_confidence: 0.7}\n")}
	cfg.Watcher.Folders = []string{root}
	w, err := wireDecision(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Workers) != 1 {
		t.Fatalf("%d workers, want the semantic indexer", len(w.Workers))
	}
	w.Workers[0](context.Background()) // indexes every watched root, then returns

	incoming := filepath.Join(root, "new.txt")
	d, err := w.Engine.Decide(context.Background(), decision.Facts{Path: incoming, Vector: []float32{1, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != decision.Move || d.Target != "A" || d.Source != "semantic" {
		t.Errorf("Decide = %+v, want a semantic move to A", d)
	}
}

func TestWireDecisionIndexerWorkerStopsOnCancel(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "A", "one.txt"), "alpha")
	cfg := &File{Decision: parseDecisionYAML(t, "decision:\n  engines:\n    - {type: semantic}\n")}
	cfg.Watcher.Folders = []string{root, "relative/is/skipped"}
	w, err := wireDecision(cfg, fullDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Workers[0](ctx) // must return rather than block or panic
}

// The folder index and the Facts builder must describe a file with the same
// text, or their vectors are not comparable (internal/config wires both).
func TestSemanticIndexerAndFactsBuilderEmbedTheSameText(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("alpha é words ", 400) // longer than any limit, with multi-byte runes at the cut
	files := map[string]string{
		"short.txt":  "alpha short note",
		"long.txt":   long,
		"binary.bin": "\x00\x01\x02 not parseable",
	}
	for name, body := range files {
		writeFile(t, filepath.Join(root, "A", name), body)
	}

	p := parser.NewMultiParser()
	src := newSemanticSource(p)
	embed := &fakeEmbed{}
	fb := &pipeline.FactsBuilder{Parser: p, Embed: embed, Roots: []string{root}, SnippetLen: pipeline.DefaultSnippetLen}

	for name := range files {
		embed.seen = nil
		if _, err := fb.Build(context.Background(), filepath.Join(root, "A", name)); err != nil {
			t.Fatal(err)
		}
		if len(embed.seen) != 1 {
			t.Fatalf("%s: Facts builder embedded %d texts, want 1", name, len(embed.seen))
		}
		got, err := src.Text(context.Background(), root, "A", name)
		if err != nil {
			t.Fatal(err)
		}
		if got != embed.seen[0] {
			t.Errorf("%s: folder indexer text differs from Facts builder text\nindexer: %q\nfacts:   %q", name, got, embed.seen[0])
		}
	}
}

func TestLoadWithoutDecisionSectionBuildsDefaultChain(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  port: 12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	if app.Cfg.Server.Port != 12345 {
		t.Errorf("port = %d", app.Cfg.Server.Port)
	}
	if app.Decision == nil {
		t.Error("App.Decision is nil")
	}
	if app.LLMProvider == nil || app.EmbedProvider == nil || app.VectorStore == nil || app.SQLEngine == nil || app.Parser == nil {
		t.Error("existing providers were not wired")
	}
}

func TestLoadRejectsUnknownEngineType(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	y := "decision:\n  engines:\n    - {type: oracle}\n"
	if err := os.WriteFile(cfgPath, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Load(cfgPath)
	if err == nil || !strings.Contains(err.Error(), `unknown engine type "oracle"`) {
		if app != nil {
			app.Close()
		}
		t.Fatalf("err = %v", err)
	}
	if !strings.HasPrefix(err.Error(), "config: ") {
		t.Errorf("error %q does not start with the config: prefix the other wire functions use", err)
	}
}

func TestLoadExpandsHomeInRulesPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rulesYAML := "rules:\n  - {id: r1, when: 'Ext == \".txt\"', action: ignore}\n"
	if err := os.WriteFile(filepath.Join(home, "my-rules.yaml"), []byte(rulesYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	y := "decision:\n  engines:\n    - {type: rules, rules_path: ~/my-rules.yaml}\n"
	if err := os.WriteFile(cfgPath, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if app.Rules == nil || app.Rules.Len() != 1 {
		t.Errorf("rules were not loaded from the expanded path: %+v", app.Rules)
	}
}

func TestLoadWiresExecutorAndCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if app.Executor == nil {
		t.Skip("journal unavailable in this build (no CGo)")
	}
	if _, err := os.Stat(filepath.Join(dir, "journal.db")); err != nil {
		t.Errorf("journal is not next to the config file: %v", err)
	}
	app.Close()
	app.Close()
}

func TestWireExecutorReportsWhyTheJournalIsUnavailable(t *testing.T) {
	// A directory that cannot hold a journal must not stop the server from
	// starting; Load logs the reason and /api/v1/sort/apply answers 503.
	exec, closeJournal, err := wireExecutor(filepath.Join(t.TempDir(), "missing-dir"))
	if exec != nil || err == nil {
		t.Errorf("wireExecutor = %v, %v; want a nil Executor and an error", exec, err)
	}
	closeJournal() // must be safe to call
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The semantic folder index is persisted in the app dir and released through
// the wiring's Closers, including when a later engine fails to wire.
func TestWireDecisionPersistsFolderIndex(t *testing.T) {
	deps := fullDeps(t)
	w, err := wireDecision(&File{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deps.AppDir, folderIndexFile)); err != nil {
		t.Errorf("semantic folder index not created: %v", err)
	}
	if len(w.Closers) != 1 {
		t.Fatalf("Closers = %d, want 1", len(w.Closers))
	}
	w.Closers[0]()

	// llm comes after semantic in the default chain; without an LLM it fails
	// to wire after the store was opened, and the store must be closed again.
	deps = fullDeps(t)
	deps.LLM = nil
	if _, err := wireDecision(&File{}, deps); err == nil {
		t.Fatal("wireDecision without an LLM = nil error")
	}
}
