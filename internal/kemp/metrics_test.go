package kemp

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// metrics.go is the SOLE schema authority in this exporter: every []Label in the
// tree is built by one of these functions, PromCollector.Describe emits nothing (so
// the registry never runs checkDescConsistency), and the label-key union invariant
// therefore rests entirely on the key SEQUENCE each builder returns. Until this
// file existed, none of them had a direct unit test.
//
// The assertion is on the exact sequence, not the set: the sequence is what pins
// the canonical order that the collectors' first-sample-wins schema rule compares
// every later sample against.
func TestLabelBuilderKeySequences(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  []Label
		want []string
	}{
		{"systemLabels", systemLabels("lm-01"), []string{"system"}},
		{"cpuLabels", cpuLabels("lm-01", "cpu0"), []string{"system", "cpu"}},
		{"interfaceLabels", interfaceLabels("lm-01", "eth0"), []string{"system", "interface"}},
		{
			"vsLabels",
			vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
			[]string{"system", "name", "address", "port", "protocol"},
		},
		{
			"rsLabels",
			rsLabels("lm-01", "192.168.1.20", 8443, "10.0.0.10", 443),
			[]string{"system", "address", "port", "vs_address", "vs_port"},
		},
		{
			"vsLabels+status",
			withLabel(vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"), "status", "Up"),
			[]string{"system", "name", "address", "port", "protocol", "status"},
		},
		{
			"rsLabels+status",
			withLabel(rsLabels("lm-01", "192.168.1.20", 8443, "10.0.0.10", 443), "status", "Up"),
			[]string{"system", "address", "port", "vs_address", "vs_port", "status"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.got) != len(tc.want) {
				t.Fatalf("%s returned %d labels (%v), want %d (%v)",
					tc.name, len(tc.got), labelKeys(tc.got), len(tc.want), tc.want)
			}
			for i, key := range tc.want {
				if tc.got[i].Key != key {
					t.Errorf("%s[%d].Key = %q, want %q (full sequence %v, want %v)",
						tc.name, i, tc.got[i].Key, key, labelKeys(tc.got), tc.want)
				}
			}
		})
	}
}

// An unresolved value must stay an empty VALUE and never drop its KEY: one metric
// name carries one label-key set across every one of its series, or the collectors
// drop the divergent ones.
func TestLabelBuildersKeepKeysForEmptyValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  []Label
		want int
	}{
		{"cpuLabels", cpuLabels("", ""), 2},
		{"interfaceLabels", interfaceLabels("", ""), 2},
		{"vsLabels", vsLabels("", "", "", 0, ""), 5},
		{"rsLabels", rsLabels("", "", 0, "", 0), 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.got) != tc.want {
				t.Fatalf("%s with every value empty returned %d labels, want %d", tc.name, len(tc.got), tc.want)
			}
		})
	}
}

func labelKeys(labels []Label) []string {
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = l.Key
	}
	return out
}

// --- Final review, I2: invalid UTF-8 diverged the two readers ---
//
// Label values are appliance-supplied (virtual-service name, cpu id, interface id,
// status, addresses) and nothing validated them. prometheus.NewConstMetric rejects
// non-UTF-8 via validateLabelValues, so the Prometheus reader dropped the offending
// series and warned; the OTLP reader had no equivalent and exported it, and the
// per-REQUEST protobuf marshal then failed with "string field contains invalid
// UTF-8" -- losing EVERY metric in that export batch, every cycle, for as long as
// the appliance reported that name. For an OTLP-only deployment that is total
// metric loss behind one export-error log.
//
// The fix is upstream of both readers, in the builders, so neither reader decides
// this independently and the two cannot drift apart again.
func TestLabelBuildersReplaceInvalidUTF8Values(t *testing.T) {
	bad := "web\xff\xfe"
	for _, tc := range []struct {
		name  string
		got   []Label
		index int
	}{
		{"cpuLabels", cpuLabels("lm-01", bad), 1},
		{"interfaceLabels", interfaceLabels("lm-01", bad), 1},
		{"vsLabels", vsLabels("lm-01", bad, "10.0.0.10", 443, "tcp"), 1},
		{"vsLabels system", vsLabels(bad, "web", "10.0.0.10", 443, "tcp"), 0},
		{"rsLabels", rsLabels("lm-01", bad, 8443, "10.0.0.10", 443), 1},
		{"withLabel status", withLabel(systemLabels("lm-01"), "status", bad), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.got[tc.index].Value
			if !utf8.ValidString(v) {
				t.Fatalf("%s kept an invalid-UTF-8 value %q; both readers must see clean data", tc.name, v)
			}
			if !strings.Contains(v, "�") {
				t.Errorf("%s value = %q, want the invalid bytes replaced with U+FFFD "+
					"(dropping the sample would lose a real series; a fabricated value would lie)", tc.name, v)
			}
		})
	}
}

// A valid value must survive byte-identically: sanitising must not normalise,
// escape or otherwise rewrite the appliance's own strings.
func TestLabelBuildersLeaveValidValuesUntouched(t *testing.T) {
	name := "wéb-https_01 (prod)"
	got := vsLabels("lm-01", name, "10.0.0.10", 443, "tcp")
	if got[1].Value != name {
		t.Errorf("vsLabels rewrote a valid value: %q, want %q", got[1].Value, name)
	}
}

// The end-to-end statement of I2: one snapshot, one bad label value, both readers.
// They must agree on the series count, and the OTLP attributes must be valid UTF-8
// (an invalid one fails the protobuf marshal of the whole export request).
func TestBothReadersAgreeOnInvalidUTF8LabelValue(t *testing.T) {
	store := NewSnapshotStore()
	store.Store(&Snapshot{
		BuiltAt: time.Now(),
		Systems: []*SystemSnapshot{{
			System: "lm-01",
			OK:     true,
			Samples: []Sample{
				{Name: "kemp_virtual_service_up", Labels: vsLabels("lm-01", "ok", "10.0.0.10", 80, "tcp"), Value: 1},
				{Name: "kemp_virtual_service_up", Labels: vsLabels("lm-01", "bad\xff\xfe", "10.0.0.11", 443, "tcp"), Value: 1},
			},
		}},
	})

	reg := prometheus.NewRegistry()
	if err := reg.Register(NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	promSeries := 0
	for _, f := range families {
		if f.GetName() == "kemp_virtual_service_up" {
			promSeries = len(f.GetMetric())
		}
	}

	otlpSeries := 0
	for _, m := range collectOTLP(t, store) {
		if m.Name != "kemp_virtual_service_up" {
			continue
		}
		gauge, ok := m.Data.(metricdata.Gauge[float64])
		if !ok {
			t.Fatalf("kemp_virtual_service_up data type = %T, want Gauge[float64]", m.Data)
		}
		for _, dp := range gauge.DataPoints {
			otlpSeries++
			for _, kv := range dp.Attributes.ToSlice() {
				if !utf8.ValidString(kv.Value.AsString()) {
					t.Errorf("OTLP attribute %s = %q is not valid UTF-8; the protobuf marshal of this "+
						"export request fails, losing every metric in the batch", kv.Key, kv.Value.AsString())
				}
			}
		}
	}

	if promSeries != 2 || otlpSeries != 2 {
		t.Fatalf("readers disagree or dropped a series: prometheus %d, otlp %d, want 2 and 2",
			promSeries, otlpSeries)
	}
}
