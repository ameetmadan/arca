package main

import (
	"log/slog"
	"net/http"

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
	})

	return r
}

// apiRoutes registers the business endpoints on r. It is mounted once under
// /api/v1.
func (s *server) apiRoutes(r chi.Router) {
	r.Post("/index", s.handleIndex)
	r.Post("/sort", s.handleSort)
	r.Post("/sort/apply", s.handleSortApply)
	r.Get("/chat", s.handleChat)
	r.Post("/query", s.handleQuery)
	r.Get("/models", s.handleModels)
	r.Get("/models/download", s.handleModelDownload)
	r.Post("/models/activate", s.handleModelActivate)
}
