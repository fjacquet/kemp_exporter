package kemp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// jsonServer serves the login endpoint plus one command, counting calls to each.
func jsonServer(t *testing.T, payload []byte, failFirstAuth bool) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var logins, cmds int32
	var authFailed atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/access/login":
			atomic.AddInt32(&logins, 1)
			w.Header().Set("Content-Type", "application/json")
			writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"tok-123"}}}`))
		default:
			atomic.AddInt32(&cmds, 1)
			if failFirstAuth && !authFailed.Load() {
				authFailed.Store(true)
				w.WriteHeader(http.StatusUnauthorized)
				writeBytes(w, []byte(`{"status":"fail","code":401}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			writeBytes(w, payload)
		}
	}))
	return srv, &logins, &cmds
}

func jsonSystem(t *testing.T, srv *httptest.Server) (sys configSystem) {
	t.Helper()
	s := systemFor(t, srv)
	s.APIKey = ""
	s.Username = "bal"
	s.Password = "secret"
	return s
}

func TestJSONTransportDecodesStats(t *testing.T) {
	srv, logins, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if tr.Name() != "json" {
		t.Errorf("Name() = %q, want json", tr.Name())
	}
	if len(st.VirtualServices) != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", len(st.VirtualServices))
	}
	if *logins != 1 {
		t.Errorf("login called %d times, want 1 (lazy, cached)", *logins)
	}

	// A second command must reuse the cached session rather than logging in again.
	var st2 models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st2); err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if *logins != 1 {
		t.Errorf("login called %d times after two commands, want 1", *logins)
	}
}

// A 401 mid-session means the token expired: re-login once and retry the command.
func TestJSONTransportRefreshesOnceOn401(t *testing.T) {
	srv, logins, cmds := jsonServer(t, fixture(t, "stats.json"), true)
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if *logins != 2 {
		t.Errorf("login called %d times, want 2 (initial + one refresh)", *logins)
	}
	if *cmds != 2 {
		t.Errorf("command called %d times, want 2 (401 then success)", *cmds)
	}
	if len(st.VirtualServices) != 2 {
		t.Errorf("payload not decoded after refresh: %d virtual services", len(st.VirtualServices))
	}
}

// A 404 on login means this firmware has no JSON path — the signal for detection
// to fall back to XML, not a hard failure.
func TestJSONTransportUnsupportedFirmware(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded against a firmware with no JSON path; want error")
	}
	if !isUnsupported(err) {
		t.Errorf("error = %v, want one classified as unsupported", err)
	}
}

func TestJSONEnvelopeRejectsAPIError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"t"}}}`))
			return
		}
		writeBytes(w, []byte(`{"status":"fail","code":422,"Error":"bad parameter"}`))
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err == nil {
		t.Fatal("Do succeeded on an appliance-level error payload; want error")
	}
}

// TestJSONTransportSessionSafeForConcurrentDo drives Do from many goroutines at
// once against a single jsonTransport. The session token is shared mutable state
// (auth.go's session), guarded by a mutex; this test is meant to be run with
// -race so a missing or incorrect lock shows up as a data race rather than a
// silently-wrong result. It also asserts only one login happens despite the
// concurrent first calls, since session.ensure must not double-login under a race.
func TestJSONTransportSessionSafeForConcurrentDo(t *testing.T) {
	srv, logins, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var st models.Statistics
			errs <- tr.Do(context.Background(), "stats", nil, &st)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Do: %v", err)
		}
	}
	if *logins != 1 {
		t.Errorf("login called %d times across %d concurrent Do calls, want 1", *logins, goroutines)
	}
}

var _ = json.Marshal // keep the json import meaningful if tests are trimmed
