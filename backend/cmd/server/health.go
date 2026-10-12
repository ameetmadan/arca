package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ameetmadan/arca/pkg/vector"
)

const readyCheckTimeout = 2 * time.Second

var errProviderMissing = errors.New("provider not configured")

type readyCheck struct {
	name string
	run  func(ctx context.Context) error
}

// readyChecks lists what must hold before the server can do useful work:
// every provider wired by internal/config is present, and the vector
// database answers. A vector store without a Ping method (e.g. an in-memory
// fake) has no connection to verify, so that check passes.
func (s *server) readyChecks() []readyCheck {
	present := func(name string, isNil func() bool) readyCheck {
		return readyCheck{name: name, run: func(context.Context) error {
			if isNil() {
				return errProviderMissing
			}
			return nil
		}}
	}
	a := s.app
	return []readyCheck{
		present("llm", func() bool { return a.LLMProvider == nil }),
		present("embed", func() bool { return a.EmbedProvider == nil }),
		present("vector", func() bool { return a.VectorStore == nil }),
		present("sql", func() bool { return a.SQLEngine == nil }),
		present("parser", func() bool { return a.Parser == nil }),
		{name: "database", run: func(ctx context.Context) error {
			if p, ok := a.VectorStore.(vector.Pinger); ok {
				return p.Ping(ctx)
			}
			return nil
		}},
	}
}

// GET /health — liveness: the process is up and serving HTTP. It deliberately
// touches nothing else so a slow provider can never make it fail.
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	apiResponse(w, http.StatusOK, map[string]string{"status": "ok"}, "")
}

// GET /ready — readiness: providers are loaded and the database is reachable.
// Failures are logged with the underlying error but the response only names
// the failing check, since error text can include filesystem paths.
func (s *server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyCheckTimeout)
	defer cancel()

	checks := map[string]string{}
	ready := true
	for _, c := range s.readyChecks() {
		if err := c.run(ctx); err != nil {
			ready = false
			checks[c.name] = "failed"
			appLogger.Warn("readiness check failed",
				"request_id", requestIDFrom(r.Context()), "check", c.name, "error", err.Error())
			continue
		}
		checks[c.name] = "ok"
	}

	if !ready {
		// Per-check detail rides along in data so callers can see what failed.
		apiResponse(w, http.StatusServiceUnavailable,
			map[string]interface{}{"status": "not_ready", "checks": checks}, "not ready")
		return
	}
	apiResponse(w, http.StatusOK, map[string]interface{}{"status": "ready", "checks": checks}, "")
}
