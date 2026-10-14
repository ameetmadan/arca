package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/internal/policy"
	"github.com/ameetmadan/arca/internal/sorter"
	"github.com/ameetmadan/arca/pkg/decision"
	"github.com/ameetmadan/arca/pkg/sqlengine"
	"github.com/ameetmadan/arca/pkg/vector"
)

// modelPathUpdater is implemented by LlamaCppProvider (both CGo and stub).
type modelPathUpdater interface {
	UpdateModelPath(path string)
}

// POST /api/index — index a file or directory
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		apiResponse(w, http.StatusBadRequest, nil, "path required")
		return
	}

	indexed, err := s.indexer().IndexFile(r.Context(), req.Path)
	if err != nil {
		apiResponse(w, indexStatus(err), nil, err.Error())
		return
	}

	apiResponse(w, http.StatusOK, map[string]int{"indexed": indexed}, "")
}

// indexer returns the parse -> embed -> upsert stage. The watcher pipeline
// runs the same code (see main).
func (s *server) indexer() *pipeline.Indexer {
	return &pipeline.Indexer{
		Parser: s.app.Parser,
		Embed:  s.app.EmbedProvider,
		Store:  s.app.VectorStore,
	}
}

// indexStatus maps an indexing failure to the HTTP status POST /index returns.
func indexStatus(err error) int {
	var ie *pipeline.IndexError
	if errors.As(err, &ie) {
		switch ie.Stage {
		case pipeline.StageParse:
			return http.StatusUnprocessableEntity
		case pipeline.StageEmbed:
			return http.StatusServiceUnavailable
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// POST /api/sort — sort / categorise files in a folder.
//
// The response always carries "moves" ([{file, suggested_folder}]), which is
// what the Swift client reads. With "submit": true in the request the same
// suggestions are also sent through the decision queue as decisions with
// source "cluster", and the response gains a "decisions" summary.
func (s *server) handleSort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
		Submit bool   `json:"submit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Folder == "" {
		apiResponse(w, http.StatusBadRequest, nil, "folder required")
		return
	}
	if req.Submit {
		if s.decisions == nil {
			apiResponse(w, http.StatusServiceUnavailable, nil, "decision queue unavailable")
			return
		}
		if !filepath.IsAbs(req.Folder) {
			apiResponse(w, http.StatusBadRequest, nil, "folder must be an absolute path")
			return
		}
		req.Folder = filepath.Clean(req.Folder)
	}

	suggestions, err := s.sorter.Suggest(r.Context(), req.Folder)
	if err != nil {
		apiResponse(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	moves := make([]sorter.SortMove, 0, len(suggestions))
	for _, sg := range suggestions {
		moves = append(moves, sorter.SortMove{File: sg.File, SuggestedFolder: sg.Decision.Target})
	}
	data := map[string]any{"moves": moves}
	if req.Submit {
		data["decisions"] = s.submitSuggestions(r.Context(), req.Folder, suggestions)
	}
	apiResponse(w, http.StatusOK, data, "")
}

// submitSummary reports what happened to a batch of submitted suggestions.
type submitSummary struct {
	Queued  int `json:"queued"`  // waiting for the user
	Applied int `json:"applied"` // applied by the folder's policy
	Dropped int `json:"dropped"` // the policy discarded them (folder is off)
	// Skipped suggestions already had a pending decision.
	Skipped int      `json:"skipped"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// submitSuggestions sends batch-sort suggestions through the same path as the
// watcher's decisions: decisions.Service.Submit, so the folder's policy
// decides whether each is dropped, queued or applied. Unlike the watcher it
// does not apply backpressure: the user asked for this batch explicitly.
func (s *server) submitSuggestions(ctx context.Context, root string, suggestions []sorter.Suggestion) submitSummary {
	var sum submitSummary
	for _, sg := range suggestions {
		if ctx.Err() != nil {
			break
		}
		if n, err := s.decisions.CountPending(ctx, root, sg.File); err == nil && n > 0 {
			sum.Skipped++
			continue
		}
		res, err := s.decisions.Submit(ctx, root, sg.File, sg.Decision)
		if err != nil {
			sum.Failed++
			status, msg := classifyDecisionError(err, "")
			if status == http.StatusInternalServerError {
				log.Printf("sort: submit decision: %v", err)
			}
			sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %s", sg.File, msg))
			continue
		}
		switch res.Outcome {
		case policy.ApplyNow:
			sum.Applied++
		case policy.Queue:
			sum.Queued++
		default:
			sum.Dropped++
		}
	}
	return sum
}

// POST /api/sort/apply — execute the moves returned by /api/sort
func (s *server) handleSortApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string            `json:"folder"`
		Moves  []sorter.SortMove `json:"moves"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Folder == "" {
		apiResponse(w, http.StatusBadRequest, nil, "folder and moves required")
		return
	}

	if s.exec == nil {
		apiResponse(w, http.StatusServiceUnavailable, nil, "file executor unavailable")
		return
	}
	if !filepath.IsAbs(req.Folder) {
		apiResponse(w, http.StatusBadRequest, nil, "folder must be an absolute path")
		return
	}

	// The Executor is the only code that touches files: it keeps every move
	// inside req.Folder, never overwrites, and journals each move for undo.
	var applied, skipped int
	var errs []string
	for _, m := range req.Moves {
		if r.Context().Err() != nil { // client gone or server shutting down
			break
		}
		d := decision.Decision{
			Action:     decision.Move,
			Target:     m.SuggestedFolder,
			Confidence: 1, // the user chose to apply this suggestion
			Reason:     "applied from sort suggestion",
			Source:     "user",
		}
		entry, err := s.exec.Execute(r.Context(), req.Folder, d, m.File)
		switch {
		case err == nil:
			s.recent.Record(entry.Src, entry.Dst) // the watcher must not re-decide on this move
			applied++
		case errors.Is(err, executor.ErrInvalidRoot):
			apiResponse(w, http.StatusBadRequest, nil, "folder is not an existing directory")
			return
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, executor.ErrAlreadyInPlace):
			skipped++
		default:
			errs = append(errs, fmt.Sprintf("move %s: %v", m.File, err))
		}
	}

	result := map[string]interface{}{
		"applied": applied,
		"skipped": skipped,
	}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	apiResponse(w, http.StatusOK, result, "")
}

// Chat retrieval limits. The context budget keeps prompt plus answer inside
// the LLM's context window (see llamacpp.ContextTokens).
const (
	chatTopK          = 6
	chatContextBudget = 6000 // characters of excerpts, roughly 1,500 tokens
)

type chatSource struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// GET /api/chat — SSE streaming chat over indexed documents.
//
// Events, each a "data:" line with a JSON object:
//
//	{"sources": [{"path", "name"}]}  the files the answer draws on (first)
//	{"token": "..."}                  a piece of the answer
//	{"error": "..."}                  the request failed; the stream ends
//	{"done": true}                    the answer is complete
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	question := strings.TrimSpace(r.URL.Query().Get("q"))
	if question == "" {
		apiResponse(w, http.StatusBadRequest, nil, "q query param required")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	send := func(v any) bool {
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	fail := func(msg string) { send(map[string]string{"error": msg}) }

	queryVec, err := s.app.EmbedProvider.Embed(r.Context(), question)
	if err != nil {
		s.cfgMu.Lock()
		ready := s.embeddingReady()
		s.cfgMu.Unlock()
		if !ready {
			fail("The embedding model is not downloaded yet. Finish setup in Settings → General.")
			return
		}
		fail("Could not read your question: " + err.Error())
		return
	}
	results, err := s.app.VectorStore.Search(r.Context(), queryVec, chatTopK)
	if err != nil {
		fail("Search failed: " + err.Error())
		return
	}

	excerpts, sources := buildExcerpts(results, chatContextBudget)
	if len(excerpts) == 0 {
		send(map[string]string{"token": "I couldn't find anything about that in your indexed documents. " +
			"Check that the folder holding the document is watched (Settings → General) and that indexing has finished."})
		send(map[string]bool{"done": true})
		return
	}
	if !send(map[string]any{"sources": sources}) {
		return
	}

	reader, err := s.app.LLMProvider.Generate(r.Context(), chatPrompt(excerpts, question))
	if err != nil {
		fail("The language model could not start: " + err.Error())
		return
	}

	buf := make([]byte, 512)
	for r.Context().Err() == nil { // client gone or server shutting down
		n, err := reader.Read(buf)
		if n > 0 {
			if !send(map[string]string{"token": string(buf[:n])}) {
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(err.Error())
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	send(map[string]bool{"done": true})
}

// buildExcerpts turns search results into numbered excerpts within budget
// characters, and lists the distinct source files in rank order.
func buildExcerpts(results []vector.SearchResult, budget int) ([]string, []chatSource) {
	var excerpts []string
	var sources []chatSource
	seen := map[string]bool{}
	used := 0
	for _, res := range results {
		content, _ := res.Metadata[pipeline.MetaContent].(string)
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		if used+len(content) > budget {
			if used > 0 {
				break
			}
			content = content[:budget] // a single huge chunk: keep its start
		}
		used += len(content)
		src, _ := res.Metadata[pipeline.MetaSource].(string)
		label := filepath.Base(src)
		if page, ok := res.Metadata["page"]; ok {
			label += fmt.Sprintf(" (page %v)", page)
		}
		excerpts = append(excerpts, fmt.Sprintf("[%d] %s:\n%s", len(excerpts)+1, label, content))
		if src != "" && !seen[src] {
			seen[src] = true
			sources = append(sources, chatSource{Path: src, Name: filepath.Base(src)})
		}
	}
	return excerpts, sources
}

func chatPrompt(excerpts []string, question string) string {
	return "You are Arca, a private assistant that answers questions about the user's own documents. " +
		"Answer using only the excerpts below. If they do not contain the answer, say so briefly. " +
		"Mention which file the answer comes from. Be concise.\n\n" +
		"Excerpts:\n" + strings.Join(excerpts, "\n\n") +
		"\n\nQuestion: " + question
}

// POST /api/query — natural language SQL query
func (s *server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question   string `json:"question"`
		SchemaHint string `json:"schema_hint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		apiResponse(w, http.StatusBadRequest, nil, "question required")
		return
	}

	result, err := s.app.SQLEngine.QueryNL(r.Context(), req.Question, req.SchemaHint)
	switch {
	case errors.Is(err, sqlengine.ErrNoTables):
		apiResponse(w, http.StatusConflict, nil, err.Error())
		return
	case err != nil && result.SQL != "":
		// Show the SQL the model wrote so the user can see what went wrong.
		apiResponse(w, http.StatusUnprocessableEntity, nil, fmt.Sprintf("%v\n\nSQL: %s", err, result.SQL))
		return
	case err != nil:
		apiResponse(w, http.StatusServiceUnavailable, nil, err.Error())
		return
	}

	apiResponse(w, http.StatusOK, result, "")
}

// GET /api/models — list available models and their download status
func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	type modelStatus struct {
		modelmanager.Model
		Downloaded bool `json:"downloaded"`
		Active     bool `json:"active"`
	}
	active := s.activeModelID()
	statuses := make([]modelStatus, 0, len(modelmanager.AvailableModels))
	for _, m := range modelmanager.AvailableModels {
		statuses = append(statuses, modelStatus{
			Model:      m,
			Downloaded: s.mm.IsPresent(m.ID),
			Active:     m.ID == active,
		})
	}
	apiResponse(w, http.StatusOK, statuses, "")
}

