package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/ameetmadan/arca/pkg/sqlengine"
)

// tableRoutes registers the Table Chat table management endpoints on r
// (mounted under /api/v1).
func (s *server) tableRoutes(r chi.Router) {
	r.Get("/tables", s.handleTablesList)
	r.Post("/tables/import", s.handleTablesImport)
	r.Delete("/tables/{name}", s.handleTablesDrop)
}

func (s *server) tableManager(w http.ResponseWriter) (sqlengine.TableManager, bool) {
	tm, ok := s.app.SQLEngine.(sqlengine.TableManager)
	if !ok {
		apiResponse(w, http.StatusNotImplemented, nil, "the SQL engine cannot import tables")
		return nil, false
	}
	return tm, true
}

// GET /api/v1/tables
func (s *server) handleTablesList(w http.ResponseWriter, r *http.Request) {
	tm, ok := s.tableManager(w)
	if !ok {
		return
	}
	tables, err := tm.Tables(r.Context())
	if err != nil {
		log.Printf("tables: list: %v", err)
		apiResponse(w, http.StatusServiceUnavailable, nil, "could not list tables")
		return
	}
	if tables == nil {
		tables = []sqlengine.Table{}
	}
	apiResponse(w, http.StatusOK, tables, "")
}

// POST /api/v1/tables/import — body {"path": "/abs/file.csv"}
func (s *server) handleTablesImport(w http.ResponseWriter, r *http.Request) {
	tm, ok := s.tableManager(w)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		apiResponse(w, http.StatusBadRequest, nil, "path required")
		return
	}
	if !filepath.IsAbs(req.Path) {
		apiResponse(w, http.StatusBadRequest, nil, "path must be absolute")
		return
	}
	if !tm.CanImport(req.Path) {
		apiResponse(w, http.StatusUnprocessableEntity, nil, "only .csv, .tsv, .xlsx, .parquet and .json files can be imported")
		return
	}
	if info, err := os.Stat(req.Path); err != nil || info.IsDir() {
		apiResponse(w, http.StatusUnprocessableEntity, nil, "the file does not exist or cannot be read")
		return
	}
	tables, err := tm.ImportFile(r.Context(), req.Path)
	if err != nil {
		apiResponse(w, http.StatusUnprocessableEntity, nil, err.Error())
		return
	}
	apiResponse(w, http.StatusCreated, tables, "")
}

// DELETE /api/v1/tables/{name}
func (s *server) handleTablesDrop(w http.ResponseWriter, r *http.Request) {
	tm, ok := s.tableManager(w)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	switch err := tm.DropTable(r.Context(), name); {
	case err == nil:
		apiResponse(w, http.StatusOK, map[string]string{"dropped": name}, "")
	case errors.Is(err, sqlengine.ErrUnknownTable):
		apiResponse(w, http.StatusNotFound, nil, "no such table")
	default:
		log.Printf("tables: drop: %v", err)
		apiResponse(w, http.StatusInternalServerError, nil, "could not remove the table")
	}
}
