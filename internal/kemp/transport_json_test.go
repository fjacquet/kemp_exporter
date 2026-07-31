package kemp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestJSONLoginDecodesRegardlessOfContentType guards against relying on resty's
// SetResult, which only auto-unmarshals when the *response* Content-Type sniffs
// as JSON or XML (resty v2.17.2 middleware.go:408-416). A LoadMaster that
// answers a perfectly valid login body with a Content-Type of "text/plain" (or
// no header at all, which Go's ResponseWriter defaults to via content sniffing)
// must still have its token decoded -- otherwise a JSON-capable appliance is
// wrongly classified as errUnsupported and silently downgraded to XML.
func TestJSONLoginDecodesRegardlessOfContentType(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			// Deliberately no Content-Type header: a valid token body answered
			// with a sniffed/absent content type, not "application/json".
			writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"tok-123"}}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeBytes(w, fixture(t, "stats.json"))
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err != nil {
		t.Fatalf("Do: %v (login response Content-Type must not affect token decoding)", err)
	}
}

// TestJSONLoginNonJSONBodyAt200IsUnsupported covers the mirror image of the
// content-type fix above: a login response that fails to parse as JSON at all
// (not merely mislabeled) must still be treated as "this firmware doesn't
// speak JSON" -- the same signal a 404 on /access/login gives -- rather than
// a hard failure that stops Task 7 from ever falling back to XML. The XML
// transport's own login-equivalent shares the same /access/<cmd> namespace,
// so a JSON-less firmware answering POST /access/login with an XML body at
// HTTP 200 is a real, not hypothetical, shape.
func TestJSONLoginNonJSONBodyAt200IsUnsupported(t *testing.T) {
	var cmdHits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			w.Header().Set("Content-Type", "text/xml")
			writeBytes(w, []byte(`<Response stat="200"><Success><Data><Token>ignored</Token></Data></Success></Response>`))
			return
		}
		atomic.AddInt32(&cmdHits, 1)
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded against an XML-at-200 login body; want error")
	}
	if !isUnsupported(err) {
		t.Errorf("error = %v, want errors.Is(err, errUnsupported) -- an XML login body at HTTP 200 means this firmware doesn't speak JSON, the same as a 404 would", err)
	}
	if got := atomic.LoadInt32(&cmdHits); got != 0 {
		t.Errorf("command endpoint hit %d times; login should have failed before any command call", got)
	}
}

// TestJSONLoginTruncatedJSONBodyIsHardError is the other direction: a login
// body that clearly starts as JSON but is truncated/corrupt must NOT be
// classified as errUnsupported. That shape means the appliance does speak
// JSON but returned a broken response -- a real fault that Task 7 must
// propagate, not silently mask by downgrading to XML.
func TestJSONLoginTruncatedJSONBodyIsHardError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			w.Header().Set("Content-Type", "application/json")
			writeBytes(w, []byte(`{"status":"ok"`)) // truncated: syntactically invalid JSON
			return
		}
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded against a truncated JSON login body; want error")
	}
	if isUnsupported(err) {
		t.Errorf("error = %v classified as unsupported; a truncated JSON body is a real fault from a JSON-capable appliance, not a signal to fall back to XML", err)
	}
}

// TestJSONEnvelopeRejectsAPIError exercises the appliance-level error path
// (env.Error != "" in Do), which requires the login to actually succeed first.
// The login handler sets Content-Type explicitly and the assertion checks the
// error text names the appliance's own message, not just err != nil -- a bare
// non-nil check here previously passed for the wrong reason (the login
// silently failed the no-token check because the login handler's response had
// no Content-Type, and pre-fix code depended on that header via SetResult),
// leaving this envelope path with zero real coverage.
func TestJSONEnvelopeRejectsAPIError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded on an appliance-level error payload; want error")
	}
	if isUnsupported(err) {
		t.Fatalf("error = %v classified as unsupported; login must have succeeded so this exercises the envelope-error path, not the login path", err)
	}
	if !strings.Contains(err.Error(), "bad parameter") {
		t.Errorf("error = %v, want it to surface the appliance's Error message %q", err, "bad parameter")
	}
}

