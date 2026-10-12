package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision/rules"
)

// The rules shown in the package documentation are the user-facing contract,
// so they are extracted from doc.go and loaded for real.
func TestDocExamplesCompile(t *testing.T) {
	src, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	var block []string
	for _, line := range strings.Split(string(src), "\n") {
		if rest, ok := strings.CutPrefix(line, "//\t"); ok {
			block = append(block, rest)
		} else if line == "//" && len(block) > 0 {
			block = append(block, "")
		}
	}
	if len(block) == 0 {
		t.Fatal("no code block found in doc.go")
	}

	path := t.TempDir() + "/rules.yaml"
	if err := os.WriteFile(path, []byte(strings.Join(block, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := rules.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := e.Issues(); len(issues) != 0 {
		t.Fatalf("documented rules have problems: %v", issues)
	}
	if e.Len() < 5 {
		t.Errorf("documentation must show 5+ examples, found %d", e.Len())
	}
}
