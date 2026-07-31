package kemp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestPromCollectorRendersSamples checks the full exposition for every sample in
// the snapshot, not just kemp_up: it also pins the exact label set (keys, values,
// AND the alphabetical ordering the registry imposes) for a multi-label sample
// (kemp_virtual_service_active_connections' five vsLabels keys) and a second
// single-label sample (kemp_tps). Checking only kemp_up (as in the original draft)
// or only a metric count cannot catch a mislabeled or mis-valued sample elsewhere
// in the snapshot: this asserts the full rendered text so a swapped label, a wrong
// value, or a dropped sample all fail the comparison.
func TestPromCollectorRendersSamples(t *testing.T) {
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

	c := NewPromCollector(store)

	// Full-registry comparison (no metricNames filter): every family present must
	// match exactly, in the label order Prometheus's own MakeLabelPairs imposes
	// (alphabetical by label name), so this also fails if collection emitted an
	// extra or missing series.
	want := `
# HELP kemp_tps Kemp LoadMaster metric kemp_tps
# TYPE kemp_tps gauge
kemp_tps{system="lm-01"} 420
# HELP kemp_up Kemp LoadMaster metric kemp_up
# TYPE kemp_up gauge
kemp_up{system="lm-01"} 1
# HELP kemp_virtual_service_active_connections Kemp LoadMaster metric kemp_virtual_service_active_connections
# TYPE kemp_virtual_service_active_connections gauge
kemp_virtual_service_active_connections{address="10.0.0.10",name="web",port="443",protocol="tcp",system="lm-01"} 42
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Fatalf("unexpected exposition: %v", err)
	}
}

// The empty snapshot present before the first collection cycle must render cleanly,
// because the HTTP server starts before the loop does.
func TestPromCollectorEmptySnapshot(t *testing.T) {
	c := NewPromCollector(NewSnapshotStore())
	if got := testutil.CollectAndCount(c); got != 0 {
		t.Errorf("collected %d metrics from an empty snapshot, want 0", got)
	}
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("Gather on empty snapshot: %v", err)
	}
}

// Describe must emit nothing: that is what makes this an unchecked collector and
// lets the metric-name set change between snapshots.
func TestPromCollectorDescribeIsEmpty(t *testing.T) {
	ch := make(chan *prometheus.Desc, 8)
	NewPromCollector(NewSnapshotStore()).Describe(ch)
	close(ch)
	if n := len(ch); n != 0 {
		t.Errorf("Describe emitted %d descriptors, want 0", n)
	}
}

// Two series of one metric name with different label KEYS would make Gather fail.
// The collector drops the divergent one so a scrape degrades instead of erroring.
// The surviving series is also checked (not just its count): a broken guard that
// dropped the WRONG sample, or corrupted the surviving one's value/labels, would
// still leave len(f.GetMetric()) == 1 and pass a count-only assertion.
func TestPromCollectorDropsLabelKeyDrift(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			{Name: "kemp_thing", Labels: []Label{{Key: "system", Value: "lm-01"}}, Value: 1},
			{Name: "kemp_thing", Labels: []Label{{Key: "other", Value: "x"}}, Value: 2},
		},
	}}})

	reg := prometheus.NewRegistry()
	if err := reg.Register(NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather must not fail on label drift: %v", err)
	}
	var found bool
	for _, f := range families {
		if f.GetName() != "kemp_thing" {
			continue
		}
		found = true
		metrics := f.GetMetric()
		if len(metrics) != 1 {
			t.Fatalf("kemp_thing has %d series, want 1 (the divergent one dropped)", len(metrics))
		}
		labels := metrics[0].GetLabel()
		if len(labels) != 1 || labels[0].GetName() != "system" || labels[0].GetValue() != "lm-01" {
			t.Errorf("kemp_thing survivor labels = %v, want [system=lm-01] (the first-seen schema)", labels)
		}
		if got := metrics[0].GetGauge().GetValue(); got != 1 {
			t.Errorf("kemp_thing survivor value = %v, want 1", got)
		}
	}
	if !found {
		t.Fatal("kemp_thing family not present in gathered output")
	}
}

// Two series of one metric name with identical label KEYS AND VALUES would also
// make Gather fail: client_golang's registry rejects two metrics sharing a name
// and identical label values regardless of whether the collector is checked or
// unchecked (registry.go's uniqueness check runs on every collected metric). This
// is not a hypothetical: a LoadMaster SubVS row carries its parent virtual
// service's VIP address and port, so two st.VirtualServices entries can resolve
// to the same vsKey (derivations.go) and therefore byte-identical vsLabels — two
// identical kemp_virtual_service_active_connections series. The collector must
// drop the duplicate so the scrape degrades (one series survives) instead of
// erroring out the entire appliance fleet's scrape.
func TestPromCollectorDropsDuplicateLabelValues(t *testing.T) {
	store := NewSnapshotStore()
	dupLabels := vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp")
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			{
				Name:   "kemp_virtual_service_active_connections",
				Labels: dupLabels,
				Value:  10,
			},
			{
				// Same name, same label keys AND values as above (e.g. a SubVS row
				// resolving to the same parent VIP) — a duplicate series, not a
				// schema drift.
				Name:   "kemp_virtual_service_active_connections",
				Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
				Value:  99,
			},
		},
	}}})

	reg := prometheus.NewRegistry()
	if err := reg.Register(NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather must not fail on a duplicate label-value tuple: %v", err)
	}
	var found bool
	for _, f := range families {
		if f.GetName() != "kemp_virtual_service_active_connections" {
			continue
		}
		found = true
		metrics := f.GetMetric()
		if len(metrics) != 1 {
			t.Fatalf("kemp_virtual_service_active_connections has %d series, want 1 (the duplicate dropped)", len(metrics))
		}
		if got := metrics[0].GetGauge().GetValue(); got != 10 {
			t.Errorf("surviving series value = %v, want 10 (the first-seen sample)", got)
		}
	}
	if !found {
		t.Fatal("kemp_virtual_service_active_connections family not present in gathered output")
	}
}

// TestPromCollectorGaugeValueForTotalMetric guards the plan's explicit ruling: a
// "_total"-suffixed sample still renders as a GaugeValue, never CounterValue. None
// of the brief's own tests exercise a "_total" name at all, so this closes that
// gap: it pins the "# TYPE ... gauge" line of the actual exposition output, which
// only the specified rendering choice can satisfy — a CounterValue implementation
// would emit "# TYPE ... counter" and fail the comparison.
func TestPromCollectorGaugeValueForTotalMetric(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			{Name: "kemp_virtual_service_bytes_total", Labels: systemLabels("lm-01"), Value: 12345},
		},
	}}})

	want := `
# HELP kemp_virtual_service_bytes_total Kemp LoadMaster metric kemp_virtual_service_bytes_total
# TYPE kemp_virtual_service_bytes_total gauge
kemp_virtual_service_bytes_total{system="lm-01"} 12345
`
	if err := testutil.CollectAndCompare(NewPromCollector(store), strings.NewReader(want)); err != nil {
		t.Fatalf("unexpected exposition for a _total metric: %v", err)
	}
}

// --- Final review, I5: the duplicate-sample Warn was unbounded ---
//
// prometheus.go's own doc comment documents SubVS rows as a NORMAL source of
// byte-identical labels, and SubVSs are a common LoadMaster configuration. So the
// drop-and-warn path is steady state on such an appliance, not an exception: 7
// metrics x N SubVSs x 2 readers, every cycle, forever, into a log file that
// internal/logging never rotates. The drop itself is correct and stays; the
// unbounded logging of it does not.
func TestPromCollectorBoundsDuplicateSampleWarnings(t *testing.T) {
	hook := logrustest.NewGlobal()
	defer hook.Reset()

	labels := vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp")
	store := NewSnapshotStore()
	store.Store(&Snapshot{
		BuiltAt: time.Now(),
		Systems: []*SystemSnapshot{{System: "lm-01", OK: true, Samples: []Sample{
			{Name: "kemp_virtual_service_up", Labels: labels, Value: 1},
			{Name: "kemp_virtual_service_up", Labels: labels, Value: 1}, // the SubVS collision
		}}},
	})

	reg := prometheus.NewRegistry()
	c := NewPromCollector(store)
	if err := reg.Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for range 5 { // five scrapes of an appliance in this steady state
		if _, err := reg.Gather(); err != nil {
			t.Fatalf("Gather: %v", err)
		}
	}

	if got := countWarnings(hook, "duplicate label values"); got != 1 {
		t.Fatalf("%d duplicate-drop Warn lines across 5 scrapes, want exactly 1 "+
			"(unbounded, this is 7 metrics x N SubVSs x 2 readers every cycle forever)", got)
	}
}

func TestOTLPBoundsDuplicateSampleWarnings(t *testing.T) {
	hook := logrustest.NewGlobal()
	defer hook.Reset()

	labels := vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp")
	store := NewSnapshotStore()
	store.Store(&Snapshot{
		BuiltAt: time.Now(),
		Systems: []*SystemSnapshot{{System: "lm-01", OK: true, Samples: []Sample{
			{Name: "kemp_virtual_service_up", Labels: labels, Value: 1},
			{Name: "kemp_virtual_service_up", Labels: labels, Value: 1},
		}}},
	})

	reader := sdkmetric.NewManualReader()
	exp := newOTLPExporter(reader, store, "v0.0.0-test")
	if err := exp.EnsureInstruments(); err != nil {
		t.Fatalf("EnsureInstruments: %v", err)
	}
	for range 5 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("Collect: %v", err)
		}
	}

	if got := countWarnings(hook, "duplicate label values"); got != 1 {
		t.Fatalf("%d duplicate-drop Warn lines across 5 collections, want exactly 1", got)
	}
}

// A DIFFERENT metric name must still get its own line: the bound must suppress
// repetition, never a new condition.
func TestDropWarningsAreScopedPerMetricAndSystem(t *testing.T) {
	hook := logrustest.NewGlobal()
	defer hook.Reset()

	d := newDropWarnings()
	for range 3 {
		d.warn("dup", "kemp_a", "lm-01", logrus.WithField("metric", "kemp_a"), "dropping sample: duplicate label values")
		d.warn("dup", "kemp_b", "lm-01", logrus.WithField("metric", "kemp_b"), "dropping sample: duplicate label values")
		d.warn("dup", "kemp_a", "lm-02", logrus.WithField("metric", "kemp_a"), "dropping sample: duplicate label values")
	}
	if got := countWarnings(hook, "duplicate label values"); got != 3 {
		t.Fatalf("%d Warn lines, want 3 (one per metric-name/system pair, repeats suppressed)", got)
	}
}

func countWarnings(hook *logrustest.Hook, fragment string) int {
	n := 0
	for _, e := range hook.AllEntries() {
		if e.Level == logrus.WarnLevel && strings.Contains(e.Message, fragment) {
			n++
		}
	}
	return n
}
