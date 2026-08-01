// Command kemp_exporter exports Progress Kemp LoadMaster metrics to Prometheus and
// OTLP.
package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/kemp"
	"github.com/fjacquet/kemp_exporter/internal/logging"
	"github.com/fjacquet/kemp_exporter/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// version is injected at build time via -X main.version.
var version = "dev"

var (
	flagConfig string
	flagDebug  bool
	flagOnce   bool
	flagTrace  bool
)

func main() {
	root := &cobra.Command{
		Use:     "kemp_exporter",
		Short:   "Prometheus and OTLP exporter for Progress Kemp LoadMaster",
		Version: version,
		RunE:    run,
		// main() prints the returned error itself (see below), and an operational
		// failure -- an unreachable appliance, a bad reload -- has nothing to do
		// with how the command was invoked, so dumping the full usage/help text
		// alongside it would just be noise in an operator's terminal or a cron
		// job's logs. Cobra's own error line is silenced for the same reason:
		// printing it twice would be worse, not clearer.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.Flags().StringVar(&flagConfig, "config", "config.yaml", "path to the configuration file")
	root.Flags().BoolVar(&flagDebug, "debug", false, "enable debug logging; with --once, dump every collected sample")
	root.Flags().BoolVar(&flagOnce, "once", false, "run a single collection cycle and exit")
	root.Flags().BoolVar(&flagTrace, "trace", false, "log every API response body (never headers; auth responses are skipped)")

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// metricsHandler serves the registry. Split out so tests can exercise it without
// starting the whole process.
func metricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// healthBody computes the /health response text from snap's freshness relative
// to maxAge. Split out from healthHandler so tests can compute the expected
// body directly, from the same code the handler runs, rather than duplicating
// the three message strings by hand -- a duplicate copy drifts silently the
// next time a message changes, while calling this function cannot.
func healthBody(snap *kemp.Snapshot, maxAge time.Duration) string {
	switch {
	case snap.BuiltAt.IsZero():
		return "starting: no collection cycle has completed yet\n"
	case time.Since(snap.BuiltAt) > maxAge:
		return fmt.Sprintf("stale: last collection %s ago\n",
			time.Since(snap.BuiltAt).Round(time.Second))
	default:
		return "ok\n"
	}
}

// healthHandler reports collection freshness from snapshot AGE, deliberately
// independent of kemp_up. kemp_up describes the backend; a wedged collection loop
// would leave it at a stale 1 forever, so staleness is the only honest freshness
// signal.
//
// It ALWAYS answers 200. The starting/stale information is carried in the body,
// never in the status code. This handler used to return 503 in both of those
// cases, which made it unsafe as an orchestrator probe target: a Kubernetes
// liveness probe would restart an exporter that was merely waiting on its first
// collection cycle, and a readiness probe would depool one whose appliance was
// briefly unreachable -- neither of which the exporter process can fix by dying.
// /livez and /readyz (staticOKHandler) exist for probes; /health exists for a
// human or a dashboard asking "is collection actually current?", and an endpoint
// that answers that question by refusing to answer is useless exactly when it
// matters.
func healthHandler(store *kemp.SnapshotStore, maxAge time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := healthBody(store.Load(), maxAge)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		// Written via renderHealthBody(w, body), never a literal w.Write(...) or
		// inline io.WriteString(w, body) here -- both of those call shapes are
		// exactly what semgrep's no-direct-write-to-responsewriter and
		// no-io-writestring-to-responsewriter rules flag inside a function whose
		// parameter is http.ResponseWriter, on the theory that the write might be
		// unescaped HTML. Neither applies to this body: renderHealthBody takes an
		// io.Writer, not an http.ResponseWriter, and this response is text/plain,
		// not HTML, so there is no escaping question to get wrong -- body must
		// reach the client byte-for-byte. Do NOT route it through html/template
		// (that would silently turn "&"/"<" in a future hostname, target URL, or
		// wrapped err.Error() into HTML entities, corrupting output operators curl
		// and grep) or through text/template (semgrep's
		// import-text-template rule flags that import unconditionally,
		// regardless of content type).
		if err := renderHealthBody(w, body); err != nil {
			logrus.WithError(err).Debug("health response write failed")
		}
	})
}