// TestJSONEnvelopeStatusCodeIsChecked is the JSON counterpart of the XML
// transport's TestXMLTransportRejectsNonOKStatWithoutErrorElement. The JSON
// envelope's status/code fields were decoded but never read: an appliance can
// report a rejected credential, or any other appliance-level failure, purely
// through status/code with no "Error" string at all -- HTTP 200 with a body
// like `{"status":"fail","code":401,"Success":{"Data":{...}}}`. Left
// unchecked, that shape falls straight through to the Data decode and is
// reported as success (row 1), or -- when Success.Data is entirely absent --
// as a generic "empty payload" that does not satisfy errors.Is(err, errAuth)
// (row 3), even though the appliance is unambiguously reporting a rejected
// credential in both cases.
func TestJSONEnvelopeStatusCodeIsChecked(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantAuth bool
	}{
		{
			name:     "fail status with a Data payload must not be reported as success",
			body:     `{"status":"fail","code":401,"Success":{"Data":{"VStotals":{}}}}`,
			wantAuth: true,
		},
		{
			name:     "fail status with a non-auth code must not be reported as success",
			body:     `{"status":"fail","code":500,"Success":{"Data":{"VStotals":{}}}}`,
			wantAuth: false,
		},
		{
			name:     "fail status with no Data at all must still be classified as auth, not empty payload",
			body:     `{"status":"fail","code":401}`,
			wantAuth: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/access/login" {
					writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"t"}}}`))
					return
				}
				writeBytes(w, []byte(tc.body))
			}))
			defer srv.Close()

			tr, err := newJSONTransport(jsonSystem(t, srv), false)
			if err != nil {
				t.Fatalf("newJSONTransport: %v", err)
			}
			var st models.Statistics
			err = tr.Do(context.Background(), "stats", nil, &st)
			if err == nil {
				t.Fatalf("Do succeeded on body %s; want error", tc.body)
			}
			if got := errors.Is(err, errAuth); got != tc.wantAuth {
				t.Errorf("errors.Is(err, errAuth) = %v, want %v (err = %v)", got, tc.wantAuth, err)
			}
		})
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

// TestJSONTransportRefreshCollapsesConcurrentStampede reproduces the review's
// measured finding: a naive refresh() that unconditionally re-logs-in lets N
// concurrent Do calls that all discover the SAME stale token independently
// each trigger their own login, stampeding the appliance -- exactly what the
// no-4xx-retry policy exists to prevent when credentials are wrong (LoadMaster
// account lockout thresholds are commonly 3-5 failed attempts). A warm-up
// primes the cache with one token via one successful command call; the token
// then "expires" for good (every following command call returns 401
// regardless of which token is presented), and 20 concurrent Do calls all
// discover that at roughly the same time. They must collapse onto one shared
// login for that stale generation, not one each.
func TestJSONTransportRefreshCollapsesConcurrentStampede(t *testing.T) {
	var logins int32
	var expired atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&logins, 1)
			writeBytes(w, []byte(`{"status":"ok","code":200,"Success":{"Data":{"token":"tok-123"}}}`))
			return
		}
		if expired.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			writeBytes(w, []byte(`{"status":"fail","code":401}`))
			return
		}
		writeBytes(w, fixture(t, "stats.json"))
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}

	// Warm up: exactly one login, one successful command.
	var warm models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &warm); err != nil {
		t.Fatalf("warm-up Do: %v", err)
	}
	if got := atomic.LoadInt32(&logins); got != 1 {
		t.Fatalf("warm-up: logins = %d, want 1", got)
	}

	// The cached token now expires for good.
	expired.Store(true)

	const goroutines = 20
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var st models.Statistics
			_ = tr.Do(context.Background(), "stats", nil, &st) // every call fails (still expired post-refresh); only the login count matters
		}()
	}
	wg.Wait()

	const wantLogins = 2 // warm-up's 1 + exactly one shared refresh for the 20 concurrent calls
	if got := atomic.LoadInt32(&logins); got != wantLogins {
		t.Errorf("logins = %d after %d concurrent Do calls sharing one stale token, want %d (collapsed to a single shared login)", got, goroutines, wantLogins)
	}
}

// TestJSONTransportEnsureCollapsesConcurrentStampedeOnBadCredentials
// reproduces the review's other measured finding: with no cached token at
// all (a cold session), 20 concurrent Do calls against rejected credentials
// each triggered their own login attempt -- 20 failed logins from one scrape
// round, comfortably past a typical 3-5 attempt LoadMaster lockout threshold.
// They must collapse onto a single shared login attempt instead.
func TestJSONTransportEnsureCollapsesConcurrentStampedeOnBadCredentials(t *testing.T) {
	var logins, cmds int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/access/login" {
			atomic.AddInt32(&logins, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			writeBytes(w, []byte(`{"status":"fail","code":401}`))
			return
		}
		atomic.AddInt32(&cmds, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	tr, err := newJSONTransport(jsonSystem(t, srv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var st models.Statistics
			err := tr.Do(context.Background(), "stats", nil, &st)
			if !errors.Is(err, errAuth) {
				t.Errorf("Do = %v, want errors.Is(err, errAuth)", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&cmds); got != 0 {
		t.Errorf("command endpoint hit %d times; ensure must fail before any command call is attempted", got)
	}
	const wantLogins = 1
	if got := atomic.LoadInt32(&logins); got != wantLogins {
		t.Errorf("logins = %d across %d concurrent Do calls with rejected credentials, want %d (collapsed to a single shared login attempt)", got, goroutines, wantLogins)
	}
}

var _ = json.Marshal // keep the json import meaningful if tests are trimmed
