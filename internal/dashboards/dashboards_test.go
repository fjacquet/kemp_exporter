package dashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// knownMetrics is every metric name the exporter can emit. Keep it in sync with
// docs/metrics.md; a dashboard referencing anything outside this set is a bug.
var knownMetrics = map[string]bool{
	"kemp_up":                                     true,
	"kemp_exporter_build_info":                    true,
	"kemp_connections_per_second":                 true,
	"kemp_bytes_per_second":                       true,
	"kemp_packets_per_second":                     true,
	"kemp_cpu_idle_percent":                       true,
	"kemp_cpu_user_percent":                       true,
	"kemp_cpu_system_percent":                     true,
	"kemp_memory_free_bytes":                      true,
	"kemp_memory_used_bytes":                      true,
	"kemp_memory_used_percent":                    true,
	"kemp_tps":                                    true,
	"kemp_tps_ssl":                                true,
	"kemp_interface_bytes_read_total":             true,
	"kemp_interface_bytes_written_total":          true,
	"kemp_virtual_service_up":                     true,
	"kemp_virtual_service_status":                 true,
	"kemp_virtual_service_active_connections":     true,
	"kemp_virtual_service_connections_per_second": true,
	"kemp_virtual_service_connections_total":      true,
	"kemp_virtual_service_packets_total":          true,
	"kemp_virtual_service_bytes_total":            true,
	"kemp_virtual_service_bytes_read_total":       true,
	"kemp_virtual_service_bytes_written_total":    true,
	"kemp_real_server_up":                         true,
	"kemp_real_server_status":                     true,
	"kemp_real_server_active_connections":         true,
	"kemp_real_server_connections_per_second":     true,
	"kemp_real_server_connections_total":          true,
	"kemp_real_server_packets_total":              true,
	"kemp_real_server_bytes_total":                true,
	"kemp_real_server_bytes_read_total":           true,
	"kemp_real_server_bytes_written_total":        true,
}

var metricRef = regexp.MustCompile(`\bkemp_[a-z0-9_]+`)

// collectExprs walks arbitrary decoded JSON collecting every expr/query string.
func collectExprs(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok && (k == "expr" || k == "query" || k == "definition") {
				*out = append(*out, s)
				continue
			}
			collectExprs(val, out)
		}
	case []any:
		for _, item := range t {
			collectExprs(item, out)
		}
	}
}

func TestDashboardsReferenceOnlyKnownMetrics(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "grafana", "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no dashboard JSON found under grafana/")
	}

	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("dashboard is not valid JSON: %v", err)
			}
			var exprs []string
			collectExprs(doc, &exprs)
			if len(exprs) == 0 {
				t.Fatal("dashboard contains no queries")
			}

			var unknown []string
			for _, e := range exprs {
				for _, ref := range metricRef.FindAllString(e, -1) {
					if !knownMetrics[ref] {
						unknown = append(unknown, ref+"  (in: "+e+")")
					}
				}
			}
			sort.Strings(unknown)
			if len(unknown) > 0 {
				t.Errorf("dashboard references metrics the exporter never emits:\n  %s",
					strings.Join(unknown, "\n  "))
			}
		})
	}
}

// Per-second GAUGES must never be wrapped in rate(): they are already rates.
// Cumulative _total counters may and should use rate()/increase().
func TestDashboardsDoNotRateGauges(t *testing.T) {
	gauges := []string{
		"kemp_connections_per_second",
		"kemp_bytes_per_second",
		"kemp_packets_per_second",
		"kemp_virtual_service_connections_per_second",
		"kemp_real_server_connections_per_second",
		"kemp_tps",
		"kemp_tps_ssl",
		"kemp_memory_used_percent",
		"kemp_cpu_idle_percent",
	}
	paths, _ := filepath.Glob(filepath.Join("..", "..", "grafana", "*.json"))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		var exprs []string
		collectExprs(doc, &exprs)
		for _, e := range exprs {
			for _, g := range gauges {
				for _, fn := range []string{"rate(", "irate(", "increase("} {
					if strings.Contains(e, fn) && strings.Contains(e, g) {
						t.Errorf("%s: %s applied to gauge %s — use sum/avg instead:\n  %s",
							filepath.Base(p), fn, g, e)
					}
				}
			}
		}
	}
}
