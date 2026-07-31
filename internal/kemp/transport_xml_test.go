package kemp

import (
	"context"
	"crypto/tls"
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

// TLS 1.2 is the family floor. This asserts the client refuses to negotiate below it.
func TestXMLTransportMinTLS12(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
	srv.StartTLS()
	defer srv.Close()

	tr, err := newXMLTransport(systemFor(t, srv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err == nil {
		t.Fatal("handshake succeeded against a TLS 1.1 server; want failure")
	} else if !strings.Contains(strings.ToLower(err.Error()), "tls") &&
		!strings.Contains(strings.ToLower(err.Error()), "protocol version") {
		t.Logf("got error %v (accepted: any handshake failure)", err)
	}
}
