package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/policy"
)

// maxJSONBody caps request bodies of the decision endpoints.
const maxJSONBody = 64 << 10

// decisionRoutes registers the decision queue, undo and folder policy
// endpoints on r (mounted under /api/v1).
func (s *server) decisionRoutes(r chi.Router) {
	r.Get("/decisions", s.handleDecisionsList)
	r.Post("/decisions/{id}/accept", s.handleDecisionAccept)
	r.Post("/decisions/{id}/reject", s.handleDecisionReject)
	r.Post("/journal/{id}/undo", s.handleJournalUndo)
	r.Get("/folders/{id}/policy", s.handleFolderPolicyGet)
	r.Put("/folders/{id}/policy", s.handleFolderPolicyPut)
}

// decisionJSON is the wire form of a queued decision.
type decisionJSON struct {
	ID         int64     `json:"id"`
	TS         time.Time `json:"ts"`
	Root       string    `json:"root"`
	File       string    `json:"file"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	Confidence float64   `json:"confidence"`
	Reason     string    `json:"reason"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
	JournalID  *int64    `json:"journal_id"`
}

func toDecisionJSON(r decisions.Record) decisionJSON {
	return decisionJSON{
		ID: r.ID, TS: r.TS, Root: r.Root, File: r.File, Action: string(r.Action), Target: r.Target,
		Confidence: r.Confidence, Reason: r.Reason, Source: r.Source, Status: string(r.Status), JournalID: r.JournalID,
	}
}

// policyJSON is the wire form of a folder policy. The id is the folder's
// root path, base64url-encoded without padding, so a folder can be addressed
// in a URL before anything about it is stored.
type policyJSON struct {
	ID            string  `json:"id"`
	Root          string  `json:"root"`
	Mode          string  `json:"mode"`
	AutoThreshold float64 `json:"auto_threshold"`
	AutoEligible  bool    `json:"auto_eligible"`
	Stats         struct {
		Decided        int     `json:"decided"`
		Accepted       int     `json:"accepted"`
		AcceptanceRate float64 `json:"acceptance_rate"`
	} `json:"stats"`
	Requirements struct {
		MinDecisions   int     `json:"min_decisions"`
		AcceptanceRate float64 `json:"min_acceptance_rate"`
	} `json:"auto_requirements"`
}

func toPolicyJSON(v decisions.PolicyView) policyJSON {
	p := policyJSON{
		ID: encodeFolderID(v.Root), Root: v.Root, Mode: string(v.Settings.Mode),
		AutoThreshold: v.Settings.AutoThreshold, AutoEligible: v.Eligible,
	}
	p.Stats.Decided, p.Stats.Accepted, p.Stats.AcceptanceRate = v.Stats.Decided, v.Stats.Accepted, v.Stats.Rate()
	p.Requirements.MinDecisions, p.Requirements.AcceptanceRate = policy.MinDecisions, policy.MinAcceptanceRate
	return p
}

func encodeFolderID(root string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(root))
}

func decodeFolderID(id string) (string, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(id)
	if err != nil || len(b) == 0 {
		return "", fmt.Errorf("%w: folder id must be the base64url-encoded (no padding) absolute folder path", decisions.ErrInvalid)
	}
	return string(b), nil
}

