package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/decision/rules"
)

// fakeReloader records what the API hands to the live engine.
type fakeReloader struct {
	mu    sync.Mutex
	calls int
	last  []rules.Spec
}

func (f *fakeReloader) Apply(specs []rules.Spec) []rules.RuleError {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = append([]rules.Spec(nil), specs...)
	return nil
}

// memStore is an in-memory rules.Store with injectable failures.
type memStore struct {
	mu      sync.Mutex
	specs   []rules.Spec
	loadErr error
	saveErr error
	saves   int
}

func (m *memStore) Load() ([]rules.Spec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return append([]rules.Spec(nil), m.specs...), nil
}

func (m *memStore) Save(specs []rules.Spec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	if issues := rules.Validate(specs); len(issues) > 0 {
		return &rules.ValidationError{Issues: issues}
	}
	m.saves++
	m.specs = append([]rules.Spec(nil), specs...)
	return nil
}

func (m *memStore) Validate(specs []rules.Spec) []rules.RuleError { return rules.Validate(specs) }

func rulesRouter(a *rulesAPI) http.Handler {
	r := chi.NewRouter()
	(&server{rules: a}).rulesRoutes(r)
	return r
}

type rulesResp struct {
	status int
	env    decodedEnvelope
	data   map[string]any
}

func call(t *testing.T, h http.Handler, method, path, body string) rulesResp {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	env := decodeEnvelope(t, rec.Body.Bytes())
	assertTimestamp(t, env)
	var data map[string]any
	if string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("data is not an object: %s", env.Data)
		}
	}
	return rulesResp{status: rec.Code, env: env, data: data}
}

func (r rulesResp) wantStatus(t *testing.T, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d; error=%v data=%v", r.status, want, r.env.Error, r.data)
	}
}

func (r rulesResp) wantOK(t *testing.T, want int) {
	t.Helper()
	r.wantStatus(t, want)
	if r.env.Error != nil {
		t.Fatalf("error = %q, want null", *r.env.Error)
	}
}

// wantIssue asserts that data.issues contains an issue for field whose message
// contains substr.
func (r rulesResp) wantIssue(t *testing.T, field, substr string) {
	t.Helper()
	if r.env.Error == nil {
		t.Fatalf("error is null, want a message; data=%v", r.data)
	}
	issues, _ := r.data["issues"].([]any)
	for _, raw := range issues {
		is := raw.(map[string]any)
		if is["field"] == field && strings.Contains(fmt.Sprint(is["message"]), substr) {
			return
		}
	}
	t.Fatalf("no issue for field %q containing %q in %v", field, substr, issues)
}

func (r rulesResp) firstIssue(t *testing.T) map[string]any {
	t.Helper()
	issues, _ := r.data["issues"].([]any)
	if len(issues) == 0 {
		t.Fatalf("no issues in %v", r.data)
	}
	return issues[0].(map[string]any)
}

func ruleJSONBody(id, when, action, target string) string {
	b, _ := json.Marshal(map[string]string{"id": id, "when": when, "action": action, "target": target})
	return string(b)
}

func newRulesTestAPI(t *testing.T, seed ...rules.Spec) (*rulesAPI, *memStore, *fakeReloader) {
	t.Helper()
	st := &memStore{specs: seed}
	rl := &fakeReloader{}
	return newRulesAPI(st, rl), st, rl
}

func spec(id, when string) rules.Spec {
	return rules.Spec{ID: id, When: when, Action: decision.Move, Target: "Docs/" + id}
}

func ids(t *testing.T, resp rulesResp) []string {
	t.Helper()
	list, _ := resp.data["rules"].([]any)
	out := make([]string, 0, len(list))
	for _, r := range list {
		out = append(out, r.(map[string]any)["id"].(string))
	}
	return out
}

