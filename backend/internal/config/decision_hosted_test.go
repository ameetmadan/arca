package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/decision/hosted"
)

// hostedSetup returns a config with two watched folders, "in" (opted in) and
// "out", each holding a Taxes subfolder, plus a fake hosted service.
func hostedSetup(t *testing.T) (cfg *File, inFile, outFile string, hits *atomic.Int32, srv *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	in, out := filepath.Join(root, "in"), filepath.Join(root, "out")
	for _, d := range []string{in, out} {
		if err := os.MkdirAll(filepath.Join(d, "Taxes"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hits = new(atomic.Int32)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"choice":"Taxes","confidence":0.9}`))
	}))
	t.Cleanup(srv.Close)

	cfg = &File{Decision: parseDecisionYAML(t, `
decision:
  engines:
    - {type: rules}
    - type: hosted
      endpoint: `+srv.URL+`
      opt_in_folders: ["`+in+`"]
`)}
	cfg.Watcher.Folders = []string{in, out}
	return cfg, filepath.Join(in, "a.txt"), filepath.Join(out, "b.txt"), hits, srv
}

func hostedDeps(t *testing.T) decisionDeps {
	d := fullDeps(t)
	d.HostedKey = hosted.StaticKey("test-key")
	return d
}

func TestHostedIsNotInTheDefaultChain(t *testing.T) {
	w, err := wireDecision(&File{}, fullDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range w.Steps {
		if s.Name == engineHosted {
			t.Fatal("hosted engine is in the default chain")
		}
	}
	if w.Local != nil {
		t.Error("Local chain should only exist when hosted is configured")
	}
}

func TestHostedOnlyRunsForOptedInFolders(t *testing.T) {
	cfg, inFile, outFile, hits, _ := hostedSetup(t)
	w, err := wireDecision(cfg, hostedDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := stepsOf(w); !reflect.DeepEqual(got, []stepInfo{{"rules", 1.0}, {"hosted", 0.7}}) {
		t.Fatalf("steps = %+v", got)
	}
	ctx := context.Background()

	d, err := w.Engine.Decide(ctx, decision.Facts{Path: outFile, Snippet: "tax return"})
	if err != nil || d.Action != decision.AskUser {
		t.Fatalf("not opted in: d=%+v err=%v, want AskUser", d, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("%d network calls for a folder that did not opt in", hits.Load())
	}

	d, err = w.Engine.Decide(ctx, decision.Facts{Path: inFile, Snippet: "tax return"})
	if err != nil || d.Action != decision.Move || d.Target != "Taxes" || d.Source != "hosted" {
		t.Fatalf("opted in: d=%+v err=%v", d, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}

	// The local-only chain never reaches the hosted service.
	d, err = w.Local.Decide(ctx, decision.Facts{Path: inFile, Snippet: "tax return"})
	if err != nil || d.Action != decision.AskUser || hits.Load() != 1 {
		t.Fatalf("local chain: d=%+v err=%v hits=%d", d, err, hits.Load())
	}
}

func TestHostedConfigIsValidated(t *testing.T) {
	cfg, inFile, _, _, srv := hostedSetup(t)
	in := filepath.Dir(inFile)
	for name, tc := range map[string]struct{ yaml, want string }{
		"no endpoint": {"{type: hosted, opt_in_folders: [\"" + in + "\"]}", "needs an endpoint"},
		"no opt-in":   {"{type: hosted, endpoint: " + srv.URL + "}", "opt_in_folders"},
		"not watched": {"{type: hosted, endpoint: " + srv.URL + ", opt_in_folders: [/elsewhere]}", "not one of watcher.folders"},
		"relative":    {"{type: hosted, endpoint: " + srv.URL + ", opt_in_folders: [in]}", "absolute"},
		"http remote": {"{type: hosted, endpoint: \"http://example.com\", opt_in_folders: [\"" + in + "\"]}", "https"},
	} {
		c := *cfg
		c.Decision = parseDecisionYAML(t, "decision:\n  engines:\n    - "+tc.yaml+"\n")
		if _, err := wireDecision(&c, hostedDeps(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, tc.want)
		}
	}

	// hosted must be last, and its options belong to it alone.
	c := *cfg
	c.Decision = parseDecisionYAML(t, "decision:\n  engines:\n    - {type: hosted, endpoint: "+srv.URL+", opt_in_folders: [\""+in+"\"]}\n    - {type: rules}\n")
	if _, err := wireDecision(&c, hostedDeps(t)); err == nil || !strings.Contains(err.Error(), "last step") {
		t.Errorf("hosted not last: err = %v", err)
	}
	c.Decision = parseDecisionYAML(t, "decision:\n  engines:\n    - {type: rules, endpoint: \"https://x.example\"}\n")
	if _, err := wireDecision(&c, hostedDeps(t)); err == nil || !strings.Contains(err.Error(), "only apply") {
		t.Errorf("stray endpoint: err = %v", err)
	}
}
