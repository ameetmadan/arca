// Package storetest is the behavioural test suite every semantic.FolderStore
// must pass, so the in-memory and SQLite stores are held to the same contract.
package storetest

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/ameetmadan/arca/pkg/decision/semantic"
)

// Run runs the suite against stores returned by newStore. Each subtest gets a
// fresh, empty store.
func Run(t *testing.T, newStore func(t *testing.T) semantic.FolderStore) {
	t.Helper()
	for name, fn := range map[string]func(*testing.T, semantic.FolderStore){
		"CRUD":                  testCRUD,
		"RoundTripsVectors":     testRoundTripsVectors,
		"PutReplacesExamples":   testPutReplacesExamples,
		"HonoursContext":        testHonoursContext,
		"ConcurrentUse":         testConcurrentUse,
		"KeysAreRootAndRelBoth": testKeys,
	} {
		t.Run(name, func(t *testing.T) { fn(t, newStore(t)) })
	}
}

func testCRUD(t *testing.T, s semantic.FolderStore) {
	ctx := context.Background()

	if _, ok, err := s.Get(ctx, "/r", "a"); err != nil || ok {
		t.Fatalf("Get on empty store = ok %v, err %v", ok, err)
	}
	if all, err := s.List(ctx); err != nil || len(all) != 0 {
		t.Fatalf("List on empty store = %d folders, err %v", len(all), err)
	}

	a := semantic.FolderIndex{Root: "/r", Rel: "a", FileCount: 2, Fingerprint: "f1", Centroid: []float32{1, 0}}
	b := semantic.FolderIndex{Root: "/r", Rel: "b", FileCount: 1, Centroid: []float32{0, 1}}
	other := semantic.FolderIndex{Root: "/other", Rel: "a", FileCount: 1, Centroid: []float32{0, 1}}
	for _, f := range []semantic.FolderIndex{a, b, other} {
		if err := s.Put(ctx, f); err != nil {
			t.Fatalf("Put(%s %s): %v", f.Root, f.Rel, err)
		}
	}

	got, ok, err := s.Get(ctx, "/r", "a")
	if err != nil || !ok || got.Fingerprint != "f1" || got.FileCount != 2 {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}

	all, err := s.List(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("List = %d folders, err %v; want 3", len(all), err)
	}
	// List is ordered so results are deterministic.
	if all[0].Root != "/other" || all[1].Rel != "a" || all[2].Rel != "b" {
		t.Errorf("List order = %v %v %v", all[0], all[1], all[2])
	}

	// Put replaces.
	a.Fingerprint = "f2"
	if err := s.Put(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(ctx, "/r", "a"); got.Fingerprint != "f2" {
		t.Errorf("Put did not replace: %+v", got)
	}
	if all, _ := s.List(ctx); len(all) != 3 {
		t.Errorf("Put of an existing folder changed the count to %d", len(all))
	}

	if err := s.Delete(ctx, "/r", "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "/r", "a"); ok {
		t.Error("folder still present after Delete")
	}
	if err := s.Delete(ctx, "/r", "missing"); err != nil {
		t.Errorf("Delete of a missing folder = %v, want nil", err)
	}
}

func testRoundTripsVectors(t *testing.T, s semantic.FolderStore) {
	ctx := context.Background()
	want := semantic.FolderIndex{
		Root: "/r", Rel: "Taxes/2025", FileCount: 9, Fingerprint: "fp",
		Centroid: []float32{0.6, 0.8, 0},
		Examples: []semantic.Example{
			{Name: "z.pdf", Vector: []float32{1, 0, 0}},
			{Name: "a.pdf", Vector: []float32{0, 0.5, -0.5}},
		},
	}
	if err := s.Put(ctx, want); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.Get(ctx, "/r", "Taxes/2025")
	if err != nil || !ok {
		t.Fatalf("Get = ok %v, err %v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get =\n%+v\nwant\n%+v", got, want)
	}
	// Example order is the order they were stored in, not name order.
	all, err := s.List(ctx)
	if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0], want) {
		t.Errorf("List = %+v, err %v; want [%+v]", all, err, want)
	}
}

func testPutReplacesExamples(t *testing.T, s semantic.FolderStore) {
	ctx := context.Background()
	f := semantic.FolderIndex{
		Root: "/r", Rel: "a", Centroid: []float32{1, 0},
		Examples: []semantic.Example{
			{Name: "1", Vector: []float32{1, 0}},
			{Name: "2", Vector: []float32{1, 0}},
		},
	}
	if err := s.Put(ctx, f); err != nil {
		t.Fatal(err)
	}
	f.Examples = f.Examples[:1]
	if err := s.Put(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(ctx, "/r", "a")
	if len(got.Examples) != 1 || got.Examples[0].Name != "1" {
		t.Errorf("examples after replace = %+v, want only %q", got.Examples, "1")
	}

	// A folder with no usable files keeps no examples at all.
	f.Centroid, f.Examples = nil, nil
	if err := s.Put(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.Get(ctx, "/r", "a")
	if !ok || len(got.Examples) != 0 || len(got.Centroid) != 0 {
		t.Errorf("folder with no vectors = %+v, ok %v", got, ok)
	}
}

func testHonoursContext(t *testing.T, s semantic.FolderStore) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Put(ctx, semantic.FolderIndex{Root: "/r", Rel: "a"}); err == nil {
		t.Error("Put with cancelled context = nil error")
	}
	if _, err := s.List(ctx); err == nil {
		t.Error("List with cancelled context = nil error")
	}
	if _, _, err := s.Get(ctx, "/r", "a"); err == nil {
		t.Error("Get with cancelled context = nil error")
	}
	if err := s.Delete(ctx, "/r", "a"); err == nil {
		t.Error("Delete with cancelled context = nil error")
	}
}

// Readers must never see a folder half-updated while a writer replaces it.
func testConcurrentUse(t *testing.T, s semantic.FolderStore) {
	ctx := context.Background()
	mk := func(n int) semantic.FolderIndex {
		f := semantic.FolderIndex{Root: "/r", Rel: "a", FileCount: n, Centroid: []float32{1, 0}}
		for i := 0; i < n; i++ {
			f.Examples = append(f.Examples, semantic.Example{Name: "x", Vector: []float32{1, 0}})
		}
		return f
	}
	if err := s.Put(ctx, mk(3)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if err := s.Put(ctx, mk(1+i%5)); err != nil {
				t.Errorf("Put: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			got, ok, err := s.Get(ctx, "/r", "a")
			if err != nil || !ok {
				t.Errorf("Get = ok %v, err %v", ok, err)
				return
			}
			if len(got.Examples) != got.FileCount {
				t.Errorf("torn read: %d examples for FileCount %d", len(got.Examples), got.FileCount)
				return
			}
		}
	}()
	wg.Wait()
}

func testKeys(t *testing.T, s semantic.FolderStore) {
	ctx := context.Background()
	for _, f := range []semantic.FolderIndex{
		{Root: "/a", Rel: "x", Fingerprint: "ax"},
		{Root: "/b", Rel: "x", Fingerprint: "bx"},
	} {
		if err := s.Put(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := s.Get(ctx, "/a", "x"); got.Fingerprint != "ax" {
		t.Errorf("Get(/a, x) = %+v", got)
	}
	if err := s.Delete(ctx, "/a", "x"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "/b", "x"); !ok {
		t.Error("deleting /a x removed /b x")
	}
}