func TestRulesList(t *testing.T) {
	api, _, _ := newRulesTestAPI(t, spec("a", `Ext == ".pdf"`), spec("b", `Ext == ".txt"`))
	resp := call(t, rulesRouter(api), http.MethodGet, "/rules", "")
	resp.wantOK(t, http.StatusOK)

	if got := strings.Join(ids(t, resp), ","); got != "a,b" {
		t.Fatalf("ids = %s, want a,b (file order)", got)
	}
	first := resp.data["rules"].([]any)[0].(map[string]any)
	for k, want := range map[string]any{"when": `Ext == ".pdf"`, "action": "move", "target": "Docs/a", "reason": "", "valid": true, "issue": nil} {
		if v, ok := first[k]; !ok || v != want {
			t.Errorf("rules[0][%q] = %v (present=%v), want %v", k, v, ok, want)
		}
	}
	limits := resp.data["limits"].(map[string]any)
	if limits["max_rules"] != float64(rules.MaxRules) || limits["max_expression_length"] != float64(rules.MaxExprLen) {
		t.Errorf("limits = %v", limits)
	}
}

func TestRulesList_EmptyIsArrayNotNull(t *testing.T) {
	api, _, _ := newRulesTestAPI(t)
	resp := call(t, rulesRouter(api), http.MethodGet, "/rules", "")
	resp.wantOK(t, http.StatusOK)
	if list, ok := resp.data["rules"].([]any); !ok || len(list) != 0 {
		t.Fatalf("rules = %#v, want []", resp.data["rules"])
	}
}

func TestRulesList_FlagsInvalidStoredRules(t *testing.T) {
	// A hand-edited file can hold a broken rule; the list must still work and
	// must say which rule is broken and why.
	api, _, _ := newRulesTestAPI(t, spec("good", `Ext == ".pdf"`), spec("bad", `Nope == 1`))
	resp := call(t, rulesRouter(api), http.MethodGet, "/rules", "")
	resp.wantOK(t, http.StatusOK)
	list := resp.data["rules"].([]any)
	if list[0].(map[string]any)["valid"] != true {
		t.Errorf("good rule reported invalid")
	}
	bad := list[1].(map[string]any)
	if bad["valid"] != false {
		t.Fatalf("bad rule reported valid")
	}
	issue := bad["issue"].(map[string]any)
	if issue["field"] != "when" || issue["message"] == "" {
		t.Errorf("issue = %v", issue)
	}
}

func TestRulesList_StoreFailure(t *testing.T) {
	api, st, _ := newRulesTestAPI(t)
	st.loadErr = errors.New("yaml: line 3: boom")
	resp := call(t, rulesRouter(api), http.MethodGet, "/rules", "")
	resp.wantStatus(t, http.StatusInternalServerError)
	if resp.env.Error == nil || strings.Contains(*resp.env.Error, "boom") {
		t.Fatalf("error = %v; internal details must not leak", resp.env.Error)
	}
}

func TestRulesCreate(t *testing.T) {
	api, st, rl := newRulesTestAPI(t, spec("a", `Ext == ".pdf"`))
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules",
		`{"id":"b","when":"Size > 10","action":"move","target":"Big","reason":"large"}`)
	resp.wantOK(t, http.StatusCreated)

	rule := resp.data["rule"].(map[string]any)
	if rule["id"] != "b" || rule["reason"] != "large" || rule["valid"] != true {
		t.Errorf("rule = %v", rule)
	}
	if resp.data["reloaded"] != true {
		t.Errorf("reloaded = %v, want true", resp.data["reloaded"])
	}
	if len(st.specs) != 2 || st.specs[1].ID != "b" {
		t.Fatalf("stored = %v, want b appended", st.specs)
	}
	if rl.calls != 1 || len(rl.last) != 2 {
		t.Fatalf("reloader calls=%d last=%v, want 1 call with 2 rules", rl.calls, rl.last)
	}
}

