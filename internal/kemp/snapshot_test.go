package kemp

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLabelBuildersCanonicalOrder(t *testing.T) {
	got := vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp")
	want := []string{"system", "name", "address", "port", "protocol"}
	if len(got) != len(want) {
		t.Fatalf("vsLabels returned %d labels, want %d", len(got), len(want))
	}
	for i, key := range want {
		if got[i].Key != key {
			t.Errorf("vsLabels[%d].Key = %q, want %q", i, got[i].Key, key)
		}
	}
	if got[3].Value != "443" {
		t.Errorf("port label = %q, want \"443\"", got[3].Value)
	}

	rs := rsLabels("lm-01", "192.168.1.20", 8443, "10.0.0.10", 443)
	wantRS := []string{"system", "address", "port", "vs_address", "vs_port"}
	if len(rs) != len(wantRS) {
		t.Fatalf("rsLabels returned %d labels, want %d", len(rs), len(wantRS))
	}
	for i, key := range wantRS {
		if rs[i].Key != key {
			t.Errorf("rsLabels[%d].Key = %q, want %q", i, rs[i].Key, key)
		}
	}
}

// An unresolved virtual-service name must produce an empty VALUE, never a dropped
// key: a metric name must carry one label-key set across all of its series.
func TestVSLabelsKeepsKeyWhenNameEmpty(t *testing.T) {
	got := vsLabels("lm-01", "", "10.0.0.10", 443, "tcp")
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5 even with an empty name", len(got))
	}
	if got[1].Key != "name" || got[1].Value != "" {
		t.Errorf("labels[1] = %+v, want key \"name\" with an empty value", got[1])
	}
}

func TestSnapshotStoreNeverReturnsNil(t *testing.T) {
	s := NewSnapshotStore()
	if s.Load() == nil {
		t.Fatal("Load() returned nil before the first cycle")
	}
	snap := &Snapshot{BuiltAt: time.Now()}
	s.Store(snap)
	if s.Load() != snap {
		t.Fatal("Load() did not return the stored snapshot")
	}
}

func TestSnapshotMetricNamesAndSamplesByName(t *testing.T) {
	snap := &Snapshot{Systems: []*SystemSnapshot{
		{System: "a", Samples: []Sample{
			{Name: "kemp_up", Labels: systemLabels("a"), Value: 1},
			{Name: "kemp_tps", Labels: systemLabels("a"), Value: 10},
		}},
		{System: "b", Samples: []Sample{
			{Name: "kemp_up", Labels: systemLabels("b"), Value: 0},
		}},
	}}

	names := snap.MetricNames()
	if len(names) != 2 {
		t.Fatalf("MetricNames() = %v, want 2 unique names", names)
	}
	// Sorted output keeps OTLP instrument registration deterministic.
	if names[0] != "kemp_tps" || names[1] != "kemp_up" {
		t.Errorf("MetricNames() = %v, want sorted [kemp_tps kemp_up]", names)
	}

	ups := snap.SamplesByName("kemp_up")
	if len(ups) != 2 {
		t.Fatalf("SamplesByName(kemp_up) returned %d, want 2", len(ups))
	}
}

// cpuLabels and interfaceLabels aren't exercised by the vsLabels/rsLabels order
// test above; cover their own canonical order directly.
func TestCPULabelsAndInterfaceLabelsCanonicalOrder(t *testing.T) {
	cpu := cpuLabels("lm-01", "cpu0")
	wantCPU := []string{"system", "cpu"}
	if len(cpu) != len(wantCPU) {
		t.Fatalf("cpuLabels returned %d labels, want %d", len(cpu), len(wantCPU))
	}
	for i, key := range wantCPU {
		if cpu[i].Key != key {
			t.Errorf("cpuLabels[%d].Key = %q, want %q", i, cpu[i].Key, key)
		}
	}
	if cpu[1].Value != "cpu0" {
		t.Errorf("cpu label value = %q, want \"cpu0\"", cpu[1].Value)
	}

	iface := interfaceLabels("lm-01", "eth0")
	wantIface := []string{"system", "interface"}
	// Length first, as in the cpuLabels half above: without it an EXTRA trailing
	// label passes silently, and "extra key" is precisely the regression the
	// label-key union invariant exists to catch.
	if len(iface) != len(wantIface) {
		t.Fatalf("interfaceLabels returned %d labels, want %d", len(iface), len(wantIface))
	}
	for i, key := range wantIface {
		if iface[i].Key != key {
			t.Errorf("interfaceLabels[%d].Key = %q, want %q", i, iface[i].Key, key)
		}
	}
	if iface[1].Value != "eth0" {
		t.Errorf("interface label value = %q, want \"eth0\"", iface[1].Value)
	}
}

// withLabel must return a new slice: mutating its result, or calling it twice from
// the same base, must never let one call's appended label bleed into another's, and
// must never change the caller's base slice. A naive `append(base, ...)`
// implementation can pass every other test here and still corrupt a sibling sample
// whenever base happens to have spare capacity.
func TestWithLabelDoesNotMutateOrAliasBase(t *testing.T) {
	base := vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp")
	baseLen := len(base)

	up := withLabel(base, "status", "up")
	down := withLabel(base, "status", "down")

	if len(base) != baseLen {
		t.Fatalf("withLabel mutated base length: got %d, want %d", len(base), baseLen)
	}
	for i, l := range base {
		if l.Key == "status" {
			t.Fatalf("withLabel added a %q key to base at index %d", l.Key, i)
		}
	}

	if got := up[len(up)-1]; got.Key != "status" || got.Value != "up" {
		t.Errorf("first withLabel result trailing label = %+v, want {status up}", got)
	}
	if got := down[len(down)-1]; got.Key != "status" || got.Value != "down" {
		t.Errorf("second withLabel result trailing label = %+v, want {status down}", got)
	}
	// Confirm the two results don't share a backing array: mutating one must not
	// change the other.
	up[len(up)-1].Value = "mutated"
	if down[len(down)-1].Value != "down" {
		t.Errorf("mutating one withLabel result changed the other: got %q", down[len(down)-1].Value)
	}
}

// Store and Load are documented as safe for concurrent use: the collection loop
// calls Store once per cycle while HTTP scrapes and the OTLP reader call Load
// concurrently with it and with each other. A purely sequential Store-then-Load
// test can't exercise that guarantee at all, so drive many goroutines at once and
// run this under `go test -race`.
func TestSnapshotStoreConcurrentStoreAndLoad(t *testing.T) {
	s := NewSnapshotStore()

	const writers = 8
	const readers = 8
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				s.Store(&Snapshot{
					BuiltAt: time.Now(),
					Systems: []*SystemSnapshot{
						{System: fmt.Sprintf("writer-%d-%d", id, i)},
					},
				})
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				snap := s.Load()
				if snap == nil {
					t.Error("Load() returned nil during concurrent access")
					return
				}
				// Touch the fields a real reader would, so -race has something to
				// catch if a writer ever mutated a snapshot in place instead of
				// swapping in a new one.
				_ = snap.MetricNames()
			}
		}()
	}

	wg.Wait()
}
