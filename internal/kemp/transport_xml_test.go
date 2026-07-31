package kemp

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/models"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// systemFor points a config.System at a test server. The test servers use
// self-signed certificates, so verification is disabled here only.
func systemFor(t *testing.T, srv *httptest.Server) config.System {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port := 0
	if _, err := fmtSscan(u.Port(), &port); err != nil {
		t.Fatalf("parse port %q: %v", u.Port(), err)
	}
	var sys config.System
	sys.Name = "lm-test"
	sys.Host = u.Hostname()
	sys.Port = port
	sys.APIKey = "testkey"
	sys.InsecureSkipVerify = insecureTrue(t)
	return sys
}

func TestXMLTransportDecodesStats(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("apikey")
		w.Header().Set("Content-Type", "application/xml")
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/access/stats" {
		t.Errorf("path = %q, want /access/stats", gotPath)
	}
	if gotKey != "testkey" {
		t.Errorf("apikey = %q, want testkey", gotKey)
	}
	if len(st.VirtualServices) != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", len(st.VirtualServices))
	}
	if v, ok := st.Totals.ConnsPerSec.Get(); !ok || v != 150 {
		t.Errorf("Totals.ConnsPerSec = %v/%v, want 150/true", v, ok)
	}
	if tr.Name() != "xml" {
		t.Errorf("Name() = %q, want xml", tr.Name())
	}
}

func TestXMLTransportAuthFailureIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		writeBytes(w, []byte(`<Response stat="401"><Error>Invalid API key</Error></Response>`))
	}))
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded on 401; want error")
	}
	if calls != 1 {
		t.Errorf("server received %d requests; 4xx must not be retried (want 1)", calls)
	}
}

func TestXMLTransportDecodesListVS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "listvs.xml"))
	}))
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var out struct {
		VS []models.VirtualServiceInfo `xml:"VS"`
	}
	if err := tr.Do(context.Background(), "listvs", nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(out.VS) != 2 || out.VS[0].Name != "web-https" {
		t.Fatalf("VS = %+v, want two entries starting with web-https", out.VS)
	}
}

// TestXMLTransportRejectsNonOKStatWithoutErrorElement covers an appliance response
// shape the fixtures don't: HTTP 200 with a Response stat attribute reporting a
// rejected credential (401/403), but with no <Error> child element -- so the
// env.Error == "" check in Do never fires and, without a check on env.Stat, this
// used to fall straight through to decodeSuccessData and return a decode error
// ("locate Success>Data: EOF") that reads like a truncated response and, more
// importantly, does not satisfy errors.Is(err, errAuth) for a caller that needs
// to distinguish "reject, do not retry" from "malformed payload".
func TestXMLTransportRejectsNonOKStatWithoutErrorElement(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, []byte(`<Response stat="401"><Success/></Response>`))
	}))
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("Do succeeded with stat=\"401\" and no <Error> element; want an error")
	}
	if !errors.Is(err, errAuth) {
		t.Errorf("err = %v; want errors.Is(err, errAuth)", err)
	}
}

// TestDecodeSuccessDataRequiresDirectChild guards decodeSuccessData against
// decoding a Data element that is nested under an extra wrapper inside Success,
// rather than being a direct child of it. Canonical LoadMaster responses never
// have this shape, so this is robustness, not a real-world regression fixture --
// but decodeSuccessData's token loop, absent depth tracking, will happily locate
// and decode any Data anywhere inside Success, silently accepting a payload from
// the wrong place in the tree.
func TestDecodeSuccessDataRequiresDirectChild(t *testing.T) {
	body := []byte(`<Response><Success><Wrap><Data><Foo>1</Foo></Data></Wrap></Success></Response>`)
	var out struct {
		Foo string `xml:"Foo"`
	}
	if err := decodeSuccessData(body, &out); err == nil {
		t.Fatalf("decodeSuccessData decoded a Data nested under an extra wrapper "+
			"inside Success; want an error since Data must be a direct child (got Foo=%q)", out.Foo)
	}
}

// TestDecodeSuccessDataIgnoresSiblingData guards decodeSuccessData against
// decoding a Data element that is a sibling of Success (i.e. appears after
// Success has already closed), rather than a child of it.
func TestDecodeSuccessDataIgnoresSiblingData(t *testing.T) {
	body := []byte(`<Response><Success/><Data><Foo>1</Foo></Data></Response>`)
	var out struct {
		Foo string `xml:"Foo"`
	}
	if err := decodeSuccessData(body, &out); err == nil {
		t.Fatalf("decodeSuccessData decoded a Data that is a sibling of Success, "+
			"not a child of it; want an error (got Foo=%q)", out.Foo)
	}
}

// TLS 1.2 is the family floor, and this is the only test guarding it.
//
// The previous version of this test could not fail. It set only MaxVersion on the
// test server, leaving the stdlib server's own default MinVersion at TLS 1.2, so
// negotiation was impossible no matter what the client offered -- the handshake was
// refused by GO'S OWN SERVER, not by this repo's client, and lowering
// tlsconfig.go's MinVersion to TLS 1.0 left the test green. (Confirmed by mutation
// both before and after this rewrite.)
//
// So the server here is WILLING to speak TLS 1.1: MinVersion AND MaxVersion are
// both pinned below the floor. The only party left that can refuse is the client,
// which is the invariant under test.
func TestXMLTransportRefusesTLSBelow12(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	srv.TLS = &tls.Config{
		MinVersion: tls.VersionTLS10,
		MaxVersion: tls.VersionTLS11,
	}
	srv.StartTLS()
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	err = tr.Do(context.Background(), "stats", nil, &st)
	if err == nil {
		t.Fatal("the client completed a handshake with a TLS 1.1-only server; " +
			"the TLS 1.2 floor is not being enforced by this client")
	}
	// Assert on the REASON, not merely that something failed: a typo'd URL or a
	// closed port would also produce a non-nil error here.
	if msg := strings.ToLower(err.Error()); !strings.Contains(msg, "protocol version") &&
		!strings.Contains(msg, "tls") {
		t.Fatalf("error = %v, want a TLS version-negotiation failure", err)
	}
}

// The direct statement of the same invariant, independent of any handshake: the
// floor is a property of the config this package builds.
func TestTLSConfigPinsMinVersion12(t *testing.T) {
	got := tlsConfigFor(configSystem{})
	if got.MinVersion != tls.VersionTLS12 {
		t.Errorf("tlsConfigFor().MinVersion = %#x, want %#x (TLS 1.2, the family floor)",
			got.MinVersion, tls.VersionTLS12)
	}
	if got.InsecureSkipVerify {
		t.Error("tlsConfigFor() defaulted InsecureSkipVerify to true; it must come from per-target config and default false")
	}
}
