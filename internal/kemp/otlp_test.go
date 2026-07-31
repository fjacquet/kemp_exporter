package kemp

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collectOTLP registers instruments and returns the gathered metrics by name.
func collectOTLP(t *testing.T, store *SnapshotStore) map[string]metricdata.Metrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments: %v", err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func seededStore() *SnapshotStore {
	store := NewSnapshotStore()
	store.Store(&Snapshot{
		BuiltAt: time.Now(),
		Systems: []*SystemSnapshot{{
			System: "lm-01",
			OK:     true,
			Samples: []Sample{
				upSample("lm-01", true),
				{Name: "kemp_tps", Labels: systemLabels("lm-01"), Value: 420},
				{
					Name:   "kemp_virtual_service_active_connections",
					Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
					Value:  42,
				},
			},
		}},
	})
	return store
}

// singleGauge returns the lone data point of a Gauge[float64] metric, failing the
// test if the metric is absent, is not a float64 gauge, or does not carry exactly
// one data point.
func singleGauge(t *testing.T, got map[string]metricdata.Metrics, name string) metricdata.DataPoint[float64] {
	t.Helper()
	m, ok := got[name]
	if !ok {
		t.Fatalf("%s missing from OTLP output; got %v", name, keysOf(got))
	}
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("%s data type = %T, want Gauge[float64]", name, m.Data)
	}
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("%s has %d data points, want 1", name, len(gauge.DataPoints))
	}
	return gauge.DataPoints[0]
}

func TestOTLPExportsEverySample(t *testing.T) {
	got := collectOTLP(t, seededStore())
	for _, name := range []string{"kemp_up", "kemp_tps", "kemp_virtual_service_active_connections"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s missing from OTLP output; got %v", name, keysOf(got))
		}
	}

	// Presence alone cannot catch a swapped or zeroed-out value: the brief's own
	// version of this test only checked that each name appeared. Pin the exact
	// value of the two single-series metrics here (the third, multi-attribute
	// series, is checked in full by TestOTLPCarriesLabelsAsAttributes below).
	if dp := singleGauge(t, got, "kemp_up"); dp.Value != 1 {
		t.Errorf("kemp_up value = %v, want 1", dp.Value)
	}
	if dp := singleGauge(t, got, "kemp_tps"); dp.Value != 420 {
		t.Errorf("kemp_tps value = %v, want 420", dp.Value)
	}
}

func TestOTLPCarriesLabelsAsAttributes(t *testing.T) {
	got := collectOTLP(t, seededStore())
	m, ok := got["kemp_virtual_service_active_connections"]
	if !ok {
		t.Fatal("metric missing")
	}
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("data type = %T, want Gauge[float64]", m.Data)
	}
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("%d data points, want 1", len(gauge.DataPoints))
	}
	dp := gauge.DataPoints[0]
	if dp.Value != 42 {
		t.Errorf("value = %v, want 42", dp.Value)
	}
	if n := dp.Attributes.Len(); n != 5 {
		t.Errorf("%d attributes, want 5 (system,name,address,port,protocol)", n)
	}
	wantAttrs := map[string]string{
		"system":   "lm-01",
		"name":     "web",
		"address":  "10.0.0.10",
		"port":     "443",
		"protocol": "tcp",
	}
	for key, want := range wantAttrs {
		v, ok := dp.Attributes.Value(attribute.Key(key))
		if !ok || v.AsString() != want {
			t.Errorf("%s attribute = %v/%v, want %v", key, v.AsString(), ok, want)
		}
	}
}

