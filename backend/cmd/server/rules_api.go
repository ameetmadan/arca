package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/go-chi/chi/v5"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/decision/rules"
)

// Request-size limits. They are checked before anything is parsed, so an
// oversize body costs one bounded read. The rule-level limits (expression
// length, rule count, ...) live in package rules and are reported by GET
// /api/v1/rules so clients need not hard-code them.
const (
	maxRuleBody  = 16 << 10  // one rule
	maxOrderBody = 128 << 10 // a list of up to rules.MaxRules ids
	maxTestBody  = 512 << 10 // one rule plus one Facts

	maxFactsSnippet = 32 << 10 // bytes
	maxFactsVector  = 4096     // elements (embeddings are a few hundred)
	maxFactsEXIF    = 64       // entries
	maxFactsString  = 4096     // bytes: path, rel_path, mime, EXIF keys and values
)

// reservedRuleIDs are path segments below /rules that have their own route,
// so a rule with one of these ids could not be addressed by PUT/DELETE.
var reservedRuleIDs = map[string]struct{}{
	"order": {}, "validate": {}, "test": {}, "suggestions": {},
}

// defaultRulesPath is where the user's rules live: rules.yaml next to
// config.yaml in the app directory.
func defaultRulesPath() string {
	return filepath.Join(filepath.Dir(defaultConfigPath()), "rules.yaml")
}

// ruleReloader is the live rules engine, seen from the API. *rules.Engine
// implements it. After every successful edit the API hands the new rule list
// to Apply, which swaps it in atomically (hot reload).
type ruleReloader interface {
	Apply(specs []rules.Spec) []rules.RuleError
}

// rulesAPI serves /api/v1/rules. It depends on the rules.Store and
// ruleReloader interfaces only, so tests use fakes.
type rulesAPI struct {
	store    rules.Store
	reloader ruleReloader // nil: edits are stored but not applied until restart

	// mu serialises read-modify-write cycles (load, change, save, reload) so
	// two concurrent edits cannot overwrite each other. The store makes each
	// write atomic; this makes the sequence of them consistent.
	mu sync.Mutex
}

// newRulesAPI returns the handlers for store. reloader may be nil, in which
// case responses report "reloaded": false.
func newRulesAPI(store rules.Store, reloader ruleReloader) *rulesAPI {
	return &rulesAPI{store: store, reloader: reloader}
}

// rulesRoutes registers /rules on r (the /api/v1 router). The rules API has no
// unversioned alias.
func (s *server) rulesRoutes(r chi.Router) {
	api := s.rules
	r.Route("/rules", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if api == nil || api.store == nil {
					apiResponse(w, http.StatusServiceUnavailable, nil, "rules are unavailable")
					return
				}
				next.ServeHTTP(w, req)
			})
		})
		r.Get("/", func(w http.ResponseWriter, req *http.Request) { api.handleList(w, req) })
		r.Post("/", func(w http.ResponseWriter, req *http.Request) { api.handleCreate(w, req) })
		r.Get("/suggestions", func(w http.ResponseWriter, req *http.Request) {
			if s.app != nil && s.app.Feedback != nil {
				threshold := s.app.Cfg.Decision.SuggestionThreshold
				suggestions, err := s.app.Feedback.Suggestions(req.Context(), threshold)
				if err != nil {
					apiResponse(w, 500, nil, "could not load suggestions")
					return
				}
				apiResponse(w, 200, map[string]any{"suggestions": suggestions}, "")
			} else {
				api.handleSuggestions(w, req)
			}
		})
		r.Post("/validate", func(w http.ResponseWriter, req *http.Request) { api.handleValidate(w, req) })
		r.Post("/test", func(w http.ResponseWriter, req *http.Request) { api.handleTest(w, req) })
		r.Put("/order", func(w http.ResponseWriter, req *http.Request) { api.handleOrder(w, req) })
		r.Put("/{id}", func(w http.ResponseWriter, req *http.Request) { api.handleUpdate(w, req) })
		r.Delete("/{id}", func(w http.ResponseWriter, req *http.Request) { api.handleDelete(w, req) })
	})
}

