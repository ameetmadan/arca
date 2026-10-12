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

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/internal/sorter"
	"github.com/ameetmadan/arca/internal/watcher"
)

type server struct {
	app    *config.App
	mm     *modelmanager.Manager
	sorter *sorter.Sorter
	exec   *executor.Executor // nil when the journal could not be opened
	rules  *rulesAPI          // /api/v1/rules; see rules_api.go
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

	srv := &server{
		app: app,
		mm:  mm,
		sorter: &sorter.Sorter{
			LLM:    app.LLMProvider,
			Embed:  app.EmbedProvider,
			Parser: app.Parser,
		},
		exec:  app.Executor,
		rules: newRulesAPI(rulesStore(app), rulesReloader(app)),
	}

	// File events flow watcher -> pipeline -> indexer in-process; the HTTP
	// endpoint POST /api/v1/index runs the same indexer.
	events := pipeline.New(srv.indexer().Handle, pipeline.Options{Debounce: pipeline.DefaultDebounce})
	w, err := watcher.New(events.Submit)
	if err != nil {
		log.Fatalf("create watcher: %v", err)
	}
	for _, folder := range app.Cfg.Watcher.Folders {
		if err := w.AddFolder(folder); err != nil {
			log.Printf("watcher: add %s: %v", folder, err)
		}
	}

	r := srv.routes() // see routes.go

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
		Workers: append([]func(context.Context){events.Run, w.Run}, app.Workers...),
		Closers: append(closersOf(app.VectorStore, app.SQLEngine), app.Close),
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
