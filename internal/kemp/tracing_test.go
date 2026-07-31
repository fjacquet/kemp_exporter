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