// GET /api/v1/decisions?status=pending&limit=&offset=
func (s *server) handleDecisionsList(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.decisionService(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := decisions.ListFilter{Status: decisions.Status(q.Get("status"))}
	var err error
	if f.Limit, err = intParam(q.Get("limit"), "limit"); err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	if f.Offset, err = intParam(q.Get("offset"), "offset"); err != nil {
		s.decisionError(w, r, err, "")
		return
	}

	page, err := svc.List(r.Context(), f)
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	items := make([]decisionJSON, 0, len(page.Items))
	for _, rec := range page.Items {
		items = append(items, toDecisionJSON(rec))
	}
	apiResponse(w, http.StatusOK, map[string]any{
		"items": items, "total": page.Total, "limit": page.Limit, "offset": page.Offset,
	}, "")
}

// POST /api/v1/decisions/{id}/accept — body optional: {"target": "Folder"}
func (s *server) handleDecisionAccept(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.decisionService(w)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	var req struct {
		Target *string `json:"target"`
	}
	if err := decodeDecisionBody(w, r, &req, true); err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	target := ""
	if req.Target != nil {
		target = *req.Target
		if target == "" {
			s.decisionError(w, r, fmt.Errorf("%w: target must not be empty; omit it to use the suggested folder", decisions.ErrInvalid), "")
			return
		}
	}

	rec, err := svc.Accept(r.Context(), id, target)
	if err != nil {
		s.decisionError(w, r, err, "decision not found")
		return
	}
	apiResponse(w, http.StatusOK, toDecisionJSON(rec), "")
}

// POST /api/v1/decisions/{id}/reject
func (s *server) handleDecisionReject(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.decisionService(w)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	rec, err := svc.Reject(r.Context(), id)
	if err != nil {
		s.decisionError(w, r, err, "decision not found")
		return
	}
	apiResponse(w, http.StatusOK, toDecisionJSON(rec), "")
}

// POST /api/v1/journal/{id}/undo
func (s *server) handleJournalUndo(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.decisionService(w)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	if err := svc.Undo(r.Context(), id); err != nil {
		s.decisionError(w, r, err, "journal entry not found")
		return
	}
	apiResponse(w, http.StatusOK, map[string]any{"journal_id": id, "undone": true}, "")
}

// GET /api/v1/folders/{id}/policy
func (s *server) handleFolderPolicyGet(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.decisionService(w)
	if !ok {
		return
	}
	root, err := decodeFolderID(chi.URLParam(r, "id"))
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	v, err := svc.GetPolicy(r.Context(), root)
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	apiResponse(w, http.StatusOK, toPolicyJSON(v), "")
}

// PUT /api/v1/folders/{id}/policy — body: {"mode": "off|suggest|auto", "auto_threshold": 0.9}
func (s *server) handleFolderPolicyPut(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.decisionService(w)
	if !ok {
		return
	}
	root, err := decodeFolderID(chi.URLParam(r, "id"))
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	var req struct {
		Mode          *string  `json:"mode"`
		AutoThreshold *float64 `json:"auto_threshold"`
	}
	if err := decodeDecisionBody(w, r, &req, false); err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	if req.Mode == nil {
		s.decisionError(w, r, fmt.Errorf("%w: mode is required (off, suggest or auto)", decisions.ErrInvalid), "")
		return
	}
	settings := policy.Settings{Mode: policy.Mode(*req.Mode), AutoThreshold: policy.DefaultAutoThreshold}
	if req.AutoThreshold != nil {
		settings.AutoThreshold = *req.AutoThreshold
	}

	v, err := svc.PutPolicy(r.Context(), root, settings)
	if err != nil {
		s.decisionError(w, r, err, "")
		return
	}
	apiResponse(w, http.StatusOK, toPolicyJSON(v), "")
}

// decisionService returns the service, or answers 503 when the queue database
// could not be opened.
func (s *server) decisionService(w http.ResponseWriter) (*decisions.Service, bool) {
	if s.decisions == nil {
		apiResponse(w, http.StatusServiceUnavailable, nil, "decision queue unavailable")
		return nil, false
	}
	return s.decisions, true
}

// pathID parses the {id} URL parameter as a positive integer.
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%w: id must be a positive integer", decisions.ErrInvalid)
	}
	return id, nil
}

// intParam parses an optional non-negative integer query parameter; absent is 0.
func intParam(v, name string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be an integer", decisions.ErrInvalid, name)
	}
	return n, nil
}

