package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/internal/sorter"
	"github.com/ameetmadan/arca/internal/watcher"
)

type server struct {
	app    *config.App
	mm     *modelmanager.Manager
	sorter *sorter.Sorter
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	flag.Parse()

	ensureAppDirs()

	app, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	modelsDir := app.Cfg.Models.Dir
	if modelsDir == "" {
		home, _ := os.UserHomeDir()
		modelsDir = filepath.Join(home, "Library", "Application Support", "Arca", "models")
	}
	mm := modelmanager.New(modelsDir)

	w, err := watcher.New(fmt.Sprintf("http://localhost:%d/api/index", app.Cfg.Server.Port))
	if err != nil {
		log.Fatalf("create watcher: %v", err)
	}
	for _, folder := range app.Cfg.Watcher.Folders {
		if err := w.AddFolder(folder); err != nil {
			log.Printf("watcher: add %s: %v", folder, err)
		}
	}

	srv := &server{
		app: app,
		mm:  mm,
		sorter: &sorter.Sorter{
			LLM:    app.LLMProvider,
			Embed:  app.EmbedProvider,
			Parser: app.Parser,
		},
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Post("/api/index", srv.handleIndex)
	r.Post("/api/sort", srv.handleSort)
	r.Post("/api/sort/apply", srv.handleSortApply)
	r.Get("/api/chat", srv.handleChat)
	r.Post("/api/query", srv.handleQuery)
	r.Get("/api/models", srv.handleModels)
	r.Get("/api/models/download", srv.handleModelDownload)
	r.Post("/api/models/activate", srv.handleModelActivate)

	addr := fmt.Sprintf("localhost:%d", app.Cfg.Server.Port)
	httpSrv := &http.Server{Addr: addr, Handler: r}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		// Restore default signal handling after the first signal so a second
		// Ctrl-C force-quits a shutdown that is taking too long.
		<-ctx.Done()
		stop()
	}()

	err = run(ctx, runConfig{
		Server:  httpSrv,
		Workers: []func(context.Context){w.Run},
		Closers: closersOf(app.VectorStore, app.SQLEngine),
	})
	if err != nil {
		// Fatal only after run has stopped workers and closed resources.
		log.Fatalf("serve: %v", err)
	}
	log.Println("stopped")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:44742")
		next.ServeHTTP(w, r)
	})
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Arca", "config.yaml")
}

func ensureAppDirs() {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, "Library", "Application Support", "Arca")
	for _, dir := range []string{base, filepath.Join(base, "models")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("mkdir %s: %v", dir, err)
		}
	}
}
