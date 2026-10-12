package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultShutdownTimeout = 10 * time.Second
	defaultDrainTimeout    = 5 * time.Second
)

// runConfig describes everything run needs to serve and then shut down.
type runConfig struct {
	// Server is the HTTP server to run. run takes ownership of Handler and
	// BaseContext.
	Server *http.Server
	// Listener is optional; when nil run listens on Server.Addr.
	Listener net.Listener
	// Workers are background goroutines (e.g. the folder watcher). They are
	// started with a context that is cancelled as soon as shutdown begins,
	// and run waits for them to return.
	Workers []func(ctx context.Context)
	// Closers release resources (DB handles). They run last, in reverse
	// registration order, after the HTTP server and workers have stopped.
	Closers []func()
	// ShutdownTimeout bounds how long in-flight requests may keep running
	// after shutdown starts. Requests still running afterwards are cancelled.
	ShutdownTimeout time.Duration
	// DrainTimeout bounds how long run waits for cancelled handlers and
	// workers to return before closing resources anyway.
	DrainTimeout time.Duration
}

// run serves HTTP until ctx is cancelled (typically by SIGINT/SIGTERM) or the
// listener fails, then shuts down in order:
//
//  1. cancel workers and stop accepting new connections;
//  2. let in-flight requests finish for up to ShutdownTimeout;
//  3. cancel the context of anything still running (SSE streams) and wait
//     for handlers and workers to return;
//  4. close resources.
//
// A listener failure is returned only after the same cleanup has happened, so
// it never bypasses shutdown. A forced cancellation in step 3 is logged but is
// not an error: the process was asked to stop and did.
func run(ctx context.Context, cfg runConfig) error {
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = defaultDrainTimeout
	}

	// Closers run on every path out of run, including a failed Listen.
	defer func() {
		for i := len(cfg.Closers) - 1; i >= 0; i-- {
			cfg.Closers[i]()
		}
	}()

	// Request contexts derive from reqCtx rather than from ctx so that a
	// signal lets in-flight requests finish; reqCtx is cancelled only once
	// the grace period is over.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()

	srv := cfg.Server
	srv.BaseContext = func(net.Listener) context.Context { return reqCtx }

	var inflight atomic.Int64
	next := srv.Handler
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inflight.Add(1)
		defer inflight.Add(-1)
		next.ServeHTTP(w, r)
	})

	ln := cfg.Listener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", srv.Addr); err != nil {
			return err
		}
	}

	var workers sync.WaitGroup
	for _, w := range cfg.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w(workerCtx)
		}()
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("arca backend listening on %s", ln.Addr())
		serveErr <- srv.Serve(ln)
	}()

	var listenErr error
	select {
	case <-ctx.Done():
		log.Println("shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			listenErr = err
			log.Printf("server stopped unexpectedly, shutting down: %v", err)
		}
	}

	cancelWorkers()

	shutCtx, cancelShut := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShut()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("graceful shutdown incomplete (%v); cancelling remaining requests", err)
		srv.Close() //nolint:errcheck // force-closes the remaining connections
	}
	cancelReq()

	drainDeadline := time.Now().Add(cfg.DrainTimeout)
	for inflight.Load() > 0 && time.Now().Before(drainDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := inflight.Load(); n > 0 {
		log.Printf("%d request handler(s) still running after drain timeout", n)
	}

	workersDone := make(chan struct{})
	go func() { workers.Wait(); close(workersDone) }()
	select {
	case <-workersDone:
	case <-time.After(time.Until(drainDeadline)):
		log.Println("workers still running after drain timeout")
	}

	return listenErr
}

// closersOf returns the Close methods of those resources that have one.
// Providers expose Close only in their CGo builds, so this is optional.
func closersOf(resources ...any) []func() {
	var out []func()
	for _, r := range resources {
		if c, ok := r.(interface{ Close() }); ok {
			out = append(out, c.Close)
		}
	}
	return out
}
