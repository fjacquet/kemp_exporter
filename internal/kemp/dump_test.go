package kemp

import (
	"strings"
	"testing"
)

// The --once --debug dump is the live-validation tool: its output gets diffed
// against docs/metrics.md. Sorted, exposition-style, one sample per line.
func TestDumpSamplesIsSortedExposition(t *testing.T) {
	snap := &Snapshot{Systems: []*SystemSnapshot{{
		System: "lm-01",
		Samples: []Sample{
			{Name: "kemp_tps", Labels: systemLabels("lm-01"), Value: 420},
			{Name: "kemp_up", Labels: systemLabels("lm-01"), Value: 1},
			{
				Name:   "kemp_virtual_service_active_connections",
				Labels: vsLabels("lm-01", "web", "10.0.0.10", 443, "tcp"),
				Value:  42,
			},
		},
	}}}

	var sb strings.Builder
	DumpSamples(&sb, snap)
	lines := strings.Split(strings.TrimSpace(sb.String()), "\n")

	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3:\n%s", len(lines), sb.String())
	}
	if lines[0] != `kemp_tps{system="lm-01"} 420` {
		t.Errorf("line 0 = %q", lines[0])
	}
	if lines[1] != `kemp_up{system="lm-01"} 1` {
		t.Errorf("line 1 = %q", lines[1])
	}
	want := `kemp_virtual_service_active_connections{system="lm-01",name="web",address="10.0.0.10",port="443",protocol="tcp"} 42`
	if lines[2] != want {
		t.Errorf("line 2 =\n%q\nwant\n%q", lines[2], want)
	}
}
