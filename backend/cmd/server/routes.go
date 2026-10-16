package main

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// routes builds the full HTTP handler with production defaults.
func (s *server) routes() http.Handler {
	return s.routesWith(appLogger, newRateLimiter(defaultRateLimitPerSec, defaultRateLimitBurst))
}

// routesWith lets tests inject a logger and a limiter with a fake clock.
func (s *server) routesWith(logger *slog.Logger, limiter *rateLimiter) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(accessLog(logger))
	r.Use(recoverer(logger))
	r.Use(securityHeaders)
	r.Use(corsMiddleware)
	r.Use(bearerAuth(s.apiToken))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		apiResponse(w, http.StatusNotFound, nil, "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		apiResponse(w, http.StatusMethodNotAllowed, nil, "method not allowed")
	})

	// Probes sit outside the versioned API and outside the rate limiter so a
	// supervisor polling them can never be throttled.
	r.Get("/health", s.handleHealth)
	r.Get("/ready", s.handleReady)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(limiter.middleware)
		s.apiRoutes(r)
		s.rulesRoutes(r)    // new in v1: no legacy /api alias
		s.settingsRoutes(r) // new in v1: no legacy /api alias
		s.tableRoutes(r)    // new in v1: no legacy /api alias
		r.Get("/calibration", s.handleCalibration)
		r.Post("/calibration/decisions", s.handleCalibrationPrepare)
	})

	// Deprecated: unversioned aliases kept for one release so an older Swift
	// client keeps working. They share the v1 handlers and rate limiter.
	// Remove once the bundled app only uses /api/v1.
	r.Route("/api", func(r chi.Router) {
		r.Use(limiter.middleware)
		r.Use(deprecatedAlias)
		s.apiRoutes(r)
	})

	return r
}

// deprecatedAlias marks responses from the legacy /api/* paths (RFC 9745
// Deprecation header) and points at the /api/v1 successor (RFC 8288 Link).
func deprecatedAlias(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		successor := "/api/v1" + strings.TrimPrefix(r.URL.Path, "/api")
		w.Header().Add("Link", "<"+successor+">; rel=\"successor-version\"")
		next.ServeHTTP(w, r)
	})
}

// apiRoutes registers the business endpoints on r. It is mounted under
// /api/v1 and, as a deprecated alias, under /api.
func (s *server) apiRoutes(r chi.Router) {
	r.Post("/index", s.handleIndex)
	r.Post("/sort", s.handleSort)
	r.Post("/sort/apply", s.handleSortApply)
	r.Get("/chat", s.handleChat)
	r.Post("/query", s.handleQuery)
	r.Get("/models", s.handleModels)
	r.Get("/models/download", s.handleModelDownload)
	r.Post("/models/activate", s.handleModelActivate)
	s.decisionRoutes(r)
}