func TestRulesCreate_Invalid(t *testing.T) {
	long := strings.Repeat("a", rules.MaxExprLen+1)
	cases := []struct {
		name, body, field, substr string
	}{
		{"bad expression", `{"id":"x","when":"Ext ==","action":"ignore"}`, "when", "invalid expression"},
		{"unknown name in expression", `{"id":"x","when":"Nope == 1","action":"ignore"}`, "when", ""},
		{"non-boolean", `{"id":"x","when":"Size + 1","action":"ignore"}`, "when", ""},
		{"empty when", `{"id":"x","when":"","action":"ignore"}`, "when", "required"},
		{"oversize expression", `{"id":"x","when":"` + long + `","action":"ignore"}`, "when", "longer than"},
		{"missing id", `{"when":"Size > 1","action":"ignore"}`, "id", "required"},
		{"bad id chars", `{"id":"a b","when":"Size > 1","action":"ignore"}`, "id", "id must match"},
		{"reserved id", `{"id":"order","when":"Size > 1","action":"ignore"}`, "id", "reserved"},
		{"bad action", `{"id":"x","when":"Size > 1","action":"explode"}`, "action", "unknown action"},
		{"move without target", `{"id":"x","when":"Size > 1","action":"move"}`, "target", "required"},
		{"target escapes", `{"id":"x","when":"Size > 1","action":"move","target":"../etc"}`, "target", ".."},
		{"target on ignore", `{"id":"x","when":"Size > 1","action":"ignore","target":"t"}`, "target", "must be empty"},
		{"long reason", `{"id":"x","when":"Size > 1","action":"ignore","reason":"` + strings.Repeat("r", rules.MaxReasonLen+1) + `"}`, "reason", "longer than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, st, rl := newRulesTestAPI(t)
			resp := call(t, rulesRouter(api), http.MethodPost, "/rules", tc.body)
			resp.wantStatus(t, http.StatusUnprocessableEntity)
			resp.wantIssue(t, tc.field, tc.substr)
			if st.saves != 0 || rl.calls != 0 {
				t.Errorf("invalid rule was saved (%d) or reloaded (%d)", st.saves, rl.calls)
			}
		})
	}
}

func TestRulesCreate_BadBodies(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
	}{
		{"not json", `{`, http.StatusBadRequest},
		{"array", `[]`, http.StatusBadRequest},
		{"unknown key", `{"id":"x","when":"Size > 1","action":"ignore","nope":1}`, http.StatusBadRequest},
		{"trailing data", `{"id":"x","when":"Size > 1","action":"ignore"} {}`, http.StatusBadRequest},
		{"wrong type", `{"id":5}`, http.StatusBadRequest},
		{"oversize body", `{"id":"x","when":"` + strings.Repeat("a", 2*maxRuleBody) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, st, _ := newRulesTestAPI(t)
			resp := call(t, rulesRouter(api), http.MethodPost, "/rules", tc.body)
			resp.wantStatus(t, tc.status)
			if resp.env.Error == nil {
				t.Error("error is null")
			}
			if st.saves != 0 {
				t.Error("saved on bad body")
			}
		})
	}
}

func TestRulesCreate_DuplicateID(t *testing.T) {
	api, st, _ := newRulesTestAPI(t, spec("a", `Size > 1`))
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules", ruleJSONBody("a", "Size > 2", "ignore", ""))
	resp.wantStatus(t, http.StatusConflict)
	resp.wantIssue(t, "id", "already exists")
	if st.saves != 0 {
		t.Error("saved a duplicate")
	}
}

func TestRulesCreate_RuleLimit(t *testing.T) {
	seed := make([]rules.Spec, rules.MaxRules)
	for i := range seed {
		seed[i] = spec(fmt.Sprintf("r%d", i), `Size > 1`)
	}
	api, st, _ := newRulesTestAPI(t, seed...)
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules", ruleJSONBody("one-more", "Size > 2", "ignore", ""))
	resp.wantStatus(t, http.StatusUnprocessableEntity)
	resp.wantIssue(t, "rules", "limit")
	if st.saves != 0 {
		t.Error("saved beyond the limit")
	}
}

