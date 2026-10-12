// Package decisionstest provides an in-memory decisions store for tests, and a
// contract test that every Store implementation (the in-memory one and the
// SQLite one) must pass. It is not for production use.
package decisionstest

import (
	"context"
	"sort"
	"sync"

	"github.com/ameetmadan/arca/internal/decisions"
	"github.com/ameetmadan/arca/internal/policy"
)

// MemStore implements decisions.Store, decisions.PolicyStore and
// policy.StatsProvider in memory.
type MemStore struct {
	mu       sync.Mutex
	records  []decisions.Record
	policies map[string]policy.Settings
}

var (
	_ decisions.Store       = (*MemStore)(nil)
	_ decisions.PolicyStore = (*MemStore)(nil)
	_ policy.StatsProvider  = (*MemStore)(nil)
)

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{policies: map[string]policy.Settings{}}
}

// Insert implements decisions.Store.
func (m *MemStore) Insert(_ context.Context, r decisions.Record) (decisions.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = int64(len(m.records) + 1)
	r.JournalID = cloneInt64(r.JournalID)
	m.records = append(m.records, r)
	return clone(r), nil
}

// Get implements decisions.Store.
func (m *MemStore) Get(_ context.Context, id int64) (decisions.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || int(id) > len(m.records) {
		return decisions.Record{}, decisions.ErrNotFound
	}
	return clone(m.records[id-1]), nil
}

// List implements decisions.Store.
func (m *MemStore) List(_ context.Context, f decisions.ListFilter) (decisions.Page, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var match []decisions.Record
	for _, r := range m.records {
		if (f.Status == "" || r.Status == f.Status) &&
			(f.Root == "" || r.Root == f.Root) && (f.File == "" || r.File == f.File) {
			match = append(match, clone(r))
		}
	}
	sort.SliceStable(match, func(i, j int) bool { return match[i].ID > match[j].ID })
	page := decisions.Page{Items: []decisions.Record{}, Total: len(match), Limit: f.Limit, Offset: f.Offset}
	if f.Offset >= len(match) {
		return page, nil
	}
	end := len(match)
	if f.Limit > 0 && f.Offset+f.Limit < end {
		end = f.Offset + f.Limit
	}
	page.Items = match[f.Offset:end]
	return page, nil
}

// Transition implements decisions.Store.
func (m *MemStore) Transition(_ context.Context, id int64, from, to decisions.Status, u decisions.Update) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || int(id) > len(m.records) {
		return decisions.ErrNotFound
	}
	r := &m.records[id-1]
	if r.Status != from {
		return decisions.ErrWrongStatus
	}
	r.Status = to
	if u.JournalID != nil {
		r.JournalID = cloneInt64(u.JournalID)
	}
	if u.Target != nil {
		r.Target = *u.Target
	}
	return nil
}

// GetPolicy implements decisions.PolicyStore.
func (m *MemStore) GetPolicy(_ context.Context, root string) (policy.Settings, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.policies[root]
	return s, ok, nil
}

// PutPolicy implements decisions.PolicyStore.
func (m *MemStore) PutPolicy(_ context.Context, root string, s policy.Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies[root] = s
	return nil
}

// AcceptanceStats implements policy.StatsProvider: accepted and rejected
// records of root count as decided; applied, failed and pending ones do not.
func (m *MemStore) AcceptanceStats(_ context.Context, root string) (policy.Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var st policy.Stats
	for _, r := range m.records {
		if r.Root != root {
			continue
		}
		switch r.Status {
		case decisions.StatusAccepted:
			st.Decided++
			st.Accepted++
		case decisions.StatusRejected:
			st.Decided++
		}
	}
	return st, nil
}

func clone(r decisions.Record) decisions.Record {
	r.JournalID = cloneInt64(r.JournalID)
	return r
}

func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