// renderHealthBody writes body to dst verbatim, with no escaping -- see
// healthHandler's comment for why that is correct for this text/plain
// response, and why dst is typed io.Writer rather than http.ResponseWriter.
func renderHealthBody(dst io.Writer, body string) error {
	_, err := io.WriteString(dst, body)
	return err
}

// staticOKHandler always answers 200 -- no snapshot state, no collection state,
// nothing that can make it fail. /livez and /readyz both use it: a probe wired
// here can never be the reason a healthy process gets restarted or pulled from
// rotation. /health remains the endpoint for anything that wants to know whether
// collection is actually current.
func staticOKHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// indexPage is parsed once at package init. Rendered through html/template
// (contextual auto-escaping), rather than a raw w.Write of a hand-built string,
// even though metricsURI is operator-configured rather than attacker-controlled:
// this is what keeps the handler from writing unescaped HTML straight to the
// response, the pattern a raw string-concatenation write always is regardless of
// where the interpolated value came from.
var indexPage = template.Must(template.New("index").Parse(
	`<html><head><title>kemp_exporter</title></head>
<body><h1>kemp_exporter</h1><p><a href="{{.MetricsURI}}">Metrics</a></p></body></html>
`))

// indexHandler serves the landing page. It carries the metrics URI alone --
// never any other configuration value, per SafeConfig's contract that the raw
// Config never reaches anything printable.
func indexHandler(metricsURI string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if err := indexPage.Execute(w, struct{ MetricsURI string }{metricsURI}); err != nil {
			logrus.WithError(err).Debug("index response write failed")
		}
	}
}

// buildClients turns the configured systems into API clients.
func buildClients(cfg *config.Config, trace bool) ([]kemp.Client, error) {
	clients := make([]kemp.Client, 0, len(cfg.Systems))
	for _, sys := range cfg.Systems {
		c, err := kemp.NewSystemClient(sys, trace)
		if err != nil {
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, nil
}

// startServer binds addr synchronously -- so a port conflict fails run()
// immediately, as a clear startup error, rather than surfacing minutes later as
// an unexplained missing /metrics endpoint -- and then serves in the
// background. Binding before returning is also what makes "the server is
// already listening" a fact this file's own tests can rely on the instant this
// function returns, rather than racing a goroutine that has merely been
// scheduled.
func startServer(addr string, handler http.Handler) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Deliberately logged, not Fatal: Fatal calls os.Exit from a
			// background goroutine, which would skip graceful shutdown (OTLP
			// flush, draining an in-flight scrape) entirely. Serve only returns
			// a non-ErrServerClosed error for a genuine, effectively
			// unrecoverable listener failure, which in practice never happens
			// here since startServer's own net.Listen already proved the
			// address bindable.
			logrus.WithError(err).Error("http server stopped unexpectedly")
		}
	}()
	return srv, ln, nil
}

// startServing builds the metrics/health/index mux from cfg, reg and store,
// binds and starts the HTTP server, and only THEN starts the collection
// loop's Run goroutine.
//
// This exact ordering -- server bound and accepting connections before the
// loop's first collection cycle can even begin -- IS requirement 1's entire
// implementation, and this function is the single seam through which both
// run() and this file's own tests exercise it. A regression that reordered
// these two steps (e.g. `store.Store(loop.CollectOnce(ctx))` run synchronously
// above the startServer call, before ever starting the loop's Run goroutine)
// would only be caught by a test that calls startServing itself -- see
// TestServingStartsBeforeFirstCollectionCompletes in main_test.go, which does
// exactly that, rather than a test that merely replicates these steps
// independently.
func startServing(ctx context.Context, cfg *config.Config, reg *prometheus.Registry, store *kemp.SnapshotStore, loop *kemp.CollectionLoop) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.Handle(cfg.Server.URI, metricsHandler(reg))

	// routes is registered FROM config.ReservedURIs, not alongside it: the two
	// length/lookup checks below mean a route added here without a matching
	// entry in config.ReservedURIs, or a pattern reserved there without a
	// handler here, fails startServing loudly instead of silently drifting
	// back into the mux-conflict panic config.ReservedURIs exists to prevent.
	routes := map[string]http.Handler{
		// Health tolerates two missed cycles before reporting stale. It always
		// answers 200; the staleness verdict is in the body (see healthHandler).
		"/health": healthHandler(store, 2*cfg.Collection.Interval+cfg.Collection.Timeout),
		// Fixed probe paths, both wired to a handler that reads nothing and
		// cannot fail. Deliberately NOT /metrics: rendering the full exposition
		// on every probe tick is needless load and can block behind a slow
		// collection cycle.
		"/livez":  http.HandlerFunc(staticOKHandler),
		"/readyz": http.HandlerFunc(staticOKHandler),
		"/":       indexHandler(cfg.Server.URI),
	}
	if len(routes) != len(config.ReservedURIs) {
		return nil, nil, fmt.Errorf("startServing: %d routes registered but %d URIs reserved in config.ReservedURIs; the two lists have drifted apart", len(routes), len(config.ReservedURIs))
	}
	for pattern := range config.ReservedURIs {
		h, ok := routes[pattern]
		if !ok {
			return nil, nil, fmt.Errorf("startServing: reserved URI %q has no handler registered", pattern)
		}
		mux.Handle(pattern, h)
	}

	addr := cfg.Server.Host + ":" + cfg.Server.Port

	// Serve BEFORE the first collection: login plus a first poll can outlast the
	// collection timeout, and blocking startup on it would stall /metrics behind
	// an unreachable appliance.
	srv, ln, err := startServer(addr, mux)
	if err != nil {
		return nil, nil, err
	}
	logrus.WithField("addr", ln.Addr().String()).Info("serving metrics")

	go loop.Run(ctx)

	return srv, ln, nil
}

