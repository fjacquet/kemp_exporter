package kemp

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/config"
)

// unstartedTLS11Server builds an httptest TLS server capped at TLS 1.1, so any
// client enforcing the family's TLS 1.2 floor fails the handshake against it.
// Started (but not yet serving real requests) so the caller can start it and
// defer Close().
func unstartedTLS11Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
	srv.StartTLS()
	return srv
}

// countingErrRoundTripper always fails at the transport level (no HTTP response at
// all), counting invocations so retry behavior can be asserted without depending
// on real network timing, a flaky closed-port dial, or DNS resolution.
type countingErrRoundTripper struct {
	calls int32
}

func (rt *countingErrRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	atomic.AddInt32(&rt.calls, 1)
	return nil, errors.New("simulated transport failure")
}

// TestNewRestyClientRetriesTransportError guards the retry condition in
// newRestyClient. resty always hands the condition a non-nil *Response on a
// transport-level failure -- only its RawResponse field is nil -- so a condition
// that checks `r == nil` never sees that branch taken, and StatusCode() on a
// Response with a nil RawResponse reports 0, which is never >= 500. The net
// effect, if the retry condition is written the naive way, is that every
// transport-level failure (dropped connection, TLS handshake failure, DNS
// failure) is silently never retried.
func TestNewRestyClientRetriesTransportError(t *testing.T) {
	var sys config.System
	sys.Host = "127.0.0.1"
	sys.Port = 1
	sys.APIKey = "testkey"

	c, err := newRestyClient(sys, false)
	if err != nil {
		t.Fatalf("newRestyClient: %v", err)
	}
	rt := &countingErrRoundTripper{}
	c.SetTransport(rt)
	tr := &xmlTransport{client: c, apiKey: sys.APIKey}

	var out struct{}
	if err := tr.Do(context.Background(), "stats", nil, &out); err == nil {
		t.Fatal("Do succeeded against a transport that always errors; want error")
	}

	const wantAttempts = 4 // 1 initial attempt + SetRetryCount(3) retries
	if got := atomic.LoadInt32(&rt.calls); got != wantAttempts {
		t.Errorf("RoundTrip called %d times; want %d (transport errors must be retried)", got, wantAttempts)
	}
}

// TestSanitizeTransportErrorHidesAPIKey guards sanitizeTransportError directly:
// wrapping a *url.Error whose URL carries a credential-bearing query string must
// not leak that URL into the returned error's message, but should keep the host
// so an operator running several LoadMasters can tell which target failed.
func TestSanitizeTransportErrorHidesAPIKey(t *testing.T) {
	srv := unstartedTLS11Server(t)
	defer srv.Close()

	sys := systemFor(t, srv)
	tr, err := newXMLTransport(sys, false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var out struct{}
	err = tr.Do(context.Background(), "stats", nil, &out)
	if err == nil {
		t.Fatal("Do succeeded against a TLS 1.1-only server; want failure")
	}
	if strings.Contains(err.Error(), sys.APIKey) {
		t.Fatalf("error message leaked the API key: %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "apikey=") {
		t.Fatalf("error message leaked the apikey query parameter: %v", err)
	}
	if !strings.Contains(err.Error(), sys.Host) {
		t.Errorf("error message %q dropped the target host %q; an operator with several"+
			" LoadMasters configured needs to know which one failed", err.Error(), sys.Host)
	}
}

// TestNewRestyClientSilencesRestyDefaultLogger guards the SetLogger(discardLogger{})
// call in newRestyClient. resty.New() unconditionally installs a default logger
// that writes straight to os.Stderr -- via the standard log package, not logrus,
// and independent of the trace flag -- and logs the full request URL (apikey
// query parameter included) on every retry attempt and on final failure. os.Stderr
// is redirected to a pipe *before* constructing the client, since resty's default
// logger binds whatever os.Stderr points to at construction time; if
// SetLogger(discardLogger{}) were ever removed, that default logger would write
// its "WARN RESTY ...<url with apikey>..." lines into this captured pipe, and the
// assertion below would catch it.
func TestNewRestyClientSilencesRestyDefaultLogger(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { os.Stderr = origStderr }()
	os.Stderr = w

	srv := unstartedTLS11Server(t)
	defer srv.Close()
	sys := systemFor(t, srv)

	tr, newErr := newXMLTransport(sys, false)
	if newErr != nil {
		_ = w.Close()
		t.Fatalf("newXMLTransport: %v", newErr)
	}

	var out struct{}
	_ = tr.Do(context.Background(), "stats", nil, &out) // failure expected; only stderr output is under test

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("close pipe writer: %v", closeErr)
	}
	captured, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stderr: %v", readErr)
	}
	if len(captured) > 0 {
		t.Fatalf("resty's default logger wrote to stderr despite discardLogger; got: %s", captured)
	}
}