func TestRulesCreate_BlockedByInvalidNeighbour(t *testing.T) {
	// The store refuses to hold invalid rules, so while a hand-edited broken
	// rule exists the API says which one to fix instead of silently dropping it.
	api, _, _ := newRulesTestAPI(t, spec("bad", `Nope == 1`))
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules", ruleJSONBody("new", "Size > 2", "ignore", ""))
	resp.wantStatus(t, http.StatusUnprocessableEntity)
	is := resp.firstIssue(t)
	if is["rule_id"] != "bad" || is["index"] != float64(0) || is["field"] != "when" {
		t.Fatalf("issue = %v, want it to point at rule bad (index 0)", is)
	}
}

func TestRulesCreate_StoreFailure(t *testing.T) {
	api, st, rl := newRulesTestAPI(t)
	st.saveErr = errors.New("disk full /secret/path")
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules", ruleJSONBody("x", "Size > 2", "ignore", ""))
	resp.wantStatus(t, http.StatusInternalServerError)
	if strings.Contains(*resp.env.Error, "secret") {
		t.Errorf("internal error leaked: %s", *resp.env.Error)
	}
	if rl.calls != 0 {
		t.Error("reloaded after failed save")
	}
}

func TestRulesUpdate(t *testing.T) {
	api, st, rl := newRulesTestAPI(t, spec("a", `Size > 1`), spec("b", `Size > 2`))
	resp := call(t, rulesRouter(api), http.MethodPut, "/rules/a",
		`{"when":"Ext == \".md\"","action":"tag","target":"notes"}`) // id omitted: taken from the URL
	resp.wantOK(t, http.StatusOK)
	if st.specs[0].ID != "a" || st.specs[0].Action != decision.Tag || st.specs[1].ID != "b" {
		t.Fatalf("stored = %v", st.specs)
	}
	if rl.calls != 1 {
		t.Errorf("reload calls = %d", rl.calls)
	}
}

func TestRulesUpdate_Errors(t *testing.T) {
	api, st, _ := newRulesTestAPI(t, spec("a", `Size > 1`))
	h := rulesRouter(api)

	resp := call(t, h, http.MethodPut, "/rules/missing", ruleJSONBody("missing", "Size > 1", "ignore", ""))
	resp.wantStatus(t, http.StatusNotFound)

	resp = call(t, h, http.MethodPut, "/rules/a", ruleJSONBody("renamed", "Size > 1", "ignore", ""))
	resp.wantStatus(t, http.StatusUnprocessableEntity)
	resp.wantIssue(t, "id", "does not match")

	resp = call(t, h, http.MethodPut, "/rules/a", ruleJSONBody("a", "Size >", "ignore", ""))
	resp.wantStatus(t, http.StatusUnprocessableEntity)
	resp.wantIssue(t, "when", "")

	if st.saves != 0 || st.specs[0].When != "Size > 1" {
		t.Errorf("failed update changed the store: %v", st.specs)
	}
}

func TestRulesUpdate_FixesBrokenRule(t *testing.T) {
	api, st, _ := newRulesTestAPI(t, spec("bad", `Nope == 1`))
	resp := call(t, rulesRouter(api), http.MethodPut, "/rules/bad", ruleJSONBody("bad", "Size > 1", "ignore", ""))
	resp.wantOK(t, http.StatusOK)
	if st.specs[0].When != "Size > 1" {
		t.Fatalf("stored = %v", st.specs)
	}
}

