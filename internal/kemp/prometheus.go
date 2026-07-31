package kemp

import (
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// PromCollector is an unchecked Prometheus collector: Describe emits nothing, so
// the metric-name set is free to vary from snapshot to snapshot — a LoadMaster
// gaining or losing a virtual service between scrapes is normal, not an error.
// Collect reads the latest snapshot without doing anything that could block the
// collection loop: SnapshotStore.Load takes and releases its RWMutex before
// Collect does any rendering work, and Collect itself holds no lock at all.
type PromCollector struct {
	store *SnapshotStore
}

// NewPromCollector wraps the snapshot store as a prometheus.Collector.
func NewPromCollector(store *SnapshotStore) *PromCollector {
	return &PromCollector{store: store}
}

// Describe sends nothing.
//
// That silence is what makes this an unchecked collector. A checked collector's
// registry validates every metric Collect produces against the descriptors handed
// to Describe up front — including that every metric sharing a name carries the
// same label keys. Describing nothing opts out of that validation, which is the
// price of letting the metric-name set track the snapshot instead of a fixed
// schema. Collect pays that price back itself: see its own doc comment.
func (p *PromCollector) Describe(chan<- *prometheus.Desc) {}

// nameSchema pins one metric name's label-key set (the order every sample sharing
// that name must agree on) to the *prometheus.Desc built from it, so Collect builds
// a name's Desc once per scrape rather than once per sample. seen records the
// label-VALUE tuples already emitted for this name, so a second sample with the
// same keys and the same values (a duplicate series, not a schema drift) can be
// caught too.
type nameSchema struct {
	keys []string
	desc *prometheus.Desc
	seen map[string]struct{}
}

// Collect renders every snapshot sample as a gauge metric.
//
// Per-second values and cumulative "_total" values are both rendered as
// prometheus.GaugeValue. This is deliberate, not an oversight: the snapshot model
// carries no monotonic guarantee across an appliance or exporter restart, and a
// CounterValue that resets on restart would mislead rate()/increase() far worse
// than a gauge that never claimed monotonicity in the first place.
//
// Label-key consistency is enforced here rather than left to the registry, because
// for an unchecked collector the registry cannot enforce it: Describe emits no
// descriptors, so client_golang has nothing to check Collect's output against.
// Left unguarded, two samples of the same metric name with different label keys
// would surface only when Gather assembles the exposition — as either a failed
// scrape (every metric on that scrape lost, not just the offending one) or, per
// client_golang's own docs, an inconsistent result. So the schema is fixed here:
// the first sample seen for a metric name in this scrape defines that name's
// label-key set, and any later sample whose keys disagree is dropped rather than
// handed to the registry. The drop is logged at Warn — with the metric name, the
// system, and both key sets — specifically so the drift is visible in the
// exporter's own logs instead of just disappearing from the exposition. In normal
// operation this path never runs: every derivation in this package builds Labels
// through the shared builders in metrics.go, which is what keeps a name's key set
// uniform to begin with. Firing at all means one of those builders was bypassed —
// a bug worth seeing, not swallowing.
//
// A second, distinct failure mode needs its own guard: two samples of the same
// name can agree on keys but still collide on values — e.g. a LoadMaster SubVS row
// carries its parent virtual service's VIP address and port, so two
// st.VirtualServices entries can resolve to the same vsKey (derivations.go) and
// therefore byte-identical vsLabels. client_golang's registry rejects two metrics
// sharing a name AND identical label values outright — for unchecked collectors
// too — which would fail the whole Gather call, every scrape, until the
// LoadMaster's config changed. So Collect also tracks, per metric name, the set of
// label-value tuples already emitted; a later sample whose values repeat an
// already-emitted tuple is dropped and logged at Warn, the same way key drift is.
func (p *PromCollector) Collect(ch chan<- prometheus.Metric) {
	snap := p.store.Load()
	schema := make(map[string]nameSchema)

	for _, sys := range snap.Systems {
		for _, s := range sys.Samples {
			keys := make([]string, len(s.Labels))
			vals := make([]string, len(s.Labels))
			for i, l := range s.Labels {
				keys[i] = l.Key
				vals[i] = l.Value
			}

			entry, seenName := schema[s.Name]
			if !seenName {
				entry = nameSchema{
					keys: keys,
					desc: prometheus.NewDesc(s.Name, "Kemp LoadMaster metric "+s.Name, keys, nil),
					seen: make(map[string]struct{}),
				}
				schema[s.Name] = entry
			} else if !slices.Equal(entry.keys, keys) {
				logrus.WithFields(logrus.Fields{
					"metric":   s.Name,
					"system":   sys.System,
					"expected": entry.keys,
					"got":      keys,
				}).Warn("dropping sample: label keys diverge from earlier samples of the same metric name")
				continue
			}

			sig := strings.Join(vals, "\x00")
			if _, dup := entry.seen[sig]; dup {
				logrus.WithFields(logrus.Fields{
					"metric": s.Name,
					"system": sys.System,
					"keys":   keys,
					"vals":   vals,
				}).Warn("dropping sample: duplicate label values for a metric name already emitted this scrape")
				continue
			}
			entry.seen[sig] = struct{}{}

			m, err := prometheus.NewConstMetric(entry.desc, prometheus.GaugeValue, s.Value, vals...)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"metric": s.Name,
					"system": sys.System,
					"keys":   keys,
				}).WithError(err).Warn("dropping sample: could not render as a metric")
				continue
			}
			ch <- m
		}
	}
}

var _ prometheus.Collector = (*PromCollector)(nil)
