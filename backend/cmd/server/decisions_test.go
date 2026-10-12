package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/decisions/decisionstest"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/pkg/decision"
)

// decEnv wires the real router, a real Executor (in-memory journal, temp
// directories) and the in-memory decision store, so no CGo is needed.
type decEnv struct {
	t       *testing.T
	srv     *server
	handler http.Handler
	store   *decisionstest.MemStore
	svc     *decisions.Service
	root    string
}

type apiReply struct {
	status int
	data   map[string]any
	err    *string
	raw    string
}

func newDecEnv(t *testing.T) *decEnv {
	t.Helper()
	st := decisionstest.NewMemStore()
	svc := decisions.NewService(decisions.Deps{
		Queue: st, Policies: st, Stats: st, Exec: executor.New(&applyJournal{}),
		Now: func() time.Time { return time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC) },
	})
	s := &server{decisions: svc}
	_, root := applyDirs(t)
	return &decEnv{
		t: t, srv: s, store: st, svc: svc, root: root,
		handler: s.routesWith(appLogger, newRateLimiter(10000, 10000)),
	}
}

func (e *decEnv) do(method, path, body string) apiReply {
	e.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)

	var env struct {
		Data  map[string]any `json:"data"`
		Error *string        `json:"error"`
		Meta  struct {
			Timestamp string `json:"timestamp"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("%s %s: body is not an envelope: %q (%v)", method, path, rec.Body.String(), err)
	}
	if env.Meta.Timestamp == "" {
		e.t.Fatalf("%s %s: envelope has no meta.timestamp", method, path)
	}
	return apiReply{status: rec.Code, data: env.Data, err: env.Error, raw: rec.Body.String()}
}

func (r apiReply) wantStatus(t *testing.T, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d; body %s", r.status, want, r.raw)
	}
}

func (r apiReply) wantError(t *testing.T, status int, contains string) {
	t.Helper()
	r.wantStatus(t, status)
	if r.err == nil || !strings.Contains(*r.err, contains) {
		t.Fatalf("error = %v, want it to contain %q", r.err, contains)
	}
}

// submit queues a suggestion for file (created with content) and returns its id.
func (e *decEnv) submit(file, content, target string) int64 {
	e.t.Helper()
	if content != "" {
		writeFile(e.t, filepath.Join(e.root, file), content)
	}
	res, err := e.svc.Submit(context.Background(), e.root, file, decision.Decision{
		Action: decision.Move, Target: target, Confidence: 0.8, Reason: "test", Source: "semantic",
	})
	if err != nil || res.Record == nil {
		e.t.Fatalf("Submit: %+v %v", res, err)
	}
	return res.Record.ID
}

func (e *decEnv) folderID() string {
	return base64.RawURLEncoding.EncodeToString([]byte(e.root))
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func num(t *testing.T, v any) int64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%v (%T) is not a number", v, v)
	}
	return int64(f)
}

func TestDecisionsList_EmptyAndEnvelope(t *testing.T) {
	e := newDecEnv(t)
	r := e.do("GET", "/api/v1/decisions", "")
	r.wantStatus(t, 200)
	if r.err != nil {
		t.Fatalf("error = %q, want null", *r.err)
	}
	items, ok := r.data["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("items = %#v, want an empty array (not null)", r.data["items"])
	}
	if num(t, r.data["total"]) != 0 || num(t, r.data["limit"]) != decisions.DefaultLimit || num(t, r.data["offset"]) != 0 {
		t.Fatalf("paging fields = %v", r.data)
	}
}

func TestDecisionsList_FilterAndPaginate(t *testing.T) {
	e := newDecEnv(t)
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, e.submit(fmt.Sprintf("f%d.txt", i), "x", "Docs"))
	}
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/reject", ids[0]), "").wantStatus(t, 200)

	r := e.do("GET", "/api/v1/decisions?status=pending&limit=2&offset=1", "")
	r.wantStatus(t, 200)
	items := r.data["items"].([]any)
	if num(t, r.data["total"]) != 4 || len(items) != 2 || num(t, r.data["limit"]) != 2 || num(t, r.data["offset"]) != 1 {
		t.Fatalf("page = %s", r.raw)
	}
	first := items[0].(map[string]any)
	if num(t, first["id"]) != ids[3] || first["status"] != "pending" || first["file"] != "f3.txt" ||
		first["target"] != "Docs" || first["source"] != "semantic" || first["journal_id"] != nil {
		t.Fatalf("item = %v (newest first expected)", first)
	}

	r = e.do("GET", "/api/v1/decisions?status=rejected", "")
	if num(t, r.data["total"]) != 1 {
		t.Fatalf("rejected = %s", r.raw)
	}
	r = e.do("GET", "/api/v1/decisions", "")
	if num(t, r.data["total"]) != 5 {
		t.Fatalf("all = %s", r.raw)
	}
}

func TestDecisionsList_RejectsBadQuery(t *testing.T) {
	e := newDecEnv(t)
	for q, msg := range map[string]string{
		"status=bogus": "status must be one of",
		"limit=abc":    "limit must be an integer",
		"limit=-1":     "limit must be between",
		"limit=201":    "limit must be between",
		"offset=-3":    "offset must not be negative",
		"offset=1.5":   "offset must be an integer",
	} {
		e.do("GET", "/api/v1/decisions?"+q, "").wantError(t, 400, msg)
	}
	e.do("GET", "/api/v1/decisions?limit=200", "").wantStatus(t, 200)
}

func TestDecisionAccept_MovesFileThroughExecutorThenUndo(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("a.pdf", "A", "Invoices")

	r := e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "")
	r.wantStatus(t, 200)
	if r.data["status"] != "accepted" || r.data["journal_id"] == nil {
		t.Fatalf("response = %s", r.raw)
	}
	if exists(filepath.Join(e.root, "a.pdf")) || fileContent(t, filepath.Join(e.root, "Invoices", "a.pdf")) != "A" {
		t.Fatal("file was not moved")
	}

	jid := num(t, r.data["journal_id"])
	e.do("POST", fmt.Sprintf("/api/v1/journal/%d/undo", jid), "").wantStatus(t, 200)
	if fileContent(t, filepath.Join(e.root, "a.pdf")) != "A" || exists(filepath.Join(e.root, "Invoices", "a.pdf")) {
		t.Fatal("undo did not restore the file")
	}
	e.do("POST", fmt.Sprintf("/api/v1/journal/%d/undo", jid), "").wantError(t, 409, "already undone")
}

func TestDecisionAccept_TargetOverride(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("a.pdf", "A", "Invoices")
	r := e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), `{"target":"Taxes/2026"}`)
	r.wantStatus(t, 200)
	if r.data["target"] != "Taxes/2026" {
		t.Fatalf("response = %s", r.raw)
	}
	if fileContent(t, filepath.Join(e.root, "Taxes", "2026", "a.pdf")) != "A" {
		t.Fatal("file not moved to the override")
	}
}

func TestDecisionAccept_ContainmentApplies(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("a.pdf", "A", "Invoices")
	parent := filepath.Dir(e.root)

	// An override that climbs out of the watched folder is refused by the Executor.
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), `{"target":"../escaped"}`).
		wantError(t, 422, "leave the watched folder")
	if exists(filepath.Join(parent, "escaped")) || !exists(filepath.Join(e.root, "a.pdf")) {
		t.Fatal("containment was bypassed")
	}
	// The decision stays pending so the user can pick another target.
	if got, _ := e.store.Get(context.Background(), id); got.Status != decisions.StatusPending {
		t.Fatalf("status = %s", got.Status)
	}

	// A queued file path that escapes is refused too (e.g. a tampered row).
	rec, err := e.store.Insert(context.Background(), decisions.Record{
		Root: e.root, File: "../outside.txt", Action: decision.Move, Target: "Docs", Status: decisions.StatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "outside.txt"), "secret")
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", rec.ID), "").wantError(t, 422, "leave the watched folder")
	if fileContent(t, filepath.Join(parent, "outside.txt")) != "secret" {
		t.Fatal("file outside the root was moved")
	}

	// Absolute overrides are a plain validation error.
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), `{"target":"/tmp/x"}`).wantError(t, 400, "relative")
}

func TestDecisionAccept_FileVanishedIsConflict(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("gone.pdf", "A", "Invoices")
	if err := os.Remove(filepath.Join(e.root, "gone.pdf")); err != nil {
		t.Fatal(err)
	}

	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "").wantError(t, 409, "moved or no longer exists")
	if got, _ := e.store.Get(context.Background(), id); got.Status != decisions.StatusFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	// It has left the pending queue, and accepting again is a clear conflict.
	r := e.do("GET", "/api/v1/decisions?status=pending", "")
	if num(t, r.data["total"]) != 0 {
		t.Fatalf("pending = %s", r.raw)
	}
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "").wantError(t, 409, "already failed")
}

func TestDecisionAccept_FileMovedByUserIsConflict(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("a.pdf", "A", "Invoices")
	// The user already filed it by hand into the suggested folder.
	if err := os.MkdirAll(filepath.Join(e.root, "Invoices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(e.root, "a.pdf"), filepath.Join(e.root, "Invoices", "a.pdf")); err != nil {
		t.Fatal(err)
	}
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "").wantError(t, 409, "moved or no longer exists")
}

func TestDecisionAccept_TargetBlockedByFileIsConflict(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("a.pdf", "A", "Invoices")
	writeFile(t, filepath.Join(e.root, "Invoices"), "i am a file, not a folder")

	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "").wantError(t, 409, "blocked by an existing file")
	if !exists(filepath.Join(e.root, "a.pdf")) {
		t.Fatal("file must not have moved")
	}
	// Retry with another target works: it stayed pending.
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), `{"target":"Bills"}`).wantStatus(t, 200)
}

func TestDecisionAccept_NameClashNeverOverwrites(t *testing.T) {
	e := newDecEnv(t)
	writeFile(t, filepath.Join(e.root, "Invoices", "a.pdf"), "existing")
	id := e.submit("a.pdf", "new", "Invoices")
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "").wantStatus(t, 200)
	if fileContent(t, filepath.Join(e.root, "Invoices", "a.pdf")) != "existing" ||
		fileContent(t, filepath.Join(e.root, "Invoices", "a (1).pdf")) != "new" {
		t.Fatal("existing file was clobbered or new file lost")
	}
}

func TestDecisionAccept_ErrorsForBadRequests(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("a.pdf", "A", "Invoices")
	path := fmt.Sprintf("/api/v1/decisions/%d/accept", id)

	e.do("POST", "/api/v1/decisions/9999/accept", "").wantError(t, 404, "decision not found")
	e.do("POST", "/api/v1/decisions/abc/accept", "").wantError(t, 400, "positive integer")
	e.do("POST", "/api/v1/decisions/0/accept", "").wantError(t, 400, "positive integer")
	e.do("POST", path, `{not json`).wantError(t, 400, "not valid JSON")
	e.do("POST", path, `{"target": 5}`).wantError(t, 400, "wrong type")
	e.do("POST", path, `{"tagret": "x"}`).wantError(t, 400, "unknown field")
	e.do("POST", path, `{"target": ""}`).wantError(t, 400, "must not be empty")
	e.do("POST", path, `{"target": "   "}`).wantError(t, 400, "non-blank")
	e.do("POST", path, `{} {}`).wantError(t, 400, "unexpected data")
	e.do("POST", path, `{"target":"`+strings.Repeat("a", maxJSONBody)+`"}`).wantError(t, 400, "at most")
	if !exists(filepath.Join(e.root, "a.pdf")) {
		t.Fatal("a rejected request moved the file")
	}

	e.do("POST", path, "").wantStatus(t, 200)
	e.do("POST", path, "").wantError(t, 409, "already accepted")
}

func TestDecisionAccept_AskUserNeedsTarget(t *testing.T) {
	e := newDecEnv(t)
	writeFile(t, filepath.Join(e.root, "x.bin"), "x")
	res, err := e.svc.Submit(context.Background(), e.root, "x.bin", decision.Decision{Action: decision.AskUser, Source: "chain"})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/decisions/%d/accept", res.Record.ID)
	e.do("POST", path, "").wantError(t, 400, "provide a target")
	e.do("POST", path, `{"target":"Misc"}`).wantStatus(t, 200)
	if !exists(filepath.Join(e.root, "Misc", "x.bin")) {
		t.Fatal("file not moved")
	}
}

func TestDecisionReject(t *testing.T) {
	e := newDecEnv(t)
	id := e.submit("a.pdf", "A", "Invoices")
	r := e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/reject", id), "")
	r.wantStatus(t, 200)
	if r.data["status"] != "rejected" || !exists(filepath.Join(e.root, "a.pdf")) {
		t.Fatalf("response = %s", r.raw)
	}
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/reject", id), "").wantError(t, 409, "already rejected")
	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "").wantError(t, 409, "already rejected")
	e.do("POST", "/api/v1/decisions/9999/reject", "").wantError(t, 404, "decision not found")
	e.do("POST", "/api/v1/decisions/x/reject", "").wantError(t, 400, "positive integer")
}

func TestJournalUndo_Errors(t *testing.T) {
	e := newDecEnv(t)
	e.do("POST", "/api/v1/journal/777/undo", "").wantError(t, 404, "journal entry not found")
	e.do("POST", "/api/v1/journal/-1/undo", "").wantError(t, 400, "positive integer")

	// The moved file was deleted: nothing to move back.
	id := e.submit("a.pdf", "A", "Invoices")
	r := e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "")
	jid := num(t, r.data["journal_id"])
	if err := os.Remove(filepath.Join(e.root, "Invoices", "a.pdf")); err != nil {
		t.Fatal(err)
	}
	e.do("POST", fmt.Sprintf("/api/v1/journal/%d/undo", jid), "").wantError(t, 409, "no longer exists")

	// The original location was reoccupied.
	id = e.submit("b.pdf", "B", "Invoices")
	r = e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "")
	jid = num(t, r.data["journal_id"])
	writeFile(t, filepath.Join(e.root, "b.pdf"), "someone else")
	e.do("POST", fmt.Sprintf("/api/v1/journal/%d/undo", jid), "").wantError(t, 409, "occupied")
	if fileContent(t, filepath.Join(e.root, "b.pdf")) != "someone else" {
		t.Fatal("undo overwrote a file")
	}
}

func TestFolderPolicy_DefaultsToSuggest(t *testing.T) {
	e := newDecEnv(t)
	r := e.do("GET", "/api/v1/folders/"+e.folderID()+"/policy", "")
	r.wantStatus(t, 200)
	if r.data["mode"] != "suggest" || r.data["auto_threshold"] != 0.9 || r.data["auto_eligible"] != false ||
		r.data["root"] != e.root || r.data["id"] != e.folderID() {
		t.Fatalf("policy = %s", r.raw)
	}
	stats := r.data["stats"].(map[string]any)
	if num(t, stats["decided"]) != 0 || num(t, stats["accepted"]) != 0 {
		t.Fatalf("stats = %v", stats)
	}
	req := r.data["auto_requirements"].(map[string]any)
	if num(t, req["min_decisions"]) != 20 || req["min_acceptance_rate"] != 0.9 {
		t.Fatalf("requirements = %v", req)
	}
}

func TestFolderPolicy_PutAndGet(t *testing.T) {
	e := newDecEnv(t)
	p := "/api/v1/folders/" + e.folderID() + "/policy"
	r := e.do("PUT", p, `{"mode":"off"}`)
	r.wantStatus(t, 200)
	if r.data["mode"] != "off" || r.data["auto_threshold"] != 0.9 {
		t.Fatalf("put = %s", r.raw)
	}
	r = e.do("PUT", p, `{"mode":"suggest","auto_threshold":0.75}`)
	r.wantStatus(t, 200)
	r = e.do("GET", p, "")
	if r.data["mode"] != "suggest" || r.data["auto_threshold"] != 0.75 {
		t.Fatalf("get = %s", r.raw)
	}

	// Off means decisions for the folder are dropped.
	e.do("PUT", p, `{"mode":"off"}`).wantStatus(t, 200)
	res, err := e.svc.Submit(context.Background(), e.root, "a.pdf", decision.Decision{Action: decision.Move, Target: "X", Confidence: 1})
	if err != nil || res.Outcome != policy.Drop {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestFolderPolicy_AutoMustBeEarned(t *testing.T) {
	e := newDecEnv(t)
	p := "/api/v1/folders/" + e.folderID() + "/policy"
	e.do("PUT", p, `{"mode":"auto"}`).wantError(t, 409, "at least 20")

	for i := 0; i < 20; i++ {
		st := decisions.StatusAccepted
		if i < 2 {
			st = decisions.StatusRejected
		}
		if _, err := e.store.Insert(context.Background(), decisions.Record{Root: e.root, File: "f", Action: decision.Move, Status: st}); err != nil {
			t.Fatal(err)
		}
	}
	r := e.do("PUT", p, `{"mode":"auto","auto_threshold":0.95}`)
	r.wantStatus(t, 200)
	if r.data["mode"] != "auto" || r.data["auto_eligible"] != true {
		t.Fatalf("put = %s", r.raw)
	}
	stats := r.data["stats"].(map[string]any)
	if num(t, stats["decided"]) != 20 || num(t, stats["accepted"]) != 18 || stats["acceptance_rate"] != 0.9 {
		t.Fatalf("stats = %v", stats)
	}
}

func TestFolderPolicy_Validation(t *testing.T) {
	e := newDecEnv(t)
	p := "/api/v1/folders/" + e.folderID() + "/policy"
	e.do("PUT", p, `{}`).wantError(t, 400, "mode is required")
	e.do("PUT", p, "").wantError(t, 400, "body is required")
	e.do("PUT", p, `{"mode":"turbo"}`).wantError(t, 400, "mode must be one of")
	e.do("PUT", p, `{"mode":"suggest","auto_threshold":0}`).wantError(t, 400, "auto_threshold")
	e.do("PUT", p, `{"mode":"suggest","auto_threshold":1.5}`).wantError(t, 400, "auto_threshold")
	e.do("PUT", p, `{"mode":"suggest","auto_threshold":"high"}`).wantError(t, 400, "wrong type")
	e.do("PUT", p, `{"mode":5}`).wantError(t, 400, "wrong type")
	e.do("PUT", p, `{"mode":"suggest","extra":1}`).wantError(t, 400, "unknown field")

	e.do("GET", "/api/v1/folders/!!!/policy", "").wantError(t, 400, "base64url")
	rel := base64.RawURLEncoding.EncodeToString([]byte("relative/path"))
	e.do("GET", "/api/v1/folders/"+rel+"/policy", "").wantError(t, 400, "absolute")
	e.do("PUT", "/api/v1/folders/"+rel+"/policy", `{"mode":"off"}`).wantError(t, 400, "absolute")
	traversal := base64.RawURLEncoding.EncodeToString([]byte("/a/../b"))
	e.do("GET", "/api/v1/folders/"+traversal+"/policy", "").wantError(t, 400, "clean path")
}

func TestDecisionEndpoints_UnavailableWithoutQueue(t *testing.T) {
	s := &server{}
	h := s.routesWith(appLogger, newRateLimiter(10000, 10000))
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/decisions"},
		{"POST", "/api/v1/decisions/1/accept"},
		{"POST", "/api/v1/decisions/1/reject"},
		{"POST", "/api/v1/journal/1/undo"},
		{"GET", "/api/v1/folders/L3g/policy"},
		{"PUT", "/api/v1/folders/L3g/policy"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "unavailable") {
			t.Errorf("%s %s = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestDecisionAccept_WithoutExecutorIs503(t *testing.T) {
	st := decisionstest.NewMemStore()
	svc := decisions.NewService(decisions.Deps{Queue: st, Policies: st, Stats: st})
	e := &decEnv{t: t, svc: svc, store: st}
	e.handler = (&server{decisions: svc}).routesWith(appLogger, newRateLimiter(10000, 10000))
	_, e.root = applyDirs(t)
	id := e.submit("a.pdf", "A", "Invoices")

	e.do("POST", fmt.Sprintf("/api/v1/decisions/%d/accept", id), "").wantError(t, 503, "unavailable")
	e.do("POST", "/api/v1/journal/1/undo", "").wantError(t, 503, "unavailable")
	e.do("GET", "/api/v1/decisions", "").wantStatus(t, 200) // the queue itself still works
}

func TestDecisionRoutes_AlsoUnderDeprecatedAlias(t *testing.T) {
	e := newDecEnv(t)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/decisions", nil))
	if rec.Code != 200 || rec.Header().Get("Deprecation") != "true" {
		t.Fatalf("alias = %d %v", rec.Code, rec.Header())
	}
}

func TestClassifyDecisionError_UnexpectedIs500AndGeneric(t *testing.T) {
	status, msg := classifyDecisionError(fmt.Errorf("sqlite: disk I/O error at /Users/x/secret.db"), "")
	if status != 500 || msg != "internal error" {
		t.Fatalf("got %d %q: internal details must not leak", status, msg)
	}
}
