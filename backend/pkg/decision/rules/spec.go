package rules

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ameetmadan/arca/pkg/decision"
)

// Limits applied to rule definitions. They exist because rule files are
// user-editable input and must not be able to exhaust memory or CPU.
const (
	// MaxRules is the largest number of rules a rule file may contain.
	MaxRules = 1000
	// MaxFileSize is the largest rule file, in bytes, that Load will read.
	MaxFileSize = 1 << 20
	// MaxIDLen is the longest allowed rule id.
	MaxIDLen = 64
	// MaxReasonLen is the longest allowed reason, in runes.
	MaxReasonLen = 200
	// MaxTargetLen is the longest allowed target, in bytes.
	MaxTargetLen = 512
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Spec is one rule as written in the rules file.
//
// Spec is plain data; it is not compiled or checked until it is passed to
// [Validate], [New] or [Engine.Apply].
type Spec struct {
	// ID uniquely names the rule. It appears in Decision.Source as "rule:<id>".
	ID string `yaml:"id"`
	// When is an expr expression over the Facts fields that must evaluate to a
	// boolean. See the package documentation for the syntax.
	When string `yaml:"when"`
	// Action is what to do when the rule matches: move, tag, index, ignore or
	// ask_user.
	Action decision.Action `yaml:"action"`
	// Target is the destination folder (move, relative to the watched root) or
	// the tag (tag). It must be empty for the other actions.
	Target string `yaml:"target,omitempty"`
	// Reason explains the decision to the user. It defaults to
	// "matched rule <id>". It must not contain file contents.
	Reason string `yaml:"reason,omitempty"`

	// loadErr records a problem found while decoding this rule from YAML (an
	// unknown key or a value of the wrong type), so that it is reported with
	// the rule's id without failing the other rules in the file.
	loadErr error
}

// RuleError reports that one rule is invalid, was skipped, or failed.
type RuleError struct {
	// Index is the zero-based position of the rule in the file.
	Index int
	// ID is the rule's id; it may be empty if the id itself is the problem.
	ID string
	// Err describes the problem. It never contains file contents.
	Err error
}

// Error implements error.
func (e RuleError) Error() string {
	if e.ID == "" {
		return fmt.Sprintf("rules: rule #%d: %v", e.Index+1, e.Err)
	}
	return fmt.Sprintf("rules: rule %q (#%d): %v", e.ID, e.Index+1, e.Err)
}

// Unwrap returns the underlying error.
func (e RuleError) Unwrap() error { return e.Err }

// ValidationError is returned by Store.Save when it refuses to write rules
// because some of them are invalid.
type ValidationError struct {
	// Issues lists every invalid rule.
	Issues []RuleError
}

// Error implements error.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "rules: validation failed"
	}
	msg := e.Issues[0].Error()
	if len(e.Issues) > 1 {
		msg += fmt.Sprintf(" (and %d more)", len(e.Issues)-1)
	}
	return msg
}

// Validate checks every spec without building an engine: ids, actions,
// targets, reasons and the compile-time limits on expressions. It returns one
// RuleError per invalid rule, in file order, and nil if all rules are valid.
func Validate(specs []Spec) []RuleError {
	_, issues := compileAll(specs)
	return issues
}

func validateFields(s Spec) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	if s.ID == "" {
		return errors.New("id is required")
	}
	if len(s.ID) > MaxIDLen || !idPattern.MatchString(s.ID) {
		return fmt.Errorf("id must match %s and be at most %d characters", idPattern, MaxIDLen)
	}
	if !s.Action.Valid() {
		return fmt.Errorf("unknown action %q", s.Action)
	}
	if err := validateTarget(s.Action, s.Target); err != nil {
		return err
	}
	return validateReason(s.Reason)
}

func validateTarget(a decision.Action, target string) error {
	switch a {
	case decision.Move:
		return validateFolder(target)
	case decision.Tag:
		if strings.TrimSpace(target) == "" {
			return errors.New("target (the tag) is required for action tag")
		}
		if len(target) > MaxTargetLen || hasControl(target) {
			return errors.New("target is too long or contains control characters")
		}
	default:
		if target != "" {
			return fmt.Errorf("target must be empty for action %s", a)
		}
	}
	return nil
}

// validateFolder accepts only a relative folder path that cannot climb out of
// the watched root. The Executor re-checks containment before touching any
// file; this is the first line of defence.
func validateFolder(target string) error {
	if target == "" {
		return errors.New("target (the folder) is required for action move")
	}
	if len(target) > MaxTargetLen || hasControl(target) {
		return errors.New("target is too long or contains control characters")
	}
	if strings.ContainsRune(target, '\\') {
		return errors.New(`target must use "/" as the separator`)
	}
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "~") {
		return errors.New("target must be a path relative to the watched folder")
	}
	for _, seg := range strings.Split(target, "/") {
		if seg == ".." {
			return errors.New(`target must not contain ".."`)
		}
	}
	return nil
}

func validateReason(r string) error {
	if utf8.RuneCountInString(r) > MaxReasonLen {
		return fmt.Errorf("reason is longer than %d characters", MaxReasonLen)
	}
	if hasControl(r) {
		return errors.New("reason contains control characters")
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}