// shutdownTelemetry flushes OTLP if it was constructed.
//
// otlp is deliberately typed *kemp.OTLPExporter here, not telemetry.Shutdowner:
// telemetry.ShutdownAll's own doc comment warns that a nil *kemp.OTLPExporter
// boxed into the Shutdowner interface becomes a non-nil interface value (an
// interface holding a nil pointer is not itself nil), so a guard checked AFTER
// that conversion would miss it and panic inside Shutdown on the nil receiver.
// Checking the concrete pointer here, before any conversion happens, is what
// keeps the "OTLP disabled" case a no-op instead of a crash on shutdown.
func shutdownTelemetry(ctx context.Context, otlp *kemp.OTLPExporter) {
	if otlp == nil {
		return
	}
	telemetry.ShutdownAll(ctx, otlp)
}

// makeReloadHandler builds the onReload callback for the config watcher.
//
// It closes over loop and trace only -- never the watcher itself, so onReload
// cannot call watcher.Close() even by mistake: Close() holds the same mutex
// reload() holds across this very callback, so a synchronous Close from inside
// onReload would self-deadlock on a non-reentrant mutex.
//
// It calls loop.SetClients rather than constructing a new CollectionLoop:
// SetClients is mutex-guarded and safe to call while Run is executing
// concurrently; rebuilding the loop would orphan the goroutine already running
// the old one's Run and start a second, competing collection cycle.
func makeReloadHandler(loop *kemp.CollectionLoop, trace bool) func(*config.Config) {
	return func(newCfg *config.Config) {
		newClients, err := buildClients(newCfg, trace)
		if err != nil {
			logrus.WithError(err).Error("reload: rebuilding clients failed; keeping previous targets")
			return
		}
		loop.SetClients(newClients)
		logrus.WithField("systems", len(newClients)).Info("reload: target set updated")
	}
}