func TestRulesDelete(t *testing.T) {
	api, st, rl := newRulesTestAPI(t, spec("a", `Size > 1`), spec("b", `Size > 2`))
	h := rulesRouter(api)

	resp := call(t, h, http.MethodDelete, "/rules/a", "")
	resp.wantOK(t, http.StatusOK)
	if resp.data["deleted"] != "a" || resp.data["reloaded"] != true {
		t.Errorf("data = %v", resp.data)
	}
	if len(st.specs) != 1 || st.specs[0].ID != "b" || rl.calls != 1 {
		t.Fatalf("stored = %v, reloads = %d", st.specs, rl.calls)
	}

	call(t, h, http.MethodDelete, "/rules/a", "").wantStatus(t, http.StatusNotFound)
}

func TestRulesDelete_RemovesBrokenRule(t *testing.T) {
	api, st, _ := newRulesTestAPI(t, spec("bad", `Nope == 1`), spec("ok", `Size > 1`))
	call(t, rulesRouter(api), http.MethodDelete, "/rules/bad", "").wantOK(t, http.StatusOK)
	if len(st.specs) != 1 || st.specs[0].ID != "ok" {
		t.Fatalf("stored = %v", st.specs)
	}
}

func TestRulesOrder(t *testing.T) {
	api, st, rl := newRulesTestAPI(t, spec("a", `Size > 1`), spec("b", `Size > 2`), spec("c", `Size > 3`))
	resp := call(t, rulesRouter(api), http.MethodPut, "/rules/order", `{"order":["c","a","b"]}`)
	resp.wantOK(t, http.StatusOK)
	if got := strings.Join(ids(t, resp), ","); got != "c,a,b" {
		t.Fatalf("response order = %s", got)
	}
	if st.specs[0].ID != "c" || st.specs[2].ID != "b" || rl.calls != 1 {
		t.Fatalf("stored = %v, reloads = %d", st.specs, rl.calls)
	}
}

func TestRulesOrder_Invalid(t *testing.T) {
	cases := []struct{ name, body, substr string }{
		{"missing id", `{"order":["a","b"]}`, "missing"},
		{"unknown id", `{"order":["a","b","c","zzz"]}`, "unknown"},
		{"duplicate id", `{"order":["a","a","b"]}`, "duplicate"},
		{"empty", `{"order":[]}`, "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, st, _ := newRulesTestAPI(t, spec("a", `Size > 1`), spec("b", `Size > 2`), spec("c", `Size > 3`))
			resp := call(t, rulesRouter(api), http.MethodPut, "/rules/order", tc.body)
			resp.wantStatus(t, http.StatusUnprocessableEntity)
			resp.wantIssue(t, "order", tc.substr)
			if st.saves != 0 {
				t.Error("saved an invalid order")
			}
		})
	}
	api, _, _ := newRulesTestAPI(t)
	call(t, rulesRouter(api), http.MethodPut, "/rules/order", `{"nope":1}`).wantStatus(t, http.StatusBadRequest)
}

func TestRulesValidate(t *testing.T) {
	api, st, rl := newRulesTestAPI(t)
	h := rulesRouter(api)

	// Expression only: the editor validates while the user types.
	resp := call(t, h, http.MethodPost, "/rules/validate", `{"when":"Ext == \".pdf\" && Size > 10"}`)
	resp.wantOK(t, http.StatusOK)
	if resp.data["valid"] != true || len(resp.data["issues"].([]any)) != 0 {
		t.Fatalf("data = %v", resp.data)
	}

	resp = call(t, h, http.MethodPost, "/rules/validate", `{"when":"Ext =="}`)
	resp.wantOK(t, http.StatusOK)
	if resp.data["valid"] != false {
		t.Fatalf("valid = %v", resp.data["valid"])
	}
	if f := resp.firstIssue(t)["field"]; f != "when" {
		t.Errorf("field = %v", f)
	}

	// Other fields are checked when supplied.
	resp = call(t, h, http.MethodPost, "/rules/validate", `{"when":"Size > 1","action":"move","target":"../x"}`)
	resp.wantOK(t, http.StatusOK)
	if resp.data["valid"] != false {
		t.Errorf("escaping target reported valid")
	}
	resp = call(t, h, http.MethodPost, "/rules/validate", `{"when":"Size > 1","target":"x"}`)
	resp.wantOK(t, http.StatusOK)
	if resp.data["valid"] != false {
		t.Errorf("target without action reported valid")
	}

	resp = call(t, h, http.MethodPost, "/rules/validate", `{"when":"`+strings.Repeat("a", rules.MaxExprLen+1)+`"}`)
	resp.wantOK(t, http.StatusOK)
	if resp.data["valid"] != false {
		t.Errorf("oversize expression reported valid")
	}

	call(t, h, http.MethodPost, "/rules/validate", `nope`).wantStatus(t, http.StatusBadRequest)
	if st.saves != 0 || rl.calls != 0 {
		t.Error("validate must not save or reload")
	}
}

