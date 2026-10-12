package rules

import (
	"errors"
	"fmt"
	"path"
	"sync/atomic"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm"

	"github.com/ameetmadan/arca/pkg/decision"
)

const (
	// MaxExprLen is the longest allowed `when` expression, in bytes.
	MaxExprLen = 1000
	// MaxExprNodes is the largest number of syntax-tree nodes in one `when`
	// expression.
	MaxExprNodes = 200
	// maxMatchesPattern is the longest regular expression accepted by `matches`.
	maxMatchesPattern = 200
	// vmMemoryBudget caps the number of values an evaluation may allocate
	// (expr's own accounting unit, not bytes).
	vmMemoryBudget = 100_000
)

// allowedBuiltins is the complete set of expr builtins a rule may call. Every
// other builtin is disabled. The set is pure and cheap: no JSON, no
// base64, no string repetition, no map/filter/sort that allocate new
// collections, and nothing that touches the filesystem or network (expr has no
// such builtins; this list also keeps it that way across upgrades).
var allowedBuiltins = []string{
	// predicates over a collection (one level of nesting only, see checkTree)
	"all", "any", "none", "one", "count",
	// strings and numbers
	"len", "lower", "upper", "trim", "trimPrefix", "trimSuffix",
	"hasPrefix", "hasSuffix", "indexOf", "lastIndexOf", "split",
	"abs", "ceil", "floor", "round", "int", "float", "string", "min", "max",
	// time, for "older than" rules
	"now", "duration", "date",
}

var allowedBuiltinSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(allowedBuiltins))
	for _, n := range allowedBuiltins {
		m[n] = struct{}{}
	}
	return m
}()

// rule is a compiled Spec. The program is immutable and shared by all
// concurrent evaluations; each evaluation uses its own vm.VM.
type rule struct {
	spec    Spec
	target  string
	reason  string
	program *vm.Program

	// quarantined is set after the rule exceeded its evaluation deadline. A
	// quarantined rule is skipped until the next reload, so a runaway rule
	// costs at most a handful of abandoned goroutines rather than one per file.
	quarantined atomic.Bool
}

// compileAll validates and compiles specs in order. Invalid rules, including
// rules whose id repeats an earlier valid one, are reported and left out; the
// rest are returned in file order.
func compileAll(specs []Spec) ([]*rule, []RuleError) {
	var (
		rules  []*rule
		issues []RuleError
		seen   = make(map[string]struct{}, len(specs))
	)
	for i, s := range specs {
		r, err := compileRule(s)
		if err == nil {
			if _, dup := seen[s.ID]; dup {
				err = errors.New("duplicate id")
			}
		}
		if err != nil {
			issues = append(issues, RuleError{Index: i, ID: s.ID, Err: err})
			continue
		}
		seen[s.ID] = struct{}{}
		rules = append(rules, r)
	}
	return rules, issues
}

func compileRule(s Spec) (*rule, error) {
	if err := validateFields(s); err != nil {
		return nil, err
	}
	prog, err := compileExpr(s.When)
	if err != nil {
		return nil, err
	}
	r := &rule{spec: s, target: s.Target, reason: s.Reason, program: prog}
	if s.Action == decision.Move {
		r.target = path.Clean(s.Target)
	}
	if r.reason == "" {
		r.reason = "matched rule " + s.ID
	}
	return r, nil
}

// compileExpr compiles a `when` expression against decision.Facts with the
// restrictions described in the package documentation.
func compileExpr(src string) (prog *vm.Program, err error) {
	if src == "" {
		return nil, errors.New("when is required")
	}
	if len(src) > MaxExprLen {
		return nil, fmt.Errorf("when is longer than %d bytes", MaxExprLen)
	}

	// expr reports bad input as errors, but it is a third-party parser fed
	// with untrusted text, so a panic must not take the process down.
	defer func() {
		if r := recover(); r != nil {
			prog, err = nil, errors.New("expression could not be compiled")
		}
	}()

	guard := &treeGuard{}
	opts := []expr.Option{
		expr.Env(decision.Facts{}),
		expr.AsBool(),
		expr.MaxNodes(MaxExprNodes),
		expr.DisableAllBuiltins(),
		expr.Patch(guard),
	}
	for _, name := range allowedBuiltins {
		opts = append(opts, expr.EnableBuiltin(name))
	}
	prog, err = expr.Compile(src, opts...)
	if err != nil {
		return nil, fmt.Errorf("invalid expression: %w", err)
	}
	if guard.err != nil {
		return nil, guard.err
	}
	return prog, nil
}

// treeGuard rejects syntax that could make evaluation cost more than a small
// multiple of the size of the Facts. It runs on the parsed tree before
// type-checking.
//
//   - Ranges (a..b) are rejected: they allocate arrays whose size is chosen
//     by the rule, and are the usual way to build a long loop.
//   - A predicate (all, any, ...) may not contain another predicate, so loops
//     never nest and total work stays linear in the size of the Facts.
//   - `matches` needs a literal pattern of bounded length. A computed
//     pattern would be recompiled on every evaluation.
type treeGuard struct{ err error }

// Visit implements ast.Visitor.
func (g *treeGuard) Visit(node *ast.Node) {
	if g.err != nil {
		return
	}
	switch n := (*node).(type) {
	case *ast.BinaryNode:
		switch n.Operator {
		case "..":
			g.err = errors.New("ranges (..) are not allowed in rules")
		case "matches":
			lit, ok := n.Right.(*ast.StringNode)
			if !ok {
				g.err = errors.New("matches needs a string literal as its pattern")
			} else if len(lit.Value) > maxMatchesPattern {
				g.err = fmt.Errorf("matches pattern is longer than %d bytes", maxMatchesPattern)
			}
		}
	case *ast.BuiltinNode:
		// DisableAllBuiltins does not cover expr's collection builtins (map,
		// filter, sum, ...), which the parser recognises by itself.
		if _, ok := allowedBuiltinSet[n.Name]; !ok {
			g.err = fmt.Errorf("function %s is not available in rules", n.Name)
		}
	case *ast.PredicateNode:
		inner := &nestedPredicate{}
		ast.Walk(&n.Node, inner)
		if inner.found {
			g.err = errors.New("nested predicates (all/any/none/one/count inside another) are not allowed")
		}
	}
}

type nestedPredicate struct{ found bool }

func (v *nestedPredicate) Visit(node *ast.Node) {
	if _, ok := (*node).(*ast.PredicateNode); ok {
		v.found = true
	}
}

// eval runs the compiled program against f. It has no deadline of its own;
// the Engine wraps it in one. Errors are deliberately generic: expr's runtime
// messages can quote operands, which may be file content.
func (r *rule) eval(f decision.Facts) (bool, error) {
	machine := vm.VM{MemoryBudget: vmMemoryBudget}
	out, err := machine.Run(r.program, f)
	if err != nil {
		return false, errEvaluation
	}
	matched, ok := out.(bool)
	if !ok {
		return false, errEvaluation
	}
	return matched, nil
}

var (
	errEvaluation = errors.New("evaluation failed (type mismatch or runtime error)")
	errTimeout    = errors.New("evaluation exceeded its deadline; rule disabled until the next reload")
	errPanic      = errors.New("evaluation panicked")
)
