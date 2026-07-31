package kemp

import (
	"sort"
	"sync"
	"time"
)

// SystemSnapshot is one LoadMaster's result for a single collection cycle.
type SystemSnapshot struct {
	System        string
	LastScrape    time.Time
	OK            bool   // the appliance was reachable and authenticated
	Err           string // top-level failure; empty when OK
	TransportName string // "xml" or "json"
	Samples       []Sample
}

// Snapshot is an immutable, point-in-time view across every configured LoadMaster.
//
// A Snapshot is built whole and then only ever read. Nothing in this package
// mutates a *Snapshot (or a *SystemSnapshot, or a Samples/Labels slice reachable
// from one) once it has been handed to SnapshotStore.Store: the collection loop
// must construct an entirely new Snapshot, with new Systems/Samples/Labels slices,
// each cycle and swap it in wholesale. That discipline — never append to or edit a
// slice reachable from a snapshot a reader might already hold — is what keeps a
// concurrent reader from ever observing a half-built or since-changed snapshot,
// independent of the mutex in SnapshotStore.
type Snapshot struct {
	BuiltAt time.Time
	Systems []*SystemSnapshot
}

// MetricNames returns every distinct metric name in the snapshot, sorted.
// Sorting keeps OTLP instrument registration deterministic across cycles.
func (s *Snapshot) MetricNames() []string {
	seen := map[string]struct{}{}
	for _, sys := range s.Systems {
		for _, sample := range sys.Samples {
			seen[sample.Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SamplesByName returns every sample carrying the given metric name, across every
// system in the snapshot. The returned slice is freshly allocated by this call, so
// appending to it never affects the snapshot.
func (s *Snapshot) SamplesByName(name string) []Sample {
	var out []Sample
	for _, sys := range s.Systems {
		for _, sample := range sys.Samples {
			if sample.Name == name {
				out = append(out, sample)
			}
		}
	}
	return out
}

// SnapshotStore holds the latest Snapshot behind an RWMutex-guarded pointer swap.
// Store and Load are safe for concurrent use by multiple goroutines: the collection
// loop calls Store once per cycle while HTTP scrapes and the OTLP reader call Load
// concurrently with it and with each other. The lock protects only the pointer
// itself — Load returns it immediately after copying it out, so a slow reader
// holding a *Snapshot never blocks the next Store.
type SnapshotStore struct {
	mu   sync.RWMutex
	snap *Snapshot
}

// NewSnapshotStore returns a store pre-populated with an empty snapshot so readers
// never see nil before the first collection cycle completes.
func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{snap: &Snapshot{}}
}

// Store atomically swaps in a new snapshot. Callers must pass a fully built
// Snapshot and must not mutate it afterward: see the Snapshot doc comment.
func (s *SnapshotStore) Store(snap *Snapshot) {
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

// Load returns the current snapshot. Never nil.
func (s *SnapshotStore) Load() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}
