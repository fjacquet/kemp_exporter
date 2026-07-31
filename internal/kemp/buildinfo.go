package kemp

import "github.com/prometheus/client_golang/prometheus"

// NewBuildInfoCollector returns a collector exposing a single constant metric,
// `kemp_exporter_build_info{version="...",goversion="..."} 1`, so one scrape reveals
// exactly which build is running — the check that catches a stale container that was
// never re-pulled after a release.
//
// The name is the BINARY name, not the metric prefix, matching
// node_exporter_build_info and prometheus_build_info. It carries no system label:
// it describes the process, not any backend. version comes from the -X main.version
// ldflag; goversion is passed in rather than read from runtime so tests are
// deterministic.
func NewBuildInfoCollector(version, goversion string) prometheus.Collector {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "kemp_exporter",
		Name:        "build_info",
		Help:        "Exporter build information; constant 1, with the running version and Go version in the `version` and `goversion` labels.",
		ConstLabels: prometheus.Labels{"version": version, "goversion": goversion},
	})
	g.Set(1)
	return g
}
