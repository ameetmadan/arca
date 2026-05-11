package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ameetmadan/arca/internal/modelmanager"
)

// modelPathUpdater is implemented by LlamaCppProvider (both CGo and stub).
type modelPathUpdater interface {
	UpdateModelPath(path string)
}

// apiResponse wraps all responses in a consistent envelope.
func apiResponse(w http.ResponseWriter, status int, data interface{}, apiErr string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"data":  data,
		"error": apiErr,
		"meta":  map[string]string{"version": "1"},
	})
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

	chunks, err := s.app.Parser.Parse(req.Path)
	if err != nil {
		apiResponse(w, http.StatusUnprocessableEntity, nil, err.Error())
		return
	}

	for i, chunk := range chunks {
		vec, err := s.app.EmbedProvider.Embed(r.Context(), chunk.Content)
		if err != nil {
			apiResponse(w, http.StatusServiceUnavailable, nil, fmt.Sprintf("embed chunk %d: %v", i, err))
			return
		}
		id := fmt.Sprintf("%s#%d", req.Path, i)
		if err := s.app.VectorStore.Upsert(r.Context(), id, vec, chunk.Metadata); err != nil {
			apiResponse(w, http.StatusInternalServerError, nil, fmt.Sprintf("upsert chunk %d: %v", i, err))
			return
		}
	}

	apiResponse(w, http.StatusOK, map[string]int{"indexed": len(chunks)}, "")
}

type SortMove struct {
	File            string `json:"file"`
	SuggestedFolder string `json:"suggested_folder"`
}

// POST /api/sort — sort / categorise files in a folder
func (s *server) handleSort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Folder == "" {
		apiResponse(w, http.StatusBadRequest, nil, "folder required")
		return
	}

	var filenames []string
	err := filepath.WalkDir(req.Folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(req.Folder, path)
		filenames = append(filenames, rel)
		return nil
	})
	if err != nil {
		apiResponse(w, http.StatusBadRequest, nil, fmt.Sprintf("read folder: %v", err))
		return
	}
	if len(filenames) == 0 {
		apiResponse(w, http.StatusOK, map[string][]SortMove{"moves": {}}, "")
		return
	}

	fileList := strings.Join(filenames, "\n")
	prompt := fmt.Sprintf(
		"Suggest a folder organisation for these files (paths are relative to the root folder):\n\n%s\n\nRespond as a JSON array where each element is {\"file\": \"<relative-path>\", \"suggested_folder\": \"<folder>\"}. Output only valid JSON, no explanation.",
		fileList,
	)

	raw, err := s.app.LLMProvider.GenerateSync(r.Context(), prompt)
	if err != nil {
		apiResponse(w, http.StatusServiceUnavailable, nil, err.Error())
		return
	}

	moves, err := parseSortMoves(raw)
	if err != nil {
		apiResponse(w, http.StatusInternalServerError, nil, fmt.Sprintf("parse suggestions: %v", err))
		return
	}

	apiResponse(w, http.StatusOK, map[string][]SortMove{"moves": moves}, "")
}

// POST /api/sort/apply — execute the moves returned by /api/sort
func (s *server) handleSortApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string     `json:"folder"`
		Moves  []SortMove `json:"moves"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Folder == "" {
		apiResponse(w, http.StatusBadRequest, nil, "folder and moves required")
		return
	}

	var applied, skipped int
	var errs []string
	for _, m := range req.Moves {
		src := filepath.Join(req.Folder, m.File)
		if _, err := os.Stat(src); err != nil {
			skipped++
			continue
		}
		destDir := filepath.Join(req.Folder, m.SuggestedFolder)
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			errs = append(errs, fmt.Sprintf("mkdir %s: %v", destDir, err))
			continue
		}
		dst := filepath.Join(destDir, filepath.Base(m.File))
		if err := os.Rename(src, dst); err != nil {
			errs = append(errs, fmt.Sprintf("move %s: %v", m.File, err))
			continue
		}
		applied++
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

// parseSortMoves extracts a JSON array of SortMove from raw LLM output.
func parseSortMoves(raw string) ([]SortMove, error) {
	// Find the first '[' and last ']' to handle any surrounding text.
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array found in LLM output")
	}
	var moves []SortMove
	if err := json.Unmarshal([]byte(raw[start:end+1]), &moves); err != nil {
		return nil, err
	}
	return moves, nil
}

// GET /api/chat — SSE streaming chat over indexed documents
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	question := r.URL.Query().Get("q")
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

	queryVec, err := s.app.EmbedProvider.Embed(r.Context(), question)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":%q}\n\n", err.Error())
		flusher.Flush()
		return
	}

	results, err := s.app.VectorStore.Search(r.Context(), queryVec, 5)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":%q}\n\n", err.Error())
		flusher.Flush()
		return
	}

	var contextParts []string
	for _, res := range results {
		if meta, ok := res.Metadata["content"].(string); ok {
			contextParts = append(contextParts, meta)
		}
	}
	prompt := fmt.Sprintf("Context:\n%s\n\nQuestion: %s\n\nAnswer:", strings.Join(contextParts, "\n---\n"), question)

	reader, err := s.app.LLMProvider.Generate(r.Context(), prompt)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":%q}\n\n", err.Error())
		flusher.Flush()
		return
	}

	buf := make([]byte, 512)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			token := string(buf[:n])
			b, _ := json.Marshal(map[string]string{"token": token})
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}
	fmt.Fprint(w, "data: {\"done\":true}\n\n")
	flusher.Flush()
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
	if err != nil {
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
	}
	statuses := make([]modelStatus, 0, len(modelmanager.AvailableModels))
	for _, m := range modelmanager.AvailableModels {
		statuses = append(statuses, modelStatus{
			Model:      m,
			Downloaded: s.mm.IsPresent(m.ID),
		})
	}
	apiResponse(w, http.StatusOK, statuses, "")
}

// POST /api/models/activate — set the active LLM model (called after download)
func (s *server) handleModelActivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		apiResponse(w, http.StatusBadRequest, nil, "id required")
		return
	}

	llmPath := s.mm.ModelPath(req.ID)
	if llmPath == "" {
		apiResponse(w, http.StatusNotFound, nil, "unknown model id")
		return
	}
	if u, ok := s.app.LLMProvider.(modelPathUpdater); ok {
		u.UpdateModelPath(llmPath)
	}

	var embedPath string
	if s.mm.IsPresent("nomic-embed") {
		embedPath = s.mm.ModelPath("nomic-embed")
		if u, ok := s.app.EmbedProvider.(modelPathUpdater); ok {
			u.UpdateModelPath(embedPath)
		}
	}

	if err := s.app.PersistModelPaths(llmPath, embedPath); err != nil {
		log.Printf("persist model paths: %v", err)
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

	for ev := range ch {
		if err := modelmanager.SSEWrite(w, ev); err != nil {
			return
		}
		flusher.Flush()
	}
}