// decodeDecisionBody reads one JSON object into dst. Unknown fields are rejected so a
// typo is reported rather than ignored. With allowEmpty a missing body is
// treated as {}.
func decodeDecisionBody(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if errors.Is(err, io.EOF) && allowEmpty {
		return nil
	}
	if err == nil {
		// Only one JSON value is allowed.
		if _, perr := dec.Token(); !errors.Is(perr, io.EOF) {
			err = errors.New("unexpected data after the JSON object")
		}
	}
	if err == nil {
		return nil
	}

	var (
		syn  *json.SyntaxError
		typ  *json.UnmarshalTypeError
		tooL *http.MaxBytesError
	)
	switch {
	case errors.As(err, &tooL):
		return fmt.Errorf("%w: request body must be at most %d bytes", decisions.ErrInvalid, maxJSONBody)
	case errors.As(err, &typ):
		return fmt.Errorf("%w: field %q has the wrong type, expected %s", decisions.ErrInvalid, typ.Field, typ.Type.String())
	case errors.As(err, &syn), errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("%w: request body is not valid JSON", decisions.ErrInvalid)
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%w: request body is required", decisions.ErrInvalid)
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return fmt.Errorf("%w: %s", decisions.ErrInvalid, strings.TrimPrefix(err.Error(), "json: "))
	}
	return fmt.Errorf("%w: %s", decisions.ErrInvalid, err.Error())
}

// decisionError answers with the status and a user-safe message for err.
// notFound is the message for an unknown id. Unexpected errors are logged
// (paths and error text only, never file contents) and reported generically.
func (s *server) decisionError(w http.ResponseWriter, r *http.Request, err error, notFound string) {
	status, msg := classifyDecisionError(err, notFound)
	if status == http.StatusInternalServerError {
		appLogger.Error("decision request failed",
			"request_id", requestIDFrom(r.Context()), "method", r.Method, "path", r.URL.Path, "error", err.Error())
	}
	apiResponse(w, status, nil, msg)
}

func classifyDecisionError(err error, notFound string) (int, string) {
	detail := func(sentinel error) string {
		return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
	}
	switch {
	case errors.Is(err, decisions.ErrInvalid):
		return http.StatusBadRequest, detail(decisions.ErrInvalid)
	case errors.Is(err, policy.ErrInvalidSettings):
		return http.StatusBadRequest, detail(policy.ErrInvalidSettings)
	case errors.Is(err, decisions.ErrTargetRequired):
		return http.StatusBadRequest, "this decision has no suggested folder; provide a target"
	case errors.Is(err, decisions.ErrNotFound):
		if notFound == "" {
			notFound = "not found"
		}
		return http.StatusNotFound, notFound
	case errors.Is(err, decisions.ErrNotPending):
		return http.StatusConflict, detail(decisions.ErrNotPending)
	case errors.Is(err, decisions.ErrNotEligible):
		return http.StatusConflict, detail(decisions.ErrNotEligible)
	case errors.Is(err, decisions.ErrExecutorUnavailable):
		return http.StatusServiceUnavailable, "file executor unavailable"

	// The world changed since the decision was made.
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusConflict, "the file has moved or no longer exists"
	case errors.Is(err, executor.ErrInvalidRoot):
		return http.StatusConflict, "the watched folder no longer exists"
	case errors.Is(err, executor.ErrAlreadyInPlace):
		return http.StatusConflict, "the file is already in the target folder"
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, fs.ErrExist):
		return http.StatusConflict, "the target folder is blocked by an existing file"
	case errors.Is(err, executor.ErrAlreadyUndone):
		return http.StatusConflict, "this action was already undone"
	case errors.Is(err, executor.ErrUndoDestinationMissing):
		return http.StatusConflict, "cannot undo: the moved file no longer exists"
	case errors.Is(err, executor.ErrUndoSourceOccupied):
		return http.StatusConflict, "cannot undo: the original location is occupied by another file"

	// The request is well formed but not something the Executor may do.
	case errors.Is(err, executor.ErrOutsideRoot):
		return http.StatusUnprocessableEntity, "the path would leave the watched folder"
	case errors.Is(err, executor.ErrSymlink), errors.Is(err, executor.ErrNotRegularFile):
		return http.StatusUnprocessableEntity, "only regular files can be moved"
	case errors.Is(err, executor.ErrInvalidFile), errors.Is(err, executor.ErrInvalidTarget), errors.Is(err, executor.ErrUnsupportedAction):
		return http.StatusUnprocessableEntity, "this decision cannot be carried out"

	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "request cancelled"
	}
	return http.StatusInternalServerError, "internal error"
}
