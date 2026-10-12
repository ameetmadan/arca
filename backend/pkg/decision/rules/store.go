package rules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ameetmadan/arca/pkg/decision"
)

// Store loads, saves and validates rule definitions. The HTTP layer depends on
// this interface (not on files), so it can be faked in tests.
type Store interface {
	// Load returns the stored rules in order. A store with nothing in it
	// returns no rules and a nil error. Individual rules that could not be
	// decoded are returned too and are reported by Validate.
	Load() ([]Spec, error)
	// Save replaces the stored rules. It refuses to save invalid rules and
	// returns a *ValidationError listing them, so what is stored always loads
	// completely.
	Save([]Spec) error
	// Validate reports every problem in specs without storing anything.
	Validate([]Spec) []RuleError
}

// FileStore is a [Store] backed by one YAML file. It is the only code in this
// package that touches the filesystem, and it only touches that one file (and,
// while saving, a temporary file next to it).
type FileStore struct {
	path string
	mu   sync.Mutex // serialises Save
}

var _ Store = (*FileStore)(nil)

// NewFileStore returns a store for the rules file at path. The file does not
// have to exist yet; its directory does.
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

// ruleKeys are the keys a rule may have; anything else is a typo and is reported.
var ruleKeys = map[string]struct{}{"id": {}, "when": {}, "action": {}, "target": {}, "reason": {}}

type fileDoc struct {
	Rules []yaml.Node `yaml:"rules"`
}

// Load reads and decodes the rules file. A missing or empty file means no
// rules. It returns an error if the file is unreadable, larger than
// [MaxFileSize], not valid YAML, has keys other than "rules" at the top level,
// or has more than [MaxRules] rules.
func (s *FileStore) Load() ([]Spec, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rules: open rules file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("rules: read rules file: %w", err)
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("rules: rules file is larger than %d bytes", MaxFileSize)
	}
	return parse(data)
}

func parse(data []byte) ([]Spec, error) {
	var doc fileDoc
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("rules: parse rules file: %w", err)
	}
	if len(doc.Rules) > MaxRules {
		return nil, fmt.Errorf("rules: more than %d rules", MaxRules)
	}
	specs := make([]Spec, len(doc.Rules))
	for i := range doc.Rules {
		specs[i] = decodeSpec(&doc.Rules[i])
	}
	return specs, nil
}

// decodeSpec never fails: a rule that cannot be decoded becomes a Spec that
// carries the problem, so it is reported by id without hiding its neighbours.
func decodeSpec(n *yaml.Node) Spec {
	var s Spec
	var keys map[string]yaml.Node
	if err := n.Decode(&keys); err != nil {
		s.loadErr = errors.New("rule must be a mapping with id, when, action, target and reason")
		return s
	}
	// Decode the id first so any later problem is reported against it.
	_ = decodeField(keys, "id", &s.ID)
	for k := range keys {
		if _, ok := ruleKeys[k]; !ok {
			s.loadErr = fmt.Errorf("unknown key %q", k)
			return s
		}
	}
	var action string
	fields := []struct {
		name string
		dst  *string
	}{{"id", &s.ID}, {"when", &s.When}, {"action", &action}, {"target", &s.Target}, {"reason", &s.Reason}}
	for _, f := range fields {
		if err := decodeField(keys, f.name, f.dst); err != nil {
			s.loadErr = fmt.Errorf("%s must be a string", f.name)
			return s
		}
	}
	s.Action = decision.Action(action)
	return s
}

func decodeField(keys map[string]yaml.Node, name string, dst *string) error {
	n, ok := keys[name]
	if !ok {
		return nil
	}
	return n.Decode(dst)
}

// Validate implements [Store]. It is [Validate].
func (s *FileStore) Validate(specs []Spec) []RuleError { return Validate(specs) }

// Save implements [Store]. It validates, then writes the file atomically
// (temporary file in the same directory, fsync, rename) with mode 0600, so a
// crash never leaves a half-written rules file and a concurrent Load sees
// either the old or the new file.
func (s *FileStore) Save(specs []Spec) (err error) {
	if issues := Validate(specs); len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	if len(specs) > MaxRules {
		return fmt.Errorf("rules: more than %d rules", MaxRules)
	}
	out, err := yaml.Marshal(struct {
		Rules []Spec `yaml:"rules"`
	}{Rules: specs})
	if err != nil {
		return fmt.Errorf("rules: encode rules: %w", err)
	}
	if len(out) > MaxFileSize {
		return fmt.Errorf("rules: rules file would be larger than %d bytes", MaxFileSize)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".rules-*.tmp")
	if err != nil {
		return fmt.Errorf("rules: create temporary file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("rules: set file mode: %w", err)
	}
	if _, err = tmp.Write(out); err != nil {
		return fmt.Errorf("rules: write rules: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("rules: sync rules: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("rules: close rules: %w", err)
	}
	if err = os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("rules: replace rules file: %w", err)
	}
	return nil
}