func testBody(rule, facts string) string {
	return `{"rule":` + rule + `,"facts":` + facts + `}`
}

func TestRulesTest_Match(t *testing.T) {
	api, st, rl := newRulesTestAPI(t)
	body := testBody(
		`{"id":"inv","when":"Ext == \".pdf\" && Snippet contains \"Invoice\"","action":"move","target":"Finance","reason":"an invoice"}`,
		`{"rel_path":"scan.pdf","ext":".pdf","size":10,"snippet":"Invoice 42","modified":"2026-01-02T03:04:05Z"}`)
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules/test", body)
	resp.wantOK(t, http.StatusOK)
	if resp.data["matched"] != true || resp.data["reason"] != "an invoice" {
		t.Fatalf("data = %v", resp.data)
	}
	d := resp.data["decision"].(map[string]any)
	if d["action"] != "move" || d["target"] != "Finance" || d["confidence"] != float64(1) || d["source"] != "rule:inv" {
		t.Errorf("decision = %v", d)
	}
	if st.saves != 0 || rl.calls != 0 {
		t.Error("dry run must not save or reload")
	}
}

func TestRulesTest_NoMatch(t *testing.T) {
	api, _, _ := newRulesTestAPI(t)
	body := testBody(`{"id":"inv","when":"Ext == \".pdf\"","action":"ignore"}`, `{"ext":".txt"}`)
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules/test", body)
	resp.wantOK(t, http.StatusOK)
	if resp.data["matched"] != false || resp.data["decision"] != nil {
		t.Fatalf("data = %v", resp.data)
	}
	if r, _ := resp.data["reason"].(string); r == "" {
		t.Error("no-match must still explain itself")
	}
}

func TestRulesTest_UsesFactFields(t *testing.T) {
	api, _, _ := newRulesTestAPI(t)
	body := testBody(
		`{"id":"x","when":"\"Make\" in EXIF && EXIF[\"Make\"] == \"Apple\" && Modified.Year() == 2026 && len(Vector) == 2","action":"tag","target":"iphone"}`,
		`{"exif":{"Make":"Apple"},"modified":"2026-05-06T00:00:00Z","vector":[0.5,0.25]}`)
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules/test", body)
	resp.wantOK(t, http.StatusOK)
	if resp.data["matched"] != true {
		t.Fatalf("data = %v", resp.data)
	}
}

func TestRulesTest_EvaluationErrorIsReportedNotFatal(t *testing.T) {
	api, _, _ := newRulesTestAPI(t)
	// Compiles, but takes a modulo by zero at run time for Size == 0.
	body := testBody(`{"id":"x","when":"10 % Size == 0","action":"ignore"}`, `{"size":0}`)
	resp := call(t, rulesRouter(api), http.MethodPost, "/rules/test", body)
	resp.wantOK(t, http.StatusOK)
	if resp.data["matched"] != false {
		t.Fatalf("data = %v", resp.data)
	}
	if msg, _ := resp.data["evaluation_error"].(string); msg == "" {
		t.Errorf("evaluation_error missing: %v", resp.data)
	}
}

