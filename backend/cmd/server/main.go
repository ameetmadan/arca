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
	"sync"
	"syscall"
	"time"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/executor"
	"github.com/ameetmadan/arca/internal/modelmanager"
	"github.com/ameetmadan/arca/internal/pipeline"
	"github.com/ameetmadan/arca/internal/scan"
	"github.com/ameetmadan/arca/internal/sorter"
	"github.com/ameetmadan/arca/internal/watcher"
)

type server struct {
	apiToken string
	app      *config.App
	mm       *modelmanager.Manager
	sorter   *sorter.Sorter
	exec     *executor.Executor // nil when the journal could not be opened
	rules    *rulesAPI          // /api/v1/rules; see rules_api.go

	decisions *decisions.Service // nil when the queue database could not be opened

	// recent remembers paths Arca just moved so the watcher does not decide
	// on its own moves again; nil-safe.
	recent *pipeline.RecentMoves

	// scanner indexes files already in the watched folders; nil in tests
	// that do not need it.
	scanner *scan.Scanner

	// cfgMu guards the parts of app.Cfg that the settings and model
	// endpoints change at runtime (model paths, watched folders).
	cfgMu sync.Mutex
}

// version is stamped at build time: go build -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	exitWithParent := flag.Bool("exit-with-parent", false,
		"shut down when the parent process exits (the app passes this so a crash never leaves the server running)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	insecureDev := flag.Bool("insecure-dev", false, "allow unauthenticated development access")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	token := os.Getenv("ARCA_API_TOKEN")
	if token == "" && !*insecureDev {
		log.Fatal("ARCA_API_TOKEN is required (use --insecure-dev only for local development)")
	}
	log.Printf("arca-server %s", version)

	ensureAppDirs()

	if created, err := config.EnsureFile(*cfgPath); err != nil {
		log.Fatalf("create default config: %v", err)
	} else if created {
		log.Printf("wrote default config to %s", *cfgPath)
	}

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

	recent := pipeline.NewRecentMoves(pipeline.DefaultRecentMoveWindow)
	var fileExec decisions.FileExecutor // stays a nil interface when there is no journal
	if app.Executor != nil {
		fileExec = recent.Wrap(app.Executor)
	}
	decSvc, closeDecisions := newDecisionService(filepath.Dir(*cfgPath), fileExec)
	srv := &server{
		apiToken:  token,
		recent:    recent,
		decisions: decSvc,
		app:       app,
		mm:        mm,
		sorter: &sorter.Sorter{
			LLM:    app.LLMProvider,
			Embed:  app.EmbedProvider,
			Parser: app.Parser,
		},
		exec:  app.Executor,
		rules: newRulesAPI(rulesStore(app), rulesReloader(app)),
	}

	appDir := filepath.Dir(*cfgPath)
	srv.scanner = srv.newScanner(filepath.Join(appDir, "index-state.json"))
	scanRoots := watchedRoots(app.Cfg.Watcher.Folders)

	// File events flow watcher -> pipeline -> indexer -> decider in-process;
	// the HTTP endpoint POST /api/v1/index runs the same indexer.
	events := pipeline.New(srv.eventHandler(), pipeline.Options{Debounce: pipeline.DefaultDebounce})
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
	if *exitWithParent {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go watchParent(ctx, os.Getppid(), parentPollInterval, cancel)
	}
	go func() {
		// Restore default signal handling after the first signal so a second
		// Ctrl-C force-quits a shutdown that is taking too long.
		<-ctx.Done()
		stop()
	}()

	err = run(ctx, runConfig{
		Server: httpSrv,
		Workers: append([]func(context.Context){
			events.Run, w.Run,
			func(ctx context.Context) { srv.scanner.Run(ctx, scanRoots) },
		}, app.Workers...),
		Closers: append(closersOf(app.VectorStore, app.SQLEngine), app.Close, closeDecisions),
	})
	if err != nil {
		// Fatal only after run has stopped workers and closed resources.
		log.Fatalf("serve: %v", err)
	}
	log.Println("stopped")
}

const parentPollInterval = time.Second

// watchParent calls onExit once the parent process with pid ppid is gone.
// When a parent exits its children are re-parented (to launchd on macOS), so
// a changed parent pid means the app quit or crashed without stopping us.
func watchParent(ctx context.Context, ppid int, every time.Duration, onExit func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if os.Getppid() != ppid {
				log.Println("parent process exited; shutting down")
				onExit()
				return
			}
		}
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Native clients send no Origin. Browser access is intentionally disabled.
		if r.Header.Get("Origin") != "" {
			apiResponse(w, http.StatusForbidden, nil, "browser access is disabled")
			return
		}
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