// EnsureInstruments runs after every cycle, so it must be idempotent.
//
// Mutation-tested caveat, recorded here rather than silently: removing the
// dedup guard in EnsureInstruments does NOT make this test fail. The OTel SDK
// tolerates re-registering an identical name/kind/unit/description gauge
// without erroring, and because every duplicate registration's callback reads
// the same store and observes the same attribute set, the SDK's
// last-value-per-attribute-set gauge aggregation collapses the repeats into
// exactly one data point regardless of how many times the instrument was
// created — confirmed by instrumenting the callback and otel's global error
// handler during mutation testing (see task-13-report.md). So this test (and
// TestOTLPEnsureInstrumentsConcurrentSafe below) verify that the *exported
// data* stays correct under repeated calls, not that the guard itself ran —
// the guard's actual purpose (preventing an unbounded number of redundant
// Float64ObservableGauge registrations from accumulating over the life of a
// long-running process, one per name per cycle) has no black-box signal
// reachable from outside this package to assert on directly.
func TestOTLPEnsureInstrumentsIdempotent(t *testing.T) {
	store := seededStore()
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	for i := 0; i < 3; i++ {
		if err := exp.EnsureInstruments(); err != nil {
			t.Fatalf("EnsureInstruments #%d: %v", i, err)
		}
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	count := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "kemp_up" {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("kemp_up registered %d times, want 1", count)
	}
}

// The brief's TestOTLPEnsureInstrumentsIdempotent only ever calls EnsureInstruments
// against an unchanged store, so a guard that registered instruments once at
// construction time and then ignored every later name would still pass it. A real
// LoadMaster's virtual-service set grows and shrinks between cycles, so
// EnsureInstruments must also pick up a name that appears for the first time on a
// later cycle.
func TestOTLPEnsureInstrumentsPicksUpNewMetricNames(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System:  "lm-01",
		Samples: []Sample{upSample("lm-01", true)},
	}}})
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments #1: %v", err)
	}

	// A second collection cycle: a virtual service was added that did not exist
	// in the first cycle's snapshot.
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			upSample("lm-01", true),
			{
				Name:   "kemp_virtual_service_active_connections",
				Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
				Value:  7,
			},
		},
	}}})
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments #2: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	dp := singleGauge(t, out, "kemp_virtual_service_active_connections")
	if dp.Value != 7 {
		t.Errorf("kemp_virtual_service_active_connections value = %v, want 7", dp.Value)
	}
}

// The observable-gauge callback must read the CURRENT snapshot at collection time,
// not one captured when the instrument was registered. This is the property that
// makes the OTLP path pull-based rather than a one-shot snapshot of whatever the
// store held at EnsureInstruments time: store one snapshot, collect, store a
// DIFFERENT snapshot, collect again, and confirm the second collection reflects the
// second snapshot's value. A callback that closed over the snapshot captured at
// registration would report 100 forever, and a test that only collected once (as
// the brief's own tests do) could never catch that bug.
func TestOTLPCallbackObservesSnapshotAtCollectionTimeNotRegistrationTime(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System:  "lm-01",
		Samples: []Sample{{Name: "kemp_tps", Labels: systemLabels("lm-01"), Value: 100}},
	}}})
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments: %v", err)
	}

	var rm1 metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm1); err != nil {
		t.Fatalf("Collect #1: %v", err)
	}
	out1 := map[string]metricdata.Metrics{}
	for _, sm := range rm1.ScopeMetrics {
		for _, m := range sm.Metrics {
			out1[m.Name] = m
		}
	}
	if dp := singleGauge(t, out1, "kemp_tps"); dp.Value != 100 {
		t.Fatalf("first collection kemp_tps = %v, want 100", dp.Value)
	}

	// Next collection cycle: the store now holds a different snapshot entirely.
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System:  "lm-01",
		Samples: []Sample{{Name: "kemp_tps", Labels: systemLabels("lm-01"), Value: 200}},
	}}})

	var rm2 metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm2); err != nil {
		t.Fatalf("Collect #2: %v", err)
	}
	out2 := map[string]metricdata.Metrics{}
	for _, sm := range rm2.ScopeMetrics {
		for _, m := range sm.Metrics {
			out2[m.Name] = m
		}
	}
	if dp := singleGauge(t, out2, "kemp_tps"); dp.Value != 200 {
		t.Errorf("second collection kemp_tps = %v, want 200 (fresh snapshot); a callback that closed over the first snapshot would still report 100", dp.Value)
	}
}