func TestRulesTest_Invalid(t *testing.T) {
	okRule := `{"id":"x","when":"Size > 1","action":"ignore"}`
	cases := []struct {
		name, body string
		status     int
		field      string
	}{
		{"bad rule", testBody(`{"id":"x","when":"Ext ==","action":"ignore"}`, `{}`), 422, "when"},
		{"ext without dot", testBody(okRule, `{"ext":"pdf"}`), 422, "facts.ext"},
		{"ext upper case", testBody(okRule, `{"ext":".PDF"}`), 422, "facts.ext"},
		{"negative size", testBody(okRule, `{"size":-1}`), 422, "facts.size"},
		{"bad time", testBody(okRule, `{"modified":"yesterday"}`), 400, ""},
		{"oversize snippet", testBody(okRule, `{"snippet":"`+strings.Repeat("s", maxFactsSnippet+1)+`"}`), 422, "facts.snippet"},
		{"oversize vector", testBody(okRule, `{"vector":[`+strings.TrimSuffix(strings.Repeat("0,", maxFactsVector+1), ",")+`]}`), 422, "facts.vector"},
		{"too many exif keys", testBody(okRule, exifJSON(maxFactsEXIF+1)), 422, "facts.exif"},
		{"missing rule", `{"facts":{}}`, 422, "rule"},
		{"unknown facts key", testBody(okRule, `{"bogus":1}`), 400, ""},
		{"oversize body", `{"facts":{"snippet":"` + strings.Repeat("s", 2*maxTestBody) + `"}}`, 413, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, _, _ := newRulesTestAPI(t)
			resp := call(t, rulesRouter(api), http.MethodPost, "/rules/test", tc.body)
			resp.wantStatus(t, tc.status)
			if tc.field != "" {
				resp.wantIssue(t, tc.field, "")
			}
		})
	}
}

func exifJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"exif":{`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":"v"`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

func TestRulesTest_DoesNotEchoFileContent(t *testing.T) {
	api, _, _ := newRulesTestAPI(t)
	body := testBody(`{"id":"x","when":"Snippet contains \"zzz\"","action":"ignore"}`, `{"snippet":"TOP-SECRET-TEXT"}`)
	rec := httptest.NewRecorder()
	rulesRouter(api).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/rules/test", strings.NewReader(body)))
	if strings.Contains(rec.Body.String(), "TOP-SECRET-TEXT") {
		t.Fatal("response echoes the snippet")
	}
}

func TestRulesSuggestions_EmptyForNow(t *testing.T) {
	api, _, _ := newRulesTestAPI(t)
	resp := call(t, rulesRouter(api), http.MethodGet, "/rules/suggestions", "")
	resp.wantOK(t, http.StatusOK)
	list, ok := resp.data["suggestions"].([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("suggestions = %#v, want []", resp.data["suggestions"])
	}
}

func TestRulesReservedPathsAreNotRuleIDs(t *testing.T) {
	api, _, _ := newRulesTestAPI(t)
	h := rulesRouter(api)
	// "suggestions" is a static route for GET only; DELETE falls through to
	// /rules/{id} and finds no such rule.
	call(t, h, http.MethodDelete, "/rules/suggestions", "").wantStatus(t, http.StatusNotFound)
}

func TestRules_NoReloader(t *testing.T) {
	st := &memStore{}
	resp := call(t, rulesRouter(newRulesAPI(st, nil)), http.MethodPost, "/rules", ruleJSONBody("x", "Size > 1", "ignore", ""))
	resp.wantOK(t, http.StatusCreated)
	if resp.data["reloaded"] != false {
		t.Errorf("reloaded = %v, want false when no engine is attached", resp.data["reloaded"])
	}
	if len(st.specs) != 1 {
		t.Error("rule not stored")
	}
}

func TestRules_Unavailable(t *testing.T) {
	h := rulesRouter(nil) // server built without a rules API
	call(t, h, http.MethodGet, "/rules", "").wantStatus(t, http.StatusServiceUnavailable)
	call(t, h, http.MethodPost, "/rules/validate", `{"when":"Size > 1"}`).wantStatus(t, http.StatusServiceUnavailable)
}

// TestRules_ConcurrentEditsKeepFileIntact drives the real FileStore and a real
// engine from many goroutines: no edit may be lost and the file must always
// load completely.
func TestRules_ConcurrentEditsKeepFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	store := rules.NewFileStore(path)
	engine := rules.New(nil)
	h := rulesRouter(newRulesAPI(store, engine))

	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("r%d", i)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/rules",
				strings.NewReader(ruleJSONBody(id, fmt.Sprintf("Size > %d", i), "ignore", ""))))
			if rec.Code != http.StatusCreated {
				t.Errorf("create %s: %d %s", id, rec.Code, rec.Body.String())
			}
			// Interleave reads and updates of the same file.
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rules", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("list: %d", rec.Code)
			}
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/rules/"+id,
				strings.NewReader(ruleJSONBody(id, fmt.Sprintf("Size > %d", i+1000), "ignore", ""))))
			if rec.Code != http.StatusOK {
				t.Errorf("update %s: %d %s", id, rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()

	specs, err := store.Load()
	if err != nil {
		t.Fatalf("rules file does not load: %v", err)
	}
	if len(specs) != n {
		t.Fatalf("file has %d rules, want %d (an edit was lost)", len(specs), n)
	}
	if issues := rules.Validate(specs); len(issues) != 0 {
		t.Fatalf("file has invalid rules: %v", issues)
	}
	for _, s := range specs {
		if len(s.When) != len("Size > 1000") { // every rule was updated to i+1000
			t.Errorf("rule %s has when %q, update lost", s.ID, s.When)
		}
	}
	if engine.Len() != n {
		t.Errorf("engine has %d rules, want %d", engine.Len(), n)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "rules.yaml" {
			t.Errorf("stray file left behind: %s", e.Name())
		}
	}
}

func TestRules_HotReloadReachesEngine(t *testing.T) {
	store := rules.NewFileStore(filepath.Join(t.TempDir(), "rules.yaml"))
	engine := rules.New(nil)
	h := rulesRouter(newRulesAPI(store, engine))

	call(t, h, http.MethodPost, "/rules", ruleJSONBody("pdfs", `Ext == ".pdf"`, "move", "PDFs")).wantOK(t, http.StatusCreated)
	d, err := engine.Decide(context.Background(), decision.Facts{Ext: ".pdf"})
	if err != nil || d.Target != "PDFs" {
		t.Fatalf("after create: %v, %v", d, err)
	}
	call(t, h, http.MethodDelete, "/rules/pdfs", "").wantOK(t, http.StatusOK)
	if _, err := engine.Decide(context.Background(), decision.Facts{Ext: ".pdf"}); !errors.Is(err, decision.ErrNoDecision) {
		t.Fatalf("after delete: err = %v, want ErrNoDecision", err)
	}
}

func TestV1Routes_RulesMountedOnlyUnderV1(t *testing.T) {
	s := testServer(t)
	s.rules = newRulesAPI(&memStore{}, nil)
	h := s.routes()

	if rec := get(h, "/api/v1/rules"); rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/rules = %d", rec.Code)
	}
	if rec := get(h, "/api/rules"); rec.Code != http.StatusNotFound {
		t.Fatalf("/api/rules = %d, want 404 (rules have no legacy alias)", rec.Code)
	}
}

func TestDefaultRulesPath(t *testing.T) {
	if got := filepath.Base(defaultRulesPath()); got != "rules.yaml" {
		t.Fatalf("base = %s", got)
	}
	if filepath.Dir(defaultRulesPath()) != filepath.Dir(defaultConfigPath()) {
		t.Fatal("rules.yaml must live next to config.yaml")
	}
}
