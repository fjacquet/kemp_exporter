package kemp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Detection prefers JSON, so an appliance offering both must land on json.
func TestClientPrefersJSON(t *testing.T) {
	srv, _, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer srv.Close()

	sys := jsonSystem(t, srv)
	sys.APIKey = "alsoset"
	c, err := NewSystemClient(sys, false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	if _, err := c.GetStatistics(context.Background()); err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if got := c.TransportName(); got != "json" {
		t.Errorf("TransportName() = %q, want json", got)
	}
}

// An appliance with no JSON path must fall back to XML transparently.
func TestClientFallsBackToXML(t *testing.T) {
	var loginHits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&loginHits, 1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	sys := systemFor(t, srv)
	sys.Username, sys.Password = "bal", "secret"
	c, err := NewSystemClient(sys, false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	st, err := c.GetStatistics(context.Background())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if len(st.VirtualServices) != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", len(st.VirtualServices))
	}
	if got := c.TransportName(); got != "xml" {
		t.Errorf("TransportName() = %q, want xml", got)
	}
}

// Detection runs once per client, not once per cycle.
func TestClientCachesTransport(t *testing.T) {
	var loginHits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&loginHits, 1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	sys := systemFor(t, srv)
	sys.Username, sys.Password = "bal", "secret"
	c, err := NewSystemClient(sys, false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := c.GetStatistics(context.Background()); err != nil {
			t.Fatalf("GetStatistics #%d: %v", i, err)
		}
	}
	if n := atomic.LoadInt32(&loginHits); n != 1 {
		t.Errorf("JSON login probed %d times across 3 cycles; want 1 (cached detection)", n)
	}
}

// A system configured with only an API key must not attempt a JSON login at all.
func TestClientAPIKeyOnlySkipsJSONProbe(t *testing.T) {
	var loginHits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&loginHits, 1)
		}
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	c, err := NewSystemClient(systemFor(t, srv), false) // APIKey set, no username
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	if _, err := c.GetStatistics(context.Background()); err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if n := atomic.LoadInt32(&loginHits); n != 0 {
		t.Errorf("JSON login probed %d times with no session credentials; want 0", n)
	}
}

func TestClientListVirtualServices(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "listvs.xml"))
	}))
	defer srv.Close()

	c, err := NewSystemClient(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	vs, err := c.ListVirtualServices(context.Background())
	if err != nil {
		t.Fatalf("ListVirtualServices: %v", err)
	}
	if len(vs) != 2 || vs[0].Name != "web-https" || vs[1].Port != 80 {
		t.Fatalf("ListVirtualServices = %+v, want web-https:443 and web-http:80", vs)
	}
}

// --- Additional tests beyond the brief: negative-caching decision for Task 7 ---
//
// Carry-forward from Task 6: "No negative caching on login failure. Today every Do
// re-attempts a login when the previous one failed." The failure mode to avoid is a
// scrape loop generating one failed login per cycle forever against an appliance
// with wrong credentials -- a real LoadMaster with account lockout enabled (commonly
// 3-5 failed attempts) would lock the account within one to a few scrape intervals.
//
// Decision implemented in client.go: a confirmed credential rejection (errAuth) is
// cached for authFailureCooldown; further calls within that window return the cached
// error without making any network call at all. errUnsupported and transport-level
// failures are deliberately NOT covered by this cooldown (see client.go's doc comment
// on authFailureCooldown for why). These tests use SystemClient.now, an injectable
// clock, so the cooldown is exercised deterministically without a real sleep.

// TestClientAuthFailureCooldownSuppressesThenRecovers reproduces the exact scenario
// the brief's carry-forward warns about: JSON-only credentials that are rejected
// (401) on every request. Without a cooldown, every GetStatistics call would attempt
// a fresh login. This test asserts: (1) the first failed cycle logs in once, (2) a
// second cycle inside the cooldown window logs in zero more times, (3) a cycle after
// the cooldown elapses is allowed exactly one more login attempt, and (4) once the
// appliance starts accepting the credentials, the client detects json and the
// cooldown state does not linger.
func TestClientAuthFailureCooldownSuppressesThenRecovers(t *testing.T) {
	var logins int32
	var accept atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&logins, 1)
			if accept.Load() {
				writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"tok-123"}}}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeBytes(w, []byte(`{"status":"fail","code":401}`))
			return
		}
		writeBytes(w, fixture(t, "stats.json"))
	}))
	defer srv.Close()

	sys := jsonSystem(t, srv) // no APIKey: json is the only configured transport
	c, err := NewSystemClient(sys, false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}
	fakeNow := time.Now()
	c.now = func() time.Time { return fakeNow }

	// Cycle 1: credentials rejected, one login attempt.
	if _, err := c.GetStatistics(context.Background()); err == nil {
		t.Fatal("GetStatistics succeeded against rejected credentials; want error")
	}
	if got := atomic.LoadInt32(&logins); got != 1 {
		t.Fatalf("logins after cycle 1 = %d, want 1", got)
	}
	if got := c.TransportName(); got != "" {
		t.Errorf("TransportName() = %q after failed detection, want \"\"", got)
	}

	// Cycle 2, same instant: cooldown must suppress a second login attempt.
	if _, err := c.GetStatistics(context.Background()); err == nil {
		t.Fatal("GetStatistics succeeded against rejected credentials; want error")
	}
	if got := atomic.LoadInt32(&logins); got != 1 {
		t.Errorf("logins after cycle 2 (still within cooldown) = %d, want 1 (suppressed)", got)
	}

	// Cycle 3, after the cooldown elapses: exactly one more login attempt is allowed.
	fakeNow = fakeNow.Add(authFailureCooldown + time.Second)
	if _, err := c.GetStatistics(context.Background()); err == nil {
		t.Fatal("GetStatistics succeeded against rejected credentials; want error")
	}
	if got := atomic.LoadInt32(&logins); got != 2 {
		t.Errorf("logins after cycle 3 (cooldown elapsed) = %d, want 2", got)
	}

	// The appliance now accepts the credentials (e.g. an operator fixed them).
	// After another cooldown window, detection must succeed and land on json.
	accept.Store(true)
	fakeNow = fakeNow.Add(authFailureCooldown + time.Second)
	st, err := c.GetStatistics(context.Background())
	if err != nil {
		t.Fatalf("GetStatistics after credentials were fixed: %v", err)
	}
	if len(st.VirtualServices) != 2 {
		t.Errorf("len(VirtualServices) = %d, want 2 once credentials are valid", len(st.VirtualServices))
	}
	if got := c.TransportName(); got != "json" {
		t.Errorf("TransportName() = %q after recovery, want json", got)
	}

	// A further immediate call must succeed without the stale cooldown reasserting
	// itself (it was cleared on the successful call above).
	if _, err := c.GetStatistics(context.Background()); err != nil {
		t.Errorf("GetStatistics after recovery: %v", err)
	}
}

// TestSystemClientConcurrentGetStatistics drives GetStatistics from many goroutines
// at once. SystemClient's detected-transport cache and auth-failure-cooldown state
// are shared mutable state guarded by a mutex; this is meant to be run with -race so
// a missing or incorrect lock shows up as a data race rather than a silently-wrong
// result.
func TestSystemClientConcurrentGetStatistics(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	c, err := NewSystemClient(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("NewSystemClient: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.GetStatistics(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent GetStatistics: %v", err)
		}
	}
	if got := c.TransportName(); got != "xml" {
		t.Errorf("TransportName() = %q, want xml", got)
	}
}