// POST /api/models/activate — make a downloaded LLM the active one. The
// embedding model, if downloaded, is activated with it.
func (s *server) handleModelActivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		apiResponse(w, http.StatusBadRequest, nil, "id required")
		return
	}

	model, ok := modelmanager.Lookup(req.ID)
	if !ok {
		apiResponse(w, http.StatusNotFound, nil, "unknown model id")
		return
	}
	if model.Kind != modelmanager.KindLLM {
		apiResponse(w, http.StatusBadRequest, nil, "only a language model can be activated")
		return
	}
	if !s.mm.IsPresent(req.ID) {
		apiResponse(w, http.StatusConflict, nil, "the model is not downloaded yet")
		return
	}

	llmPath := s.mm.ModelPath(req.ID)
	var embedPath string
	if s.mm.IsPresent(modelmanager.EmbeddingModelID) {
		embedPath = s.mm.ModelPath(modelmanager.EmbeddingModelID)
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if u, ok := s.app.LLMProvider.(modelPathUpdater); ok {
		u.UpdateModelPath(llmPath)
	}
	s.app.Cfg.LLM.ModelPath = llmPath
	if embedPath != "" && embedPath != s.app.Cfg.Embed.ModelPath {
		if u, ok := s.app.EmbedProvider.(modelPathUpdater); ok {
			u.UpdateModelPath(embedPath)
		}
		s.app.Cfg.Embed.ModelPath = embedPath
	}
	if err := s.app.PersistModelPaths(llmPath, embedPath); err != nil {
		log.Printf("persist model paths: %v", err)
		apiResponse(w, http.StatusInternalServerError, nil, "the model is active but could not be saved to the configuration")
		return
	}

	apiResponse(w, http.StatusOK, map[string]string{"activated": req.ID}, "")
}

// GET /api/models/download — SSE progress stream for a model download
func (s *server) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	modelID := r.URL.Query().Get("id")
	if modelID == "" {
		apiResponse(w, http.StatusBadRequest, nil, "id query param required")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch, err := s.mm.Download(r.Context(), modelID)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":%q}\n\n", err.Error())
		flusher.Flush()
		return
	}

	for {
		select {
		case <-r.Context().Done():
			// Download sees the same context and stops on its own.
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := modelmanager.SSEWrite(w, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