// ---- wire types -----------------------------------------------------------

// ruleInput is a rule as sent by the client.
type ruleInput struct {
	ID     string `json:"id"`
	When   string `json:"when"`
	Action string `json:"action"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

func (in ruleInput) spec() rules.Spec {
	return rules.Spec{ID: in.ID, When: in.When, Action: decision.Action(in.Action), Target: in.Target, Reason: in.Reason}
}

// ruleView is a rule as returned to the client. Valid and Issue report
// whether the stored rule compiles; rules written by hand can be broken.
type ruleView struct {
	ID     string    `json:"id"`
	When   string    `json:"when"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Reason string    `json:"reason"`
	Valid  bool      `json:"valid"`
	Issue  *apiIssue `json:"issue"`
}

// apiIssue is one field-level problem. Index (position in the resulting rule
// list) and RuleID are set when the problem is about a rule in the list.
type apiIssue struct {
	Index   *int   `json:"index,omitempty"`
	RuleID  string `json:"rule_id,omitempty"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type issuesBody struct {
	Issues []apiIssue `json:"issues"`
}

type rulesLimits struct {
	MaxRules            int `json:"max_rules"`
	MaxExpressionLength int `json:"max_expression_length"`
	MaxExpressionNodes  int `json:"max_expression_nodes"`
	MaxIDLength         int `json:"max_id_length"`
	MaxTargetLength     int `json:"max_target_length"`
	MaxReasonLength     int `json:"max_reason_length"`
}

var currentLimits = rulesLimits{
	MaxRules:            rules.MaxRules,
	MaxExpressionLength: rules.MaxExprLen,
	MaxExpressionNodes:  rules.MaxExprNodes,
	MaxIDLength:         rules.MaxIDLen,
	MaxTargetLength:     rules.MaxTargetLen,
	MaxReasonLength:     rules.MaxReasonLen,
}

// ---- helpers --------------------------------------------------------------

// decodeBody reads one JSON value of at most limit bytes into dst. Unknown
// keys and trailing data are errors. On failure it writes the response and
// returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		if _, e := dec.Token(); !errors.Is(e, io.EOF) {
			err = errors.New("unexpected data after the JSON value")
			if e != nil {
				var mbe *http.MaxBytesError
				if errors.As(e, &mbe) {
					err = e
				}
			}
		}
	}
	if err == nil {
		return true
	}

	var (
		mbe *http.MaxBytesError
		ute *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &mbe):
		apiResponse(w, http.StatusRequestEntityTooLarge, nil, fmt.Sprintf("request body is larger than %d bytes", limit))
	case errors.As(err, &ute):
		apiResponse(w, http.StatusBadRequest, nil, fmt.Sprintf("invalid JSON: field %q has the wrong type", ute.Field))
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		apiResponse(w, http.StatusBadRequest, nil, "invalid JSON: "+strings.TrimPrefix(err.Error(), "json: "))
	default:
		apiResponse(w, http.StatusBadRequest, nil, "request body must be one valid JSON object")
	}
	return false
}

func writeIssues(w http.ResponseWriter, status int, summary string, issues []apiIssue) {
	apiResponse(w, status, issuesBody{Issues: issues}, summary)
}

// issueFromRuleError converts a rules.RuleError. idx is the index in the list
// the error refers to.
func issueFromRuleError(re rules.RuleError) apiIssue {
	idx := re.Index
	return apiIssue{Index: &idx, RuleID: re.ID, Field: fieldOf(re.Err), Message: re.Err.Error()}
}

// fieldOf names the rule field a validation error is about. The rules package
// words its messages as "<field> ..."; anything that is not about id, action,
// target or reason comes from compiling the expression.
func fieldOf(err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "duplicate id"), strings.HasPrefix(msg, "id "):
		return "id"
	case strings.HasPrefix(msg, "unknown action"), strings.HasPrefix(msg, "action "):
		return "action"
	case strings.HasPrefix(msg, "target "):
		return "target"
	case strings.HasPrefix(msg, "reason "):
		return "reason"
	case strings.HasPrefix(msg, "unknown key"), strings.HasPrefix(msg, "rule must be"):
		return ""
	}
	return "when"
}

func toIssues(res []rules.RuleError) []apiIssue {
	out := make([]apiIssue, 0, len(res))
	for _, re := range res {
		out = append(out, issueFromRuleError(re))
	}
	return out
}

func reservedIDIssue(id string) (apiIssue, bool) {
	if _, ok := reservedRuleIDs[id]; ok {
		return apiIssue{Field: "id", Message: fmt.Sprintf("id %q is reserved", id)}, true
	}
	return apiIssue{}, false
}

func viewsOf(store rules.Store, specs []rules.Spec) []ruleView {
	problems := make(map[int]apiIssue)
	for _, re := range store.Validate(specs) {
		problems[re.Index] = apiIssue{Field: fieldOf(re.Err), Message: re.Err.Error()}
	}
	out := make([]ruleView, len(specs))
	for i, s := range specs {
		v := ruleView{ID: s.ID, When: s.When, Action: string(s.Action), Target: s.Target, Reason: s.Reason, Valid: true}
		if p, bad := problems[i]; bad {
			v.Valid, v.Issue = false, &p
		}
		out[i] = v
	}
	return out
}

func findRule(specs []rules.Spec, id string) int {
	for i, s := range specs {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// failure is an early, already-described HTTP error from inside mutate.
type failure struct {
	status  int
	summary string
	issues  []apiIssue
}

func (f *failure) write(w http.ResponseWriter) {
	if f.issues != nil {
		writeIssues(w, f.status, f.summary, f.issues)
		return
	}
	apiResponse(w, f.status, nil, f.summary)
}

// mutate runs one serialised load -> change -> save -> reload cycle. change
// receives the stored rules and returns the new list, or a failure. On
// success it returns the new list and whether the live engine was updated.
func (a *rulesAPI) mutate(change func([]rules.Spec) ([]rules.Spec, *failure)) ([]rules.Spec, bool, *failure) {
	a.mu.Lock()
	defer a.mu.Unlock()

	specs, err := a.store.Load()
	if err != nil {
		log.Printf("rules api: load: %v", err)
		return nil, false, &failure{status: http.StatusInternalServerError, summary: "the rules file could not be read; fix or remove it"}
	}
	next, fail := change(specs)
	if fail != nil {
		return nil, false, fail
	}
	if err := a.store.Save(next); err != nil {
		var ve *rules.ValidationError
		if errors.As(err, &ve) {
			return nil, false, &failure{
				status:  http.StatusUnprocessableEntity,
				summary: "rules are invalid: " + ve.Error(),
				issues:  toIssues(ve.Issues),
			}
		}
		log.Printf("rules api: save: %v", err)
		return nil, false, &failure{status: http.StatusInternalServerError, summary: "the rules could not be saved"}
	}
	return next, a.reload(next), nil
}

func (a *rulesAPI) reload(specs []rules.Spec) bool {
	if a.reloader == nil {
		return false
	}
	if issues := a.reloader.Apply(specs); len(issues) > 0 {
		// Save refuses invalid rules, so this means the engine and store
		// disagree about validity. Keep serving; say so once.
		log.Printf("rules api: engine rejected %d saved rule(s)", len(issues))
	}
	return true
}

// ---- handlers -------------------------------------------------------------

// GET /api/v1/rules
func (a *rulesAPI) handleList(w http.ResponseWriter, _ *http.Request) {
	specs, err := a.store.Load()
	if err != nil {
		log.Printf("rules api: load: %v", err)
		apiResponse(w, http.StatusInternalServerError, nil, "the rules file could not be read; fix or remove it")
		return
	}
	apiResponse(w, http.StatusOK, map[string]any{
		"rules":  viewsOf(a.store, specs),
		"limits": currentLimits,
	}, "")
}

// POST /api/v1/rules
func (a *rulesAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in ruleInput
	if !decodeBody(w, r, maxRuleBody, &in) {
		return
	}
	if is, bad := reservedIDIssue(in.ID); bad {
		writeIssues(w, http.StatusUnprocessableEntity, is.Message, []apiIssue{is})
		return
	}

	next, reloaded, fail := a.mutate(func(specs []rules.Spec) ([]rules.Spec, *failure) {
		if findRule(specs, in.ID) >= 0 {
			is := apiIssue{Field: "id", Message: fmt.Sprintf("a rule with id %q already exists", in.ID)}
			return nil, &failure{status: http.StatusConflict, summary: is.Message, issues: []apiIssue{is}}
		}
		if len(specs) >= rules.MaxRules {
			is := apiIssue{Field: "rules", Message: fmt.Sprintf("rule limit reached: at most %d rules are allowed", rules.MaxRules)}
			return nil, &failure{status: http.StatusUnprocessableEntity, summary: is.Message, issues: []apiIssue{is}}
		}
		return append(specs, in.spec()), nil
	})
	if fail != nil {
		fail.write(w)
		return
	}
	apiResponse(w, http.StatusCreated, map[string]any{
		"rule":     viewsOf(a.store, next)[len(next)-1],
		"reloaded": reloaded,
	}, "")
}

// PUT /api/v1/rules/{id}
func (a *rulesAPI) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in ruleInput
	if !decodeBody(w, r, maxRuleBody, &in) {
		return
	}
	if in.ID != "" && in.ID != id {
		is := apiIssue{Field: "id", Message: "id in the body does not match the id in the URL; rules cannot be renamed"}
		writeIssues(w, http.StatusUnprocessableEntity, is.Message, []apiIssue{is})
		return
	}
	in.ID = id

	next, reloaded, fail := a.mutate(func(specs []rules.Spec) ([]rules.Spec, *failure) {
		i := findRule(specs, id)
		if i < 0 {
			return nil, &failure{status: http.StatusNotFound, summary: "rule not found"}
		}
		out := append([]rules.Spec(nil), specs...)
		out[i] = in.spec()
		return out, nil
	})
	if fail != nil {
		fail.write(w)
		return
	}
	apiResponse(w, http.StatusOK, map[string]any{
		"rule":     viewsOf(a.store, next)[findRule(next, id)],
		"reloaded": reloaded,
	}, "")
}

// DELETE /api/v1/rules/{id}
func (a *rulesAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, reloaded, fail := a.mutate(func(specs []rules.Spec) ([]rules.Spec, *failure) {
		i := findRule(specs, id)
		if i < 0 {
			return nil, &failure{status: http.StatusNotFound, summary: "rule not found"}
		}
		out := make([]rules.Spec, 0, len(specs)-1)
		out = append(out, specs[:i]...)
		return append(out, specs[i+1:]...), nil
	})
	if fail != nil {
		fail.write(w)
		return
	}
	apiResponse(w, http.StatusOK, map[string]any{"deleted": id, "reloaded": reloaded}, "")
}

// PUT /api/v1/rules/order
func (a *rulesAPI) handleOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Order []string `json:"order"`
	}
	if !decodeBody(w, r, maxOrderBody, &in) {
		return
	}

	next, reloaded, fail := a.mutate(func(specs []rules.Spec) ([]rules.Spec, *failure) {
		byID := make(map[string]rules.Spec, len(specs))
		for _, s := range specs {
			byID[s.ID] = s
		}
		var issues []apiIssue
		seen := make(map[string]bool, len(in.Order))
		out := make([]rules.Spec, 0, len(specs))
		for _, id := range in.Order {
			s, known := byID[id]
			switch {
			case !known:
				issues = append(issues, apiIssue{Field: "order", Message: fmt.Sprintf("unknown id %q", id)})
			case seen[id]:
				issues = append(issues, apiIssue{Field: "order", Message: fmt.Sprintf("duplicate id %q", id)})
			default:
				out = append(out, s)
			}
			seen[id] = true
		}
		for _, s := range specs {
			if !seen[s.ID] {
				issues = append(issues, apiIssue{Field: "order", Message: fmt.Sprintf("missing id %q: order must list every rule exactly once", s.ID)})
			}
		}
		if len(issues) > 0 {
			return nil, &failure{status: http.StatusUnprocessableEntity, summary: "order must list every existing rule id exactly once", issues: issues}
		}
		return out, nil
	})
	if fail != nil {
		fail.write(w)
		return
	}
	apiResponse(w, http.StatusOK, map[string]any{
		"rules":    viewsOf(a.store, next),
		"reloaded": reloaded,
	}, "")
}

// previewSpec builds the Spec used by /validate and /test, where id and action
// may be left out while a rule is still being typed.
func previewSpec(in ruleInput) (rules.Spec, []apiIssue) {
	var issues []apiIssue
	if in.ID != "" {
		if is, bad := reservedIDIssue(in.ID); bad {
			issues = append(issues, is)
		}
	}
	s := in.spec()
	if s.ID == "" {
		s.ID = "preview"
	}
	if s.Action == "" {
		if s.Target != "" {
			issues = append(issues, apiIssue{Field: "action", Message: "action is required when target is given"})
		}
		s.Action = decision.Ignore
	}
	for _, re := range rules.Validate([]rules.Spec{s}) {
		is := issueFromRuleError(re)
		is.Index, is.RuleID = nil, ""
		issues = append(issues, is)
	}
	return s, issues
}

// POST /api/v1/rules/validate
func (a *rulesAPI) handleValidate(w http.ResponseWriter, r *http.Request) {
	var in ruleInput
	if !decodeBody(w, r, maxRuleBody, &in) {
		return
	}
	_, issues := previewSpec(in)
	if issues == nil {
		issues = []apiIssue{}
	}
	apiResponse(w, http.StatusOK, map[string]any{"valid": len(issues) == 0, "issues": issues}, "")
}

// factsInput is decision.Facts on the wire.
type factsInput struct {
	Path     string            `json:"path"`
	RelPath  string            `json:"rel_path"`
	Ext      string            `json:"ext"`
	MIME     string            `json:"mime"`
	Size     int64             `json:"size"`
	Modified time.Time         `json:"modified"`
	EXIF     map[string]string `json:"exif"`
	Snippet  string            `json:"snippet"`
	Vector   []float32         `json:"vector"`
}

func (f factsInput) validate() []apiIssue {
	var issues []apiIssue
	bad := func(field, msg string) { issues = append(issues, apiIssue{Field: "facts." + field, Message: msg}) }

	if f.Ext != "" && (len(f.Ext) < 2 || f.Ext[0] != '.' || f.Ext != strings.ToLower(f.Ext)) {
		bad("ext", `ext must be lower case and include the leading dot, for example ".pdf"`)
	}
	if f.Size < 0 {
		bad("size", "size must not be negative")
	}
	for name, v := range map[string]string{"path": f.Path, "rel_path": f.RelPath, "mime": f.MIME} {
		if len(v) > maxFactsString {
			bad(name, fmt.Sprintf("%s is longer than %d bytes", name, maxFactsString))
		}
	}
	if len(f.Snippet) > maxFactsSnippet {
		bad("snippet", fmt.Sprintf("snippet is longer than %d bytes", maxFactsSnippet))
	}
	if len(f.Vector) > maxFactsVector {
		bad("vector", fmt.Sprintf("vector has more than %d elements", maxFactsVector))
	}
	if len(f.EXIF) > maxFactsEXIF {
		bad("exif", fmt.Sprintf("exif has more than %d entries", maxFactsEXIF))
	} else {
		for k, v := range f.EXIF {
			if len(k) > maxFactsString || len(v) > maxFactsString {
				bad("exif", fmt.Sprintf("exif keys and values must be at most %d bytes", maxFactsString))
				break
			}
		}
	}
	return issues
}

func (f factsInput) facts() decision.Facts {
	return decision.Facts{
		Path: f.Path, RelPath: f.RelPath, Ext: f.Ext, MIME: f.MIME, Size: f.Size,
		Modified: f.Modified, EXIF: f.EXIF, Snippet: f.Snippet, Vector: f.Vector,
	}
}

type decisionView struct {
	Action     string  `json:"action"`
	Target     string  `json:"target"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Source     string  `json:"source"`
}

