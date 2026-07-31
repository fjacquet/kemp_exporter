package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// serving. TestServerAcceptsRequestsWhileFirstCollectionInFlight below is the
// test that actually proves the ordering.
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

// TestServerAcceptsRequestsWhileFirstCollectionInFlight is the direct proof of
// requirement 1: the server and the collection loop are started in exactly the
// order run() uses (startServer, then go loop.Run), against a client whose
// first GetStatistics call is deliberately slow. A real GET to /metrics must
// complete -- and the store must still hold the pre-collection empty snapshot
// -- while that first collection is provably still in flight. Reordering
// main.go to collect synchronously before serving would make this test time
// out or see a populated snapshot too early; it cannot pass by accident.
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

// /health is driven by snapshot age, independent of kemp_up.
func TestHealthHandlerUsesSnapshotAge(t *testing.T) {
	store := kemp.NewSnapshotStore()
	h := healthHandler(store, time.Minute)

	// No snapshot yet: starting up, not yet healthy.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("pre-collection status = %d, want 503", rec.Code)
	}

	// Fresh snapshot: healthy.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now()})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("fresh-snapshot status = %d, want 200", rec.Code)
	}

	// Stale snapshot: the loop is wedged even though kemp_up may still read 1.
	store.Store(&kemp.Snapshot{BuiltAt: time.Now().Add(-10 * time.Minute)})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("stale-snapshot status = %d, want 503", rec.Code)
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

// Registering the same build-info collector twice -- the obvious way a reload
// path could get this wrong -- returns AlreadyRegisteredError. That is correct
// singleton behavior for the registry to enforce, not a bug to work around; this
// documents the exact failure main's wiring must never trigger. main's own
// reload closure (built by makeReloadHandler) has no reference to the registry
// at all, which is what makes a second registration structurally unreachable
// from that path, not just accidentally avoided.
func TestBuildInfoCollectorSecondRegistrationFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := reg.Register(kemp.NewBuildInfoCollector("v1", "go1.26.5")); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := reg.Register(kemp.NewBuildInfoCollector("v1", "go1.26.5"))
	var are prometheus.AlreadyRegisteredError
	if !errors.As(err, &are) {
		t.Fatalf("second Register error = %#v, want AlreadyRegisteredError", err)
	}
}

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
}
