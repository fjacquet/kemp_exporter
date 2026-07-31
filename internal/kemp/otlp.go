package kemp

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// OTLPExporter pushes the snapshot via OTLP using asynchronous observable gauges.
// The reader (a PeriodicReader in production, a ManualReader in tests) drives
// collection: on each cycle every registered instrument's callback reads the
// latest snapshot and observes its samples. Both export paths therefore render
// from the same immutable snapshot and cannot disagree — including on which
// sample survives when the snapshot itself is anomalous. A LoadMaster SubVS row
// carries its parent virtual service's VIP address and port, so two distinct
// derivations can resolve to byte-identical labels for one metric name
// (prometheus.go's PromCollector.Collect doc comment describes this concretely).
// PromCollector keeps the first such sample and drops the rest, logging a Warn;
// each callback below applies the identical first-wins, log-on-drop rule over the
// identical iteration order (Snapshot.SamplesByName preserves Systems/Samples
// order), so the two export paths pick the same surviving sample rather than an
// OTLP-only deployment silently keeping whichever duplicate its gauge aggregation
// happens to observe last within one collection.
type OTLPExporter struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter
	store    *SnapshotStore

	mu         sync.Mutex
	registered map[string]struct{}
}

// NewOTLPExporter creates an exporter pushing to an OTLP gRPC endpoint. The
// underlying gRPC client connects lazily (otlpmetricgrpc.New uses grpc.NewClient,
// which does not dial synchronously), so this succeeds even if no collector is
// listening yet at oc.Endpoint; failures surface as export errors later, not here.
func NewOTLPExporter(ctx context.Context, oc config.OTelConfig, store *SnapshotStore, serviceVersion string) (*OTLPExporter, error) {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(oc.Endpoint)}
	if oc.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	exp, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	reader := sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(oc.Interval))
	return newOTLPExporter(reader, store, serviceVersion), nil
}

// newOTLPExporter builds the meter provider from a reader. Separated from
// NewOTLPExporter so tests inject a ManualReader instead of a live gRPC connection
// — that seam is what makes this half of the exporter testable without a running
// collector.
func newOTLPExporter(reader sdkmetric.Reader, store *SnapshotStore, serviceVersion string) *OTLPExporter {
	res, _ := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("kemp-exporter"),
		semconv.ServiceVersion(serviceVersion),
	))
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	return &OTLPExporter{
		provider:   provider,
		meter:      provider.Meter("kemp-exporter"),
		store:      store,
		registered: make(map[string]struct{}),
	}
}

// EnsureInstruments registers an observable gauge for every metric name in the
// current snapshot that does not already have one. Idempotent, so it is safe to
// call after every collection cycle: a name already in e.registered is skipped
// rather than re-registered. Verified empirically (see task 13's report) that the
// SDK tolerates re-registering an identical name/kind/unit/description gauge
// without erroring and, because every registration's callback reads the same
// store and observes the same attribute set, the last-value-per-attribute-set
// gauge aggregation collapses repeat observations into a single correct data
// point regardless — so this guard is not what keeps exported data correct. What
// it does prevent is unbounded growth: without it, every call to
// EnsureInstruments (the collection loop calls it once per cycle, forever) would
// register one more redundant Float64ObservableGauge + callback closure for every
// name it already knows, accumulating for the life of the process. A name that
// appears for the first time on a later cycle — a LoadMaster gaining a virtual
// service — is still picked up immediately, because this method re-scans the full
// current name set every call rather than only running once.
//
// A name is never removed from e.registered once added, even if every sample of
// that name later disappears from the snapshot: the stable metric API gives no
// supported way to fully retire a single async instrument's callback once it is
// created, short of tearing down and rebuilding the whole MeterProvider (which
// would also drop every other instrument and reset the OTLP resource identity for
// no benefit). That instrument's callback keeps running every cycle for the rest
// of the process, but harmlessly: it reads store.Load().SamplesByName(name) fresh
// each time (see the doc on that call below) and, finding nothing, calls
// obs.Observe zero times — so the instrument reports zero data points, never a
// stale or fabricated value. The set of distinct names a LoadMaster fleet can ever
// produce is bounded by the label builders in metrics.go, not by cardinality of any
// one label value, so this is a small, fixed set of harmless no-op callbacks, not
// an unbounded leak.
func (e *OTLPExporter) EnsureInstruments() error {
	snap := e.store.Load()
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, name := range snap.MetricNames() {
		if _, ok := e.registered[name]; ok {
			continue
		}
		metricName := name // capture per iteration for the callback
		_, err := e.meter.Float64ObservableGauge(metricName,
			metric.WithFloat64Callback(func(_ context.Context, obs metric.Float64Observer) error {
				// Loads the CURRENT snapshot at collection time, not whatever was
				// current when this callback was registered: the reader invokes
				// this callback fresh on every collection cycle, so reading
				// e.store.Load() here (rather than closing over snap above) is
				// what makes this instrument pull-based rather than a one-shot
				// capture of the first cycle's data.
				//
				// schemaKeys and seen replicate PromCollector.Collect's per-name
				// nameSchema guard (prometheus.go), scoped to this one metric name's
				// callback instead of a map keyed by name — one callback already IS
				// one name here, so no outer map is needed. The first sample this
				// invocation observes fixes the label-key schema; a later sample
				// with different keys, or with the same keys AND values as one
				// already observed this cycle, is dropped and logged rather than
				// exported — matching prometheus.go's drop-and-warn choice exactly,
				// over the same iteration order, so both export paths keep the same
				// surviving sample for one snapshot.
				var schemaKeys []string
				seen := make(map[string]struct{})
				for _, s := range e.store.Load().SamplesByName(metricName) {
					keys := make([]string, len(s.Labels))
					vals := make([]string, len(s.Labels))
					for i, l := range s.Labels {
						keys[i] = l.Key
						vals[i] = l.Value
					}

					if schemaKeys == nil {
						schemaKeys = keys
					} else if !slices.Equal(schemaKeys, keys) {
						logrus.WithFields(logrus.Fields{
							"metric":   metricName,
							"expected": schemaKeys,
							"got":      keys,
						}).Warn("dropping OTLP sample: label keys diverge from earlier samples of the same metric name")
						continue
					}

					sig := strings.Join(vals, "\x00")
					if _, dup := seen[sig]; dup {
						logrus.WithFields(logrus.Fields{
							"metric": metricName,
							"keys":   keys,
							"vals":   vals,
						}).Warn("dropping OTLP sample: duplicate label values for a metric name already observed this collection")
						continue
					}
					seen[sig] = struct{}{}

					obs.Observe(s.Value, metric.WithAttributes(attrsFor(s.Labels)...))
				}
				return nil
			}),
		)
		if err != nil {
			return err
		}
		e.registered[metricName] = struct{}{}
	}
	return nil
}

// Shutdown flushes and stops the meter provider.
func (e *OTLPExporter) Shutdown(ctx context.Context) error {
	return e.provider.Shutdown(ctx)
}

// attrsFor converts the sample's labels to OTLP attributes, preserving order.
func attrsFor(labels []Label) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, len(labels))
	for i, l := range labels {
		attrs[i] = attribute.String(l.Key, l.Value)
	}
	return attrs
}
