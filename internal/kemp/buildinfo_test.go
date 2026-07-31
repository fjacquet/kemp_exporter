package kemp

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBuildInfoCollector(t *testing.T) {
	c := NewBuildInfoCollector("v1.2.3", "go1.26.5")
	want := `
# HELP kemp_exporter_build_info Exporter build information; constant 1, with the running version and Go version in the ` + "`version`" + ` and ` + "`goversion`" + ` labels.
# TYPE kemp_exporter_build_info gauge
kemp_exporter_build_info{goversion="go1.26.5",version="v1.2.3"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Fatalf("unexpected metric: %v", err)
	}
	if got := testutil.CollectAndCount(c); got != 1 {
		t.Errorf("collected %d metrics, want 1", got)
	}
	// It must register cleanly into a real registry.
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

// TestBuildInfoCollectorReflectsArguments guards against an implementation that
// quietly hardcodes a version/goversion literal instead of routing its
// parameters into the ConstLabels. A single fixed-input test (as above) cannot
// catch that: the hardcoded string could simply match whatever the test
// happens to pass. Calling with a second, different pair and requiring the
// output to change accordingly closes that gap.
func TestBuildInfoCollectorReflectsArguments(t *testing.T) {
	c := NewBuildInfoCollector("v9.9.9", "go9.9.9")
	want := `
# HELP kemp_exporter_build_info Exporter build information; constant 1, with the running version and Go version in the ` + "`version`" + ` and ` + "`goversion`" + ` labels.
# TYPE kemp_exporter_build_info gauge
kemp_exporter_build_info{goversion="go9.9.9",version="v9.9.9"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Fatalf("unexpected metric: %v", err)
	}
}
