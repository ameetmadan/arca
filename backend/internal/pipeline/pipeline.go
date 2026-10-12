// Package pipeline is the in-process event pipeline: file events go in
// (from the watcher, a manual sort, or an API call) and are handed to a bounded
// pool of workers after de-duplication and debouncing.
//
// It replaces the watcher's old habit of POSTing to its own HTTP API. See
// docs/adr/0006-decision-engine-chain-with-policy-and-executor.md.
//
// # Backpressure policy
//
// Submit never blocks, so a slow indexer can never stall the file watcher.
// When more than Options.QueueSize distinct paths are waiting, the oldest
// waiting path is dropped (and counted in Dropped). Dropping the oldest keeps
// the most recent state of the folder, which is what matters; a dropped file is
// picked up again the next time it changes or is indexed manually.
//
// # De-duplication
//
// Events for the same path are coalesced: the debounce is trailing-edge, so a
// burst of writes is handled once, Options.Debounce after the last one, and the
// final write is never lost. A path is also never processed by two workers at
// the same time; a change that arrives while it is being processed is handled
// afterwards.
//
// # Shutdown
//
// Run returns after ctx is cancelled and every worker has returned. Queued
// events are discarded and in-flight handlers receive the cancelled context.
package pipeline

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync/atomic"
	"time"
)

// Op is the kind of change a file event reports.
type Op int

// File event kinds.
const (
	Created Op = iota + 1
	Modified
	Removed
)

// String returns the lower-case name of the op.
func (o Op) String() string {
	switch o {
	case Created:
		return "created"
	case Modified:
		return "modified"
	case Removed:
		return "removed"
	}
	return fmt.Sprintf("op(%d)", int(o))
}

func (o Op) valid() bool { return o == Created || o == Modified || o == Removed }

// Event is a change to one file.
type Event struct {
	Op   Op
	Path string
}

// Handler processes one event. It must honour ctx cancellation. A returned
// error is reported through Options.OnError; it does not stop the pipeline.
type Handler func(ctx context.Context, ev Event) error

// Defaults for Options.
const (
	DefaultWorkers   = 4
	DefaultQueueSize = 1024
	DefaultDebounce  = 500 * time.Millisecond

	// blockedRetry is how often the scheduler retries handing a due event to
	// the workers while all of them are busy.
	blockedRetry = 20 * time.Millisecond
)

// Options tunes a Pipeline. Zero values select the defaults above, except
// Debounce where 0 means "no debounce"; use DefaultDebounce for the production
// value.
type Options struct {
	// Workers is the number of events processed concurrently.
	Workers int
	// QueueSize bounds the number of distinct paths waiting to be processed.
	QueueSize int
	// Debounce is how long a path must stay quiet before it is processed.
	Debounce time.Duration
	// OnError is called when a handler fails or panics. It is not called for
	// failures caused by shutdown. The default logs the op and the error.
	OnError func(ev Event, err error)
}

// Pipeline coalesces file events and runs them through a Handler on a bounded
// worker pool. Create it with New and start it with Run.
type Pipeline struct {
	handle   Handler
	workers  int
	size     int
	debounce time.Duration
	onError  func(Event, error)
	now      func() time.Time

	in      chan Event      // Submit -> scheduler, drop-oldest when full
	work    chan Event      // scheduler -> workers
	done    chan string     // workers -> scheduler: path finished
	dropped atomic.Uint64
	running atomic.Bool
}

// New returns a Pipeline that passes events to h.
func New(h Handler, opt Options) *Pipeline {
	if opt.Workers <= 0 {
		opt.Workers = DefaultWorkers
	}
	if opt.QueueSize <= 0 {
		opt.QueueSize = DefaultQueueSize
	}
	if opt.Debounce < 0 {
		opt.Debounce = 0
	}
	if opt.OnError == nil {
		opt.OnError = func(ev Event, err error) {
			log.Printf("pipeline: %s event failed: %v", ev.Op, err)
		}
	}
	return &Pipeline{
		handle:   h,
		workers:  opt.Workers,
		size:     opt.QueueSize,
		debounce: opt.Debounce,
		onError:  opt.OnError,
		now:      time.Now,
		in:       make(chan Event, opt.QueueSize),
		work:     make(chan Event, opt.Workers),
		done:     make(chan string, opt.Workers),
	}
}

