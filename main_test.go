package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/kemp"
	"github.com/fjacquet/kemp_exporter/internal/models"
	"github.com/prometheus/client_golang/prometheus"
)

// /metrics must answer before any collection has happened — the server comes up
// first so an unreachable appliance cannot stall scrapes.
//
// This test alone only proves the handler behaves correctly given an empty
// store; it never actually starts the server and the collection loop together,
// so it cannot fail merely because someone reordered run() to collect before
// serving. TestServingStartsBeforeFirstCollectionCompletes below is the test
// that actually proves that ordering, by calling startServing itself -- the
// exact function run() calls.
func TestMetricsHandlerServesBeforeFirstCollection(t *testing.T) {
	store := kemp.NewSnapshotStore()
	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(kemp.NewBuildInfoCollector("v0.0.0-test", "go1.26.5")); err != nil {
		t.Fatalf("Register build info: %v", err)
	}

	srv := httptest.NewServer(metricsHandler(reg))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestServerAcceptsRequestsWhileFirstCollectionInFlight proves that
// startServer really does bind and start serving before returning (so a real
// GET can land while a slow collection is still in flight), and that
// startServer composed with CollectionLoop.Run in this order behaves
// correctly.
//
// CORRECTION (post-review): this test builds its own mux and calls
// startServer and go loop.Run directly -- it does NOT call run() or
// startServing, so on its own it does NOT regression-guard main.go's actual
// startup ordering. A previous version of this comment claimed reordering
// main.go "would make this test time out... it cannot pass by accident" --
// that was false: this test never exercises main.go's ordering code at all,
// so no change to run() or startServing can make it fail. Verified
// concretely: reviewer-suggested regression (replacing startServing's `go
// loop.Run(ctx)` with `store.Store(loop.CollectOnce(ctx))` run synchronously
// before startServer) leaves this test green.
// TestServingStartsBeforeFirstCollectionCompletes below is the test that
// actually guards that ordering, because it calls startServing itself.
func TestServerAcceptsRequestsWhileFirstCollectionInFlight(t *testing.T) {
	store := kemp.NewSnapshotStore()
	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(kemp.NewBuildInfoCollector("v0.0.0-test", "go1.26.5")); err != nil {
		t.Fatalf("Register build info: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler(reg))

	srv, ln, err := startServer("127.0.0.1:0", mux)
	if err != nil {
		t.Fatalf("startServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	slow := &kemp.MockClient{
		SystemName: "lm-01",
		Stats:      &models.Statistics{},
		StatsDelay: 300 * time.Millisecond,
	}
	loop := kemp.NewCollectionLoop([]kemp.Client{slow}, config.Collection{
		Interval:      time.Hour,
		Timeout:       2 * time.Second,
		MaxConcurrent: 1,
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx) // the "first collection" -- deliberately slow via StatsDelay

	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics while collection in flight: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// A local loopback round trip is microseconds, not hundreds of
	// milliseconds, so the GET above must have completed well before the
	// mock's 300ms delay could have elapsed. The store must therefore still
	// hold the pre-collection empty snapshot -- proving the request was
	// served WHILE collection was still in flight, not after it finished.
	if !store.Load().BuiltAt.IsZero() {
		t.Fatal("store already updated before the GET returned: the request did not race a real in-flight collection")
	}

	// Confirm the collection actually completes afterward, so the assertion
	// above isn't vacuously true because the delay never mattered.
	deadline := time.Now().Add(2 * time.Second)
	for store.Load().BuiltAt.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("first collection never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServingStartsBeforeFirstCollectionCompletes is the actual regression
// guard for requirement 1: it calls startServing itself -- the single
// function run() calls to build the mux, bind the server, and start the
// collection loop -- rather than a hand-assembled replica of its steps. A
// slow first collection (via MockClient.StatsDelay) must not block a real GET
// against the mux startServing built, and the store must still hold the
// pre-collection empty snapshot at the moment that GET returns.
//
// This also exercises the mux wiring itself (cfg.Server.URI routed to
// metricsHandler), which the hand-built-mux version of this test above did
// not cover.
func TestServingStartsBeforeFirstCollectionCompletes(t *testing.T) {
	store := kemp.NewSnapshotStore()
	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(kemp.NewBuildInfoCollector("v0.0.0-test", "go1.26.5")); err != nil {
		t.Fatalf("Register build info: %v", err)
	}

	slow := &kemp.MockClient{
		SystemName: "lm-01",
		Stats:      &models.Statistics{},
		StatsDelay: 300 * time.Millisecond,
	}
	loop := kemp.NewCollectionLoop([]kemp.Client{slow}, config.Collection{
		Interval:      time.Hour,
		Timeout:       2 * time.Second,
		MaxConcurrent: 1,
	}, store)

	cfg := &config.Config{Server: config.Server{Host: "127.0.0.1", Port: "0", URI: "/metrics"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, ln, err := startServing(ctx, cfg, reg, store, loop)
	if err != nil {
		t.Fatalf("startServing: %v", err)
	}
	defer func() { _ = srv.Close() }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics while collection in flight: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if !store.Load().BuiltAt.IsZero() {
		t.Fatal("store already updated before the GET returned: startServing is not serving before collection completes")
	}

	deadline := time.Now().Add(2 * time.Second)
	for store.Load().BuiltAt.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("first collection never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// /health is driven by snapshot age, independent of kemp_up -- and it ALWAYS
// answers 200. The starting/stale distinction lives in the body, never in the
// status code: /health used to return 503 while starting or stale, which meant
// any orchestrator probe pointed at it would restart or depool an exporter that
// was merely waiting on its first cycle, or whose appliance was briefly
// unreachable. /livez and /readyz are the endpoints probes should use; they read
// no state and cannot fail. /health is the diagnostic endpoint, and a diagnostic
// endpoint that refuses to answer is useless precisely when it is needed.
func TestHealthHandlerUsesSnapshotAge(t *testing.T) {
	store := kemp.NewSnapshotStore()
	h := healthHandler(store, time.Minute)

	get := func() (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		return rec.Code, rec.Body.String()
	}

	// No snapshot yet: starting up. 200, and the body says which.
	code, body := get()
	if code != http.StatusOK {
		t.Errorf("pre-collection status = %d, want 200", code)
	}
	if !strings.Contains(body, "starting") {
		t.Errorf("pre-collection body = %q, want it to report %q", body, "starting")
	}

	// Fresh snapshot: healthy, and the body must not claim otherwise.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now()})
	code, body = get()
	if code != http.StatusOK {
		t.Errorf("fresh-snapshot status = %d, want 200", code)
	}
	if !strings.HasPrefix(body, "ok") {
		t.Errorf("fresh-snapshot body = %q, want it to start with %q", body, "ok")
	}

	// Stale snapshot: the loop is wedged even though kemp_up may still read 1.
	// Still 200 -- the body is what carries the bad news.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now().Add(-10 * time.Minute)})
	code, body = get()
	if code != http.StatusOK {
		t.Errorf("stale-snapshot status = %d, want 200", code)
	}
	if !strings.Contains(body, "stale") {
		t.Errorf("stale-snapshot body = %q, want it to report %q", body, "stale")
	}
}

// renderHealthBody must never escape its input: the /health body is
// text/plain, and today's three possible bodies ("ok\n", "starting: …\n", and a
// time.Duration.String()) happen to contain no escapable characters, so a
// regression to routing this body through html/template would pass every
// other test in this file while still corrupting the response the moment a
// future body carries a hostname, target URL, or wrapped err.Error()
// containing "&" or "<". This test feeds exactly that shape of value -- an
// error message embedding a host:port -- through the same call healthHandler
// uses, and asserts the raw characters survive rather than coming back as
// "&amp;"/"&lt;".
func TestRenderHealthBodyDoesNotEscape(t *testing.T) {
	body := "stale: dial tcp lm&prod<01>:443 failed\n"

	var buf strings.Builder
	if err := renderHealthBody(&buf, body); err != nil {
		t.Fatalf("renderHealthBody: %v", err)
	}

	got := buf.String()
	if got != body {
		t.Errorf("renderHealthBody wrote %q, want the raw input %q unescaped", got, body)
	}
	if strings.Contains(got, "&amp;") || strings.Contains(got, "&lt;") || strings.Contains(got, "&gt;") {
		t.Errorf("renderHealthBody produced HTML entities in a text/plain body: %q", got)
	}
}

// /livez and /readyz must answer 200 before any collection cycle has completed,
// and they must be wired into the mux startServing actually builds -- calling
// staticOKHandler directly would pass even if nobody registered it, which is
// exactly the regression worth guarding. /health is checked here too, for the
// same wiring reason.
//
// Never probe /metrics instead: rendering the full exposition on every probe tick
// is needless load, and it can block behind a slow collection cycle -- which is
// the failure these endpoints exist to be immune to.
func TestProbeEndpointsAlwaysOKBeforeFirstCollection(t *testing.T) {
	store := kemp.NewSnapshotStore()
	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewPromCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A deliberately slow first collection: the probes must answer while it is
	// still in flight, not merely after it finishes.
	slow := &kemp.MockClient{
		SystemName: "lm-01",
		Stats:      &models.Statistics{},
		StatsDelay: 300 * time.Millisecond,
	}
	loop := kemp.NewCollectionLoop([]kemp.Client{slow}, config.Collection{
		Interval:      time.Hour,
		Timeout:       2 * time.Second,
		MaxConcurrent: 1,
	}, store)

	cfg := &config.Config{Server: config.Server{Host: "127.0.0.1", Port: "0", URI: "/metrics"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, ln, err := startServing(ctx, cfg, reg, store, loop)
	if err != nil {
		t.Fatalf("startServing: %v", err)
	}
	defer func() { _ = srv.Close() }()

	for _, path := range []string{"/livez", "/readyz", "/health"} {
		resp, err := http.Get("http://" + ln.Addr().String() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		code := resp.StatusCode
		bodyBytes, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s body: %v", path, readErr)
		}
		if code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 before the first collection cycle", path, code)
		}
		if strings.Contains(string(bodyBytes), "<html>") {
			t.Errorf("GET %s served the index landing page: the route is not registered", path)
		}
	}
}

// / must never leak configuration values (SafeConfig's whole point is that the
// raw Config never reaches anything printable). This locks the landing page
// down to the static shell plus the metrics URI alone.
func TestIndexHandlerDoesNotLeakConfig(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler("/metrics"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, secret := range []string{"apiKey", "apikey", "password", "Password", "APIKey"} {
		if strings.Contains(body, secret) {
			t.Fatalf("index page leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "/metrics") {
		t.Fatalf("index page does not link the metrics URI: %s", body)
	}
}

// Hot reload must call CollectionLoop.SetClients, never rebuild the loop --
// rebuilding would orphan the goroutine already running Run and double-collect.
// This proves the reload handler mutates the SAME loop instance's client set:
// NewCollectionLoop is called exactly once, before the handler ever runs, so the
// reloaded system can only appear in a later CollectOnce via SetClients.
func TestReloadHandlerUpdatesLoopClientsWithoutRebuildingLoop(t *testing.T) {
	store := kemp.NewSnapshotStore()
	cc := config.Collection{Interval: time.Hour, Timeout: 300 * time.Millisecond, MaxConcurrent: 1}
	loop := kemp.NewCollectionLoop(nil, cc, store) // built ONCE; never rebuilt below

	handler := makeReloadHandler(loop, false)

	newCfg := &config.Config{Systems: []config.System{
		{Name: "lm-02", Host: "127.0.0.1", Port: 1, APIKey: "x"},
	}}
	handler(newCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snap := loop.CollectOnce(ctx)
	if len(snap.Systems) != 1 || snap.Systems[0].System != "lm-02" {
		t.Fatalf("loop did not pick up the reloaded client set: %+v", snap.Systems)
	}
}

// REMOVED (final review, must-fix 10): TestBuildInfoCollectorSecondRegistrationFails.
// It asserted only that client_golang's registry returns AlreadyRegisteredError on
// a duplicate registration -- entirely third-party behaviour. Mutation confirmed it:
// renaming the build_info metric left it green, while TestBuildInfoCollector (in the
// kemp package) caught the rename. Its own comment already conceded that main's
// reload closure "has no reference to the registry at all", which is the real
// guarantee: a second registration is unreachable from the reload path by SCOPE, and
// scope is not something a test of the registry can demonstrate.

// telemetry.ShutdownAll's own doc comment warns that a nil *kemp.OTLPExporter
// boxed into its Shutdowner interface parameter becomes a non-nil interface
// value, so a guard written against the interface (rather than the concrete
// pointer) would miss it and panic inside Shutdown on the nil receiver.
// shutdownTelemetry's guard checks the CONCRETE *kemp.OTLPExporter pointer
// before any conversion to the interface -- this proves nothing panics when
// OTLP was never constructed (push disabled for this run).
func TestShutdownTelemetrySkipsNilOTLPExporter(t *testing.T) {
	var otlp *kemp.OTLPExporter // never constructed: OTel disabled for this run
	shutdownTelemetry(context.Background(), otlp)
}

// --once must exit non-zero if collection failed, so a cron or CI caller can
// tell. "Failed" here means at least one configured system's snapshot came
// back not OK (see run()'s --once branch). This forks the real binary
// entrypoint against an unreachable target and checks the process exit code --
// the one behavior most likely to be asserted without ever being exercised.
func TestOnceExitsNonZeroWhenCollectionFails(t *testing.T) {
	if os.Getenv("KEMP_EXPORTER_TEST_ONCE_SUBPROCESS") == "1" {
		os.Args = []string{"kemp_exporter", "--config", os.Getenv("KEMP_EXPORTER_TEST_ONCE_CONFIG"), "--once"}
		main()
		return
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	const cfgBody = `
server:
  port: "0"
collection:
  interval: 1s
  timeout: 300ms
  maxConcurrent: 1
otel:
  enabled: false
systems:
  - name: unreachable
    host: "127.0.0.1"
    port: 1
    apiKey: "deadbeef"
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestOnceExitsNonZeroWhenCollectionFails$")
	cmd.Env = append(os.Environ(),
		"KEMP_EXPORTER_TEST_ONCE_SUBPROCESS=1",
		"KEMP_EXPORTER_TEST_ONCE_CONFIG="+cfgPath,
	)
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected the subprocess to exit with an error, got err=%v output=%s", err, out)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("exit code = 0, want non-zero; output=%s", out)
	}
	// Pins the failure to the collection path specifically: a non-zero exit
	// from, say, a config parse error would satisfy the two assertions above
	// just as well, without ever exercising runOnce's failure branch at all.
	if !strings.Contains(string(out), "collection failed for 1 of 1 system(s)") {
		t.Fatalf("exit was non-zero but not for the expected reason; output=%s", out)
	}
}

// --- Final review, must-fix 13: a slow shutdown made a normal exit non-zero ---
//
// run() used to `return srv.Shutdown(shutdownCtx)`. A shutdown that exceeds the
// 10s budget with connections still open returns context.DeadlineExceeded, so a
// perfectly ordinary SIGTERM exited non-zero -- which the shipped systemd unit
// (Restart=on-failure) and Kubernetes both read as a failed termination and act on.
// The exporter has nothing left to do at that point: the listener is closed, the
// snapshot is irrelevant, and a lingering connection is not an error the operator
// can act on. Log it, exit 0.
func TestShutdownServerReportsSuccessWhenTheGraceBudgetExpires(t *testing.T) {
	// A request that is still being served when shutdown starts: this is what makes
	// Shutdown actually wait, and then fail, on the budget. Without a connection in
	// flight Shutdown returns nil immediately and the test would prove nothing.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			once.Do(func() { close(entered) })
			<-release
			w.WriteHeader(http.StatusOK)
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer close(release)

	go func() {
		req, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		if err != nil {
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := shutdownServer(ctx, srv); err != nil {
		t.Fatalf("shutdownServer returned %v; a shutdown that overran its budget must still "+
			"be a clean exit (systemd Restart=on-failure and Kubernetes both act on a non-zero code)", err)
	}
}
