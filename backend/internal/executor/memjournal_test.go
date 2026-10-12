package executor

import (
	"context"
	"sync"
	"time"
)

// memJournal is an in-memory Journal fake.
type memJournal struct {
	mu        sync.Mutex
	entries   map[int64]JournalEntry
	next      int64
	appendErr error
}

func newMemJournal() *memJournal { return &memJournal{entries: map[int64]JournalEntry{}} }

func (m *memJournal) Append(_ context.Context, e JournalEntry) (JournalEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.appendErr != nil {
		return JournalEntry{}, m.appendErr
	}
	m.next++
	e.ID = m.next
	m.entries[e.ID] = e
	return e, nil
}

func (m *memJournal) Get(_ context.Context, id int64) (JournalEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return JournalEntry{}, ErrJournalEntryNotFound
	}
	return e, nil
}

func (m *memJournal) MarkUndone(_ context.Context, id int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return ErrJournalEntryNotFound
	}
	e.UndoneAt = &at
	m.entries[id] = e
	return nil
}