// A sample that disappears from the snapshot (e.g. a virtual service was deleted
// on the LoadMaster) must stop being reported — never fall back to reporting a
// fabricated 0, and never keep reporting the last value it held. The instrument
// itself is allowed to remain registered (OTel's stable metric API has no supported
// way to fully retire a single async instrument's callback once created), but it
// must contribute zero data points once its underlying sample is gone.
func TestOTLPMetricAbsentAfterDisappearingFromSnapshot(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			upSample("lm-01", true),
			{
				Name:   "kemp_virtual_service_active_connections",
				Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
				Value:  42,
			},
		},
	}}})
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments #1: %v", err)
	}

	var rm1 metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm1); err != nil {
		t.Fatalf("Collect #1: %v", err)
	}
	out1 := map[string]metricdata.Metrics{}
	for _, sm := range rm1.ScopeMetrics {
		for _, m := range sm.Metrics {
			out1[m.Name] = m
		}
	}
	if dp := singleGauge(t, out1, "kemp_virtual_service_active_connections"); dp.Value != 42 {
		t.Fatalf("first collection value = %v, want 42", dp.Value)
	}

	// The virtual service is gone from the next cycle's snapshot.
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System:  "lm-01",
		Samples: []Sample{upSample("lm-01", true)},
	}}})
	// EnsureInstruments must remain a safe no-op for a name it already knows,
	// even though that name is momentarily absent from the snapshot.
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments #2: %v", err)
	}

	var rm2 metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm2); err != nil {
		t.Fatalf("Collect #2: %v", err)
	}
	for _, sm := range rm2.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "kemp_virtual_service_active_connections" {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[float64])
			if !ok {
				t.Fatalf("data type = %T, want Gauge[float64]", m.Data)
			}
			for _, dp := range gauge.DataPoints {
				if dp.Value == 42 {
					t.Errorf("stale value 42 still reported after the sample vanished from the snapshot")
				}
			}
			if len(gauge.DataPoints) != 0 {
				t.Errorf("%d data points remained after the sample vanished, want 0", len(gauge.DataPoints))
			}
		}
	}
}

// EnsureInstruments is called by the collection loop after every cycle and could in
// principle race with a concurrent OTLP Collect from the periodic reader's own
// goroutine. Drive many goroutines calling EnsureInstruments (against a store also
// being written concurrently) at once, under -race, so a missing mutex around the
// registered-names map would be caught rather than passing by luck on a
// single-goroutine call sequence. Confirmed by mutation-testing: removing e.mu's
// Lock/Unlock around the map access makes `go test -race` report a genuine data
// race here (concurrent map read/write) — this is the property this test actually
// proves; see TestOTLPEnsureInstrumentsIdempotent's comment for what the final
// count==1 assertion below does and does not prove.
func TestOTLPEnsureInstrumentsConcurrentSafe(t *testing.T) {
	store := seededStore()
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")

	const goroutines = 8
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if err := exp.EnsureInstruments(); err != nil {
					t.Errorf("EnsureInstruments: %v", err)
				}
			}
		}()
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				store.Store(&Snapshot{Systems: []*SystemSnapshot{{
					System:  "lm-01",
					Samples: []Sample{upSample("lm-01", true)},
				}}})
			}
			_ = id
		}(i)
	}
	wg.Wait()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	count := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "kemp_up" {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("kemp_up registered %d times under concurrent EnsureInstruments calls, want 1", count)
	}
}

// Shutdown must flush and stop the meter provider cleanly even when the reader is
// the test double: main.go's shutdown path has nothing left to react to a
// Shutdown error but the call itself must not hang or panic.
func TestOTLPShutdown(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, NewSnapshotStore(), "v0.0.0-test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exp.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func keysOf(m map[string]metricdata.Metrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