// Submit enqueues an event. It never blocks: if the queue is full the oldest
// queued event is dropped to make room. Events with an empty path or an unknown
// Op are ignored. The path is carried verbatim; it is never re-encoded.
func (p *Pipeline) Submit(ev Event) {
	if ev.Path == "" || !ev.Op.valid() {
		return
	}
	for {
		select {
		case p.in <- ev:
			return
		default:
		}
		select {
		case <-p.in:
			p.dropped.Add(1)
		default:
		}
	}
}

// Dropped returns how many events were discarded because the queue was full.
func (p *Pipeline) Dropped() uint64 { return p.dropped.Load() }

// Run processes events until ctx is cancelled, then waits for the workers to
// return. It must be called at most once; a second concurrent call returns
// immediately.
func (p *Pipeline) Run(ctx context.Context) {
	if !p.running.CompareAndSwap(false, true) {
		return
	}
	defer p.running.Store(false)

	finished := make(chan struct{}, p.workers)
	for i := 0; i < p.workers; i++ {
		go func() {
			defer func() { finished <- struct{}{} }()
			p.worker(ctx)
		}()
	}
	p.schedule(ctx)
	for i := 0; i < p.workers; i++ {
		<-finished
	}
}

type entry struct {
	ev  Event
	due time.Time
	seq uint64 // order in which the path first became pending
}

// schedule owns all de-duplication state. It is the only goroutine that reads
// p.in and writes p.work.
func (p *Pipeline) schedule(ctx context.Context) {
	pending := make(map[string]*entry)
	inflight := make(map[string]bool)
	var seq uint64

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-p.in:
			p.add(pending, &seq, ev)
		case path := <-p.done:
			delete(inflight, path)
		case <-timer.C:
		}

		next, blocked := p.flush(pending, inflight)
		wake := blockedRetry
		if !blocked {
			if next.IsZero() {
				wake = time.Hour
			} else if wake = next.Sub(p.now()); wake < 0 {
				wake = 0
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wake)
	}
}

// add merges ev into the pending set, evicting the oldest path when full.
func (p *Pipeline) add(pending map[string]*entry, seq *uint64, ev Event) {
	due := p.now().Add(p.debounce)
	if e, ok := pending[ev.Path]; ok {
		e.ev.Op = mergeOp(e.ev.Op, ev.Op)
		e.due = due
		return
	}
	if len(pending) >= p.size {
		var oldest *entry
		for _, e := range pending {
			if oldest == nil || e.seq < oldest.seq {
				oldest = e
			}
		}
		delete(pending, oldest.ev.Path)
		p.dropped.Add(1)
	}
	*seq++
	pending[ev.Path] = &entry{ev: ev, due: due, seq: *seq}
}

// flush hands every due, idle path to the workers without blocking. It returns
// the earliest remaining deadline and whether a due path is waiting for a free
// worker (in which case the caller should retry soon).
func (p *Pipeline) flush(pending map[string]*entry, inflight map[string]bool) (next time.Time, blocked bool) {
	now := p.now()
	var due []*entry
	for _, e := range pending {
		if !e.due.After(now) {
			due = append(due, e)
		} else if next.IsZero() || e.due.Before(next) {
			next = e.due
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].seq < due[j].seq })

	for _, e := range due {
		if inflight[e.ev.Path] {
			continue // its done notification wakes the scheduler
		}
		select {
		case p.work <- e.ev:
			inflight[e.ev.Path] = true
			delete(pending, e.ev.Path)
		default:
			return next, true
		}
	}
	return next, false
}

func (p *Pipeline) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-p.work:
			p.process(ctx, ev)
			select {
			case p.done <- ev.Path:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (p *Pipeline) process(ctx context.Context, ev Event) {
	defer func() {
		if r := recover(); r != nil {
			p.onError(ev, fmt.Errorf("handler panic: %v", r))
		}
	}()
	if err := p.handle(ctx, ev); err != nil && ctx.Err() == nil {
		p.onError(ev, err)
	}
}

// mergeOp combines the op already pending for a path with a newer one.
func mergeOp(old, next Op) Op {
	switch {
	case next == Removed:
		return Removed
	case old == Removed:
		return Modified // removed then recreated: the content was replaced
	case old == Created && next == Modified:
		return Created
	}
	return next
}