// POST /api/v1/rules/test
//
// A dry run: the rule in the request is compiled into a throwaway engine and
// evaluated against the supplied Facts. Nothing is stored and the live engine
// is not touched. The response never echoes the Facts.
func (a *rulesAPI) handleTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rule  *ruleInput `json:"rule"`
		Facts factsInput `json:"facts"`
	}
	if !decodeBody(w, r, maxTestBody, &in) {
		return
	}
	var issues []apiIssue
	var spec rules.Spec
	if in.Rule == nil {
		issues = append(issues, apiIssue{Field: "rule", Message: "rule is required"})
	} else {
		spec, issues = previewSpec(*in.Rule)
	}
	issues = append(issues, in.Facts.validate()...)
	if len(issues) > 0 {
		writeIssues(w, http.StatusUnprocessableEntity, "the request is invalid", issues)
		return
	}

	var evalErr error
	engine := rules.New([]rules.Spec{spec}, rules.WithEvalErrorHook(func(_ string, err error) { evalErr = err }))
	d, err := engine.Decide(r.Context(), in.Facts.facts())

	res := map[string]any{"matched": false, "decision": nil, "evaluation_error": nil}
	switch {
	case err == nil:
		res["matched"] = true
		res["reason"] = d.Reason
		res["decision"] = decisionView{
			Action: string(d.Action), Target: d.Target, Confidence: d.Confidence, Reason: d.Reason, Source: d.Source,
		}
	case errors.Is(err, decision.ErrNoDecision) && evalErr != nil:
		res["reason"] = "the rule could not be evaluated against these facts, so it does not match"
		res["evaluation_error"] = evalErr.Error()
	case errors.Is(err, decision.ErrNoDecision):
		res["reason"] = "the rule did not match these facts"
	default:
		apiResponse(w, http.StatusServiceUnavailable, nil, "the test was cancelled")
		return
	}
	apiResponse(w, http.StatusOK, res, "")
}

// GET /api/v1/rules/suggestions
//
// Placeholder: suggested rules are produced by a later feature. The shape of
// the list items is not fixed yet; clients should treat the list as opaque
// and show nothing while it is empty.
func (a *rulesAPI) handleSuggestions(w http.ResponseWriter, _ *http.Request) {
	apiResponse(w, http.StatusOK, map[string]any{"suggestions": []any{}}, "")
}

// rulesStore returns the store the rules API edits. It is the file the live
// rules engine was loaded from (so API edits and the engine agree), or the
// default rules file when the chain has no rules step.
func rulesStore(app *config.App) rules.Store {
	if app.RulesPath != "" {
		return rules.NewFileStore(app.RulesPath)
	}
	return rules.NewFileStore(defaultRulesPath())
}

// rulesReloader returns the live rules engine, or nil when there is none.
// It avoids returning a typed nil pointer inside the interface.
func rulesReloader(app *config.App) ruleReloader {
	if app.Rules == nil {
		return nil
	}
	return app.Rules
}
