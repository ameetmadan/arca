package rules_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/decision/rules"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_ParsesYAMLAndDecides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	writeFile(t, path, `
rules:
  - id: invoices
    when: 'Ext == ".pdf" && Snippet contains "Invoice"'
    action: move
    target: Finance/Invoices
    reason: "PDF mentioning an invoice"
`)
	e, err := rules.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, err := e.Decide(context.Background(), invoiceFacts())
	if err != nil || d.Target != "Finance/Invoices" || d.Source != "rule:invoices" || d.Confidence != 1 {
		t.Fatalf("got %+v, %v", d, err)
	}
}

func TestLoad_MissingFileMeansNoRules(t *testing.T) {
	e, err := rules.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Decide(context.Background(), invoiceFacts()); !errors.Is(err, decision.ErrNoDecision) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_BadRulesAreReportedOthersLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	writeFile(t, path, `
rules:
  - id: typo
    when: 'true'
    action: move
    taget: Oops
  - id: wrong-type
    when: [1, 2]
    action: ignore
  - just a string
  - id: bad-expr
    when: 'Ext =='
    action: ignore
  - id: good
    when: 'Ext == ".pdf"'
    action: tag
    target: pdf
`)
	e, err := rules.Load(path)
	if err != nil {
		t.Fatalf("Load must not fail for individual bad rules: %v", err)
	}
	issues := e.Issues()
	if len(issues) != 4 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].ID != "typo" || !strings.Contains(issues[0].Error(), "taget") {
		t.Errorf("typo issue: %v", issues[0])
	}
	if issues[1].ID != "wrong-type" {
		t.Errorf("wrong-type issue: %v", issues[1])
	}
	if issues[3].ID != "bad-expr" {
		t.Errorf("bad-expr issue: %v", issues[3])
	}
	d, err := e.Decide(context.Background(), invoiceFacts())
	if err != nil || d.Source != "rule:good" {
		t.Fatalf("got %+v, %v", d, err)
	}
}

func TestLoad_FileLevelErrors(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"not yaml":       "rules: [unclosed",
		"unknown top":    "rulez: []\n",
		"rules not list": "rules: nope\n",
		"too large":      "rules: []\n# " + strings.Repeat("x", rules.MaxFileSize),
		"too many rules": "rules:\n" + strings.Repeat("  - {id: a, when: 'true', action: ignore}\n", rules.MaxRules+1),
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
		writeFile(t, path, content)
		if _, err := rules.Load(path); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestReload_SwapsRulesAndKeepsOldOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	writeFile(t, path, "rules:\n  - {id: old, when: 'true', action: ignore}\n")
	e, err := rules.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	src := func() string {
		d, err := e.Decide(context.Background(), invoiceFacts())
		if err != nil {
			return err.Error()
		}
		return d.Source
	}
	if src() != "rule:old" {
		t.Fatalf("got %s", src())
	}

	writeFile(t, path, "rules:\n  - {id: new, when: 'true', action: ignore}\n")
	if err := e.Reload(path); err != nil {
		t.Fatal(err)
	}
	if src() != "rule:new" {
		t.Fatalf("after reload got %s", src())
	}

	writeFile(t, path, "rules: [broken")
	if err := e.Reload(path); err == nil {
		t.Fatal("want error for broken file")
	}
	if src() != "rule:new" {
		t.Fatalf("a failed reload must keep the previous rules, got %s", src())
	}
}

func TestReload_ConcurrentWithDecide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	writeFile(t, path, "rules:\n  - {id: a, when: 'true', action: ignore}\n")
	e, err := rules.Load(path)
	if err != nil {
		t.Fatal(err)
	}

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
					if _, err := e.Decide(context.Background(), invoiceFacts()); err != nil {
						t.Errorf("Decide: %v", err)
						return
					}
				}
			}
		}()
	}
	for i := 0; i < 30; i++ {
		id := string(rune('a' + i%2))
		writeFile(t, path, "rules:\n  - {id: "+id+", when: 'true', action: ignore}\n")
		if err := e.Reload(path); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestFileStore_SaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	var store rules.Store = rules.NewFileStore(path)
	want := []rules.Spec{
		{ID: "invoices", When: `Ext == ".pdf" && Snippet contains "Invoice: 'x'"`, Action: decision.Move, Target: "Finance/Invoices", Reason: "PDF: invoice"},
		{ID: "skip", When: `Ext == ".part"`, Action: decision.Ignore},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rules", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rule %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestFileStore_SaveRefusesInvalidRulesAndKeepsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	store := rules.NewFileStore(path)
	if err := store.Save([]rules.Spec{spec("keep", `true`)}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	err := store.Save([]rules.Spec{spec("ok", `true`), spec("bad", `Ext ==`)})
	var verr *rules.ValidationError
	if !errors.As(err, &verr) || len(verr.Issues) != 1 || verr.Issues[0].ID != "bad" {
		t.Fatalf("err = %v, want ValidationError about bad", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("file changed although Save failed")
	}
}

func TestFileStore_SaveFailsWhenDirectoryMissing(t *testing.T) {
	store := rules.NewFileStore(filepath.Join(t.TempDir(), "missing", "rules.yaml"))
	if err := store.Save([]rules.Spec{spec("a", `true`)}); err == nil {
		t.Fatal("want error")
	}
}

func TestFileStore_Validate(t *testing.T) {
	store := rules.NewFileStore("unused")
	if got := store.Validate([]rules.Spec{spec("a", `true`)}); got != nil {
		t.Errorf("valid rules reported: %v", got)
	}
	if got := store.Validate([]rules.Spec{spec("a", `1 +`)}); len(got) != 1 {
		t.Errorf("want 1 issue, got %v", got)
	}
}

func TestEngineApplyAfterStoreSave(t *testing.T) {
	store := rules.NewFileStore(filepath.Join(t.TempDir(), "rules.yaml"))
	e := rules.New(nil)
	specs := []rules.Spec{spec("pdf", `Ext == ".pdf"`)}
	if err := store.Save(specs); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if issues := e.Apply(loaded); len(issues) != 0 {
		t.Fatal(issues)
	}
	if d, err := e.Decide(context.Background(), invoiceFacts()); err != nil || d.Source != "rule:pdf" {
		t.Fatalf("got %+v, %v", d, err)
	}
}