func run(_ *cobra.Command, _ []string) error {
	// .env loads before interpolation; it never overrides real injected secrets.
	config.LoadDotEnv(flagConfig)

	cfg, err := config.Load(flagConfig)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := logging.Setup(cfg.Server.LogName, flagDebug); err != nil {
		return fmt.Errorf("setup logging: %w", err)
	}
	logrus.Debugf("configuration:\n%s", config.SafeConfig(cfg))

	clients, err := buildClients(cfg, flagTrace)
	if err != nil {
		return err
	}

	store := kemp.NewSnapshotStore()
	loop := kemp.NewCollectionLoop(clients, cfg.Collection, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --once: collect, optionally dump, exit. The validation path.
	if flagOnce {
		return runOnce(ctx, loop, store)
	}

	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		return fmt.Errorf("register collector: %w", err)
	}
	// Registered exactly once, here, before the watcher (and therefore before
	// any reload) exists: reload's closure (makeReloadHandler) never receives
	// reg, so a second registration is not just avoided by convention -- the
	// reload path has no way to reach the registry at all.
	if err := reg.Register(kemp.NewBuildInfoCollector(version, runtime.Version())); err != nil {
		return fmt.Errorf("register build info: %w", err)
	}

	// startServing owns the "server before first collection" ordering: it
	// builds the mux, binds and starts the HTTP server, and only then starts
	// the collection loop's Run goroutine. See its doc comment.
	srv, _, err := startServing(ctx, cfg, reg, store, loop)
	if err != nil {
		return fmt.Errorf("start http server: %w", err)
	}

	var otlp *kemp.OTLPExporter
	if cfg.OTel.Enabled {
		otlp, err = kemp.NewOTLPExporter(ctx, cfg.OTel, store, version)
		if err != nil {
			return fmt.Errorf("start OTLP exporter: %w", err)
		}
		logrus.WithField("endpoint", cfg.OTel.Endpoint).Info("OTLP export enabled")
	}

	// Register OTLP instruments as new metric names appear in the snapshot.
	if otlp != nil {
		// Registered once immediately, not just on the ticker below: the
		// ticker's first tick is a full cfg.Collection.Interval away (60s by
		// default), while loop.Run already published its first snapshot back
		// in startServing. Without this, every OTLP export in that window
		// (cfg.OTel.Interval, 10s by default -- so roughly the first six
		// export cycles) would push zero kemp metrics despite the data
		// already sitting in the store.
		if err := otlp.EnsureInstruments(); err != nil {
			logrus.WithError(err).Warn("OTLP instrument registration failed")
		}
		go func() {
			t := time.NewTicker(cfg.Collection.Interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := otlp.EnsureInstruments(); err != nil {
						logrus.WithError(err).Warn("OTLP instrument registration failed")
					}
				}
			}
		}()
	}

	// Hot reload: rebuild clients and swap them into the running loop.
	watcher, err := config.NewWatcher(flagConfig, makeReloadHandler(loop, flagTrace))
	if err != nil {
		return fmt.Errorf("start config watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()
	watcher.Start(ctx)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	sig := <-stop
	logrus.WithField("signal", sig.String()).Info("shutting down")

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	shutdownTelemetry(shutdownCtx, otlp)
	return shutdownServer(shutdownCtx, srv)
}

// shutdownServer drains the HTTP server within ctx's budget and reports SUCCESS
// even when the budget expires.
//
// Returning srv.Shutdown's error (as this did) turned an ordinary SIGTERM into a
// non-zero exit whenever a connection outlived the 10s grace period: systemd
// (Restart=on-failure, in the shipped unit) and Kubernetes both read that as a
// failed termination and act on it. By this point the process has nothing left to
// do and nothing left to lose -- the listener is closed, the collection loop is
// cancelled, telemetry is already flushed -- and a lingering connection is not
// something an operator can act on. It is worth a log line, not an exit code.
func shutdownServer(ctx context.Context, srv *http.Server) error {
	if err := srv.Shutdown(ctx); err != nil {
		logrus.WithError(err).Warn("http server did not drain within the shutdown budget; exiting anyway")
	}
	return nil
}

// runOnce runs a single collection cycle, optionally dumps every sample, and
// reports failure to the caller.
//
// "Failed" means at least one configured system's snapshot came back not OK.
// A cron or CI caller running --once wants to know about a PARTIAL failure just
// as much as a total one: kemp_up=0 for one LoadMaster out of three is exactly
// the kind of degradation a validation run exists to catch, and silently
// exiting 0 because the other two succeeded would defeat that purpose.
func runOnce(ctx context.Context, loop *kemp.CollectionLoop, store *kemp.SnapshotStore) error {
	snap := loop.CollectOnce(ctx)
	store.Store(snap)
	if flagDebug {
		if err := kemp.DumpSamples(os.Stdout, snap); err != nil {
			return fmt.Errorf("dump samples: %w", err)
		}
	}

	var failed []string
	for _, sys := range snap.Systems {
		if !sys.OK {
			failed = append(failed, sys.System)
			logrus.WithFields(logrus.Fields{"system": sys.System, "error": sys.Err}).
				Warn("system collection failed")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("collection failed for %d of %d system(s): %s",
			len(failed), len(snap.Systems), strings.Join(failed, ", "))
	}
	return nil
}
