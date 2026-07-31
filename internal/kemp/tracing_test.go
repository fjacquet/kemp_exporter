package kemp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/models"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// TestTracingDoesNotLeakAPIKey exercises installTracing (trace=true) against a real
// request and asserts the apikey query parameter never shows up in any logged field.
// r.Request.URL in resty is the fully resolved URL, query string included, so a
// tracing hook that logs it verbatim would put the credential in every trace line.
func TestTracingDoesNotLeakAPIKey(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer srv.Close()

	// installTracing logs through the package-level logrus calls, i.e. the standard
	// logger, so the test hook has to attach there rather than to a private instance.
	prevLevel := logrus.GetLevel()
	logrus.SetLevel(logrus.DebugLevel)
	defer logrus.SetLevel(prevLevel)
	hook := logrustest.NewGlobal()

	sys := systemFor(t, srv)
	tr, err := newXMLTransport(sys, true) // trace=true: installTracing is wired up
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if len(hook.AllEntries()) == 0 {
		t.Fatal("expected at least one trace log entry")
	}
	for _, e := range hook.AllEntries() {
		for field, v := range e.Data {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if strings.Contains(s, sys.APIKey) {
				t.Fatalf("log field %q leaked the API key: %s", field, s)
			}
			if strings.Contains(strings.ToLower(s), "apikey=") {
				t.Fatalf("log field %q leaked the apikey query parameter: %s", field, s)
			}
		}
	}
}

// TestJSONLoginResponseTokenNotLogged exercises the JSON transport's login flow
// end to end with trace=true and asserts the session token never appears in any
// logged field. installTracing's auth-path body suppression (isAuthPath, matched
// against the redacted request path) is the only thing standing between the
// login response body -- which carries the token in Success.Data.token -- and
// the trace log. This complements Task 5's isAuthPath fragment check by proving
// the JSON transport's actual login path ("/access/login") is covered by it, not
// just asserting the fragment list contains "login" in the abstract.
func TestJSONLoginResponseTokenNotLogged(t *testing.T) {
	srv, _, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer srv.Close()

	prevLevel := logrus.GetLevel()
	logrus.SetLevel(logrus.DebugLevel)
	defer logrus.SetLevel(prevLevel)
	hook := logrustest.NewGlobal()

	tr, err := newJSONTransport(jsonSystem(t, srv), true) // trace=true: installTracing wired up
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}
	var st models.Statistics
	if err := tr.Do(context.Background(), "stats", nil, &st); err != nil {
		t.Fatalf("Do: %v", err)
	}

	const token = "tok-123" // the token jsonServer's login handler returns
	if len(hook.AllEntries()) == 0 {
		t.Fatal("expected at least one trace log entry")
	}
	for _, e := range hook.AllEntries() {
		for field, v := range e.Data {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if strings.Contains(s, token) {
				t.Fatalf("log field %q leaked the session token: %s", field, s)
			}
		}
	}
}

// TestRedactQueryFailsClosedOnParseError asserts redactQuery reports failure
// (rather than silently returning "") when its input does not parse as a URL.
// installTracing's isAuthPath check runs on redactQuery's output; a redactQuery
// that fails open by returning ("", nil-equivalent-success) would make
// isAuthPath("") report false, and a would-be-auth response would fall through
// to the branch that logs the full body -- exactly the leak the auth-path skip
// exists to prevent for the JSON transport's login response.
func TestRedactQueryFailsClosedOnParseError(t *testing.T) {
	// An invalid percent-encoding escape makes url.Parse itself fail.
	const unparseable = "http://example.com/access/stats%zz?apikey=secret"
	path, ok := redactQuery(unparseable)
	if ok {
		t.Fatalf("redactQuery(%q) = (%q, true); want ok=false for an unparseable URL so installTracing fails closed", unparseable, path)
	}
}
