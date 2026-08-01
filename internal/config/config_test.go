package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != "9448" {
		t.Errorf("Server.Port = %q, want \"9448\"", cfg.Server.Port)
	}
	if cfg.Server.URI != "/metrics" {
		t.Errorf("Server.URI = %q, want \"/metrics\"", cfg.Server.URI)
	}
	if cfg.Collection.Interval != 60*time.Second {
		t.Errorf("Collection.Interval = %v, want 60s", cfg.Collection.Interval)
	}
	if cfg.Collection.Timeout != 60*time.Second {
		t.Errorf("Collection.Timeout = %v, want 60s", cfg.Collection.Timeout)
	}
	if cfg.Collection.MaxConcurrent != 4 {
		t.Errorf("Collection.MaxConcurrent = %d, want 4", cfg.Collection.MaxConcurrent)
	}
	if got := cfg.Systems[0].BaseURL(); got != "https://10.0.0.1:443" {
		t.Errorf("BaseURL = %q, want https://10.0.0.1:443", got)
	}
	if cfg.Systems[0].InsecureSkipVerify.Value() {
		t.Error("InsecureSkipVerify defaulted true; must default false")
	}
}

func TestLoadInterpolatesEnv(t *testing.T) {
	t.Setenv("KEMP1_HOSTNAME", "lm.example.com")
	t.Setenv("KEMP1_APIKEY", "abc123")
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: ${KEMP1_HOSTNAME}
    apiKey: ${KEMP1_APIKEY}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Systems[0].Host != "lm.example.com" {
		t.Errorf("Host = %q, want lm.example.com", cfg.Systems[0].Host)
	}
	if cfg.Systems[0].APIKey != "abc123" {
		t.Errorf("APIKey = %q, want abc123", cfg.Systems[0].APIKey)
	}
}

// An unset ${VAR} must fail at load, not silently produce an empty credential
// that turns into repeated runtime auth failures.
func TestLoadFailsFastOnUnsetEnv(t *testing.T) {
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: ${KEMP_DEFINITELY_UNSET_VAR}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with an unset env reference; want error")
	}
}

func TestLoadReadsAPIKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "apikey")
	if err := os.WriteFile(keyPath, []byte("  filekey\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKeyFile: `+keyPath+`
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Systems[0].APIKey != "filekey" {
		t.Errorf("APIKey = %q, want \"filekey\" (trimmed)", cfg.Systems[0].APIKey)
	}
}

func TestLoadRejectsEmptySystems(t *testing.T) {
	path := writeConfig(t, "systems: []\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with no systems; want error")
	}
}

// A negative Collection.Interval reaches time.NewTicker in the collection loop's
// Run, which panics on a non-positive duration -- a bad config value must fail
// loudly here, at load time with a message naming the field, not take the
// process down in a background goroutine after it has been up for a cycle.
func TestLoadRejectsNegativeInterval(t *testing.T) {
	path := writeConfig(t, `
collection:
  interval: "-1s"
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with a negative collection.interval; want error")
	}
	if !strings.Contains(err.Error(), "interval") || !strings.Contains(err.Error(), "-1s") {
		t.Errorf("error = %q, want it to name the field (interval) and the offending value (-1s)", err.Error())
	}
}

func TestLoadRejectsNegativeTimeout(t *testing.T) {
	path := writeConfig(t, `
collection:
  timeout: "-1s"
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with a negative collection.timeout; want error")
	}
	if !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), "-1s") {
		t.Errorf("error = %q, want it to name the field (timeout) and the offending value (-1s)", err.Error())
	}
}

// A negative MaxConcurrent is rejected at the config layer entirely: errgroup's
// own "negative means unlimited" convention is not exposed as a config knob (see
// the collection loop's own clamp for why silently honoring it there would be
// worse, not better). A directly-constructed config.Collection{} can still hit
// that clamp; a value loaded from YAML cannot.
func TestLoadRejectsNegativeMaxConcurrent(t *testing.T) {
	path := writeConfig(t, `
collection:
  maxConcurrent: -1
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with a negative collection.maxConcurrent; want error")
	}
	if !strings.Contains(err.Error(), "maxConcurrent") || !strings.Contains(err.Error(), "-1") {
		t.Errorf("error = %q, want it to name the field (maxConcurrent) and the offending value (-1)", err.Error())
	}
}

// Two plausible-looking server.uri values reach the mux with no defense at
// all today: "metrics" (no leading slash) panics http.ServeMux's pattern
// parser at startup ("host/path missing /"), and "/" collides with the "/"
// landing-page pattern ("conflicts with pattern"). Every other misconfigured
// field in this file fails loudly here, at load time, naming the field --
// these two instead produce a stack trace from deep inside net/http. Load
// must reject both the same way.
func TestLoadRejectsURIWithoutLeadingSlash(t *testing.T) {
	path := writeConfig(t, `
server:
  uri: "metrics"
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with server.uri lacking a leading slash; want error")
	}
	if !strings.Contains(err.Error(), "uri") {
		t.Errorf("error = %q, want it to name the field (uri)", err.Error())
	}
}

func TestLoadRejectsRootURI(t *testing.T) {
	path := writeConfig(t, `
server:
  uri: "/"
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with server.uri set to \"/\" (collides with the landing page); want error")
	}
	if !strings.Contains(err.Error(), "uri") {
		t.Errorf("error = %q, want it to name the field (uri)", err.Error())
	}
}

// server.uri is handed straight to http.ServeMux, which PANICS -- from deep
// inside net/http, naming no config field -- on a pattern that conflicts with
// one the exporter already registers ("/health", "/") or that its pattern
// parser rejects (a '{' wildcard segment, or whitespace, which ServeMux reads
// as a method prefix). Load must reject all of these itself, naming the field.
func TestLoadRejectsUnusableServerURI(t *testing.T) {
	for _, uri := range []string{"/health", "/met{rics", "/a b", "/a\tb", "/metrics/{id}", "/met}rics"} {
		t.Run(uri, func(t *testing.T) {
			path := writeConfig(t, `
server:
  uri: "`+strings.ReplaceAll(strings.ReplaceAll(uri, "\\", "\\\\"), "\t", "\\t")+`"
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load succeeded with server.uri = %q, which panics http.ServeMux; want error", uri)
			}
			if !strings.Contains(err.Error(), "server.uri") {
				t.Errorf("error = %q, want it to name the field (server.uri)", err.Error())
			}
		})
	}
}

// The counterpart to the rejections above: a plain nested path is legal and must
// keep loading, so the new validation cannot be satisfied by rejecting everything.
func TestLoadAcceptsNestedServerURI(t *testing.T) {
	path := writeConfig(t, `
server:
  uri: "/kemp/metrics"
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load rejected a legal nested server.uri: %v", err)
	}
	if cfg.Server.URI != "/kemp/metrics" {
		t.Errorf("Server.URI = %q, want \"/kemp/metrics\"", cfg.Server.URI)
	}
}

// Config.Systems[].Name becomes the `system` label on every metric this exporter
// emits. Two systems sharing a name -- including two that both omitted `name`,
// which was not an error -- produce byte-identical label tuples, and both readers'
// first-wins dedup then drops the second appliance's metrics entirely. /metrics
// and /health stay green while an entire LoadMaster is unmonitored, so this has to
// fail at load time.
func TestLoadRejectsMissingSystemName(t *testing.T) {
	path := writeConfig(t, `
systems:
  - host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with a system that has no name; want error")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error = %q, want it to name the field (name)", err.Error())
	}
}

func TestLoadRejectsDuplicateSystemNames(t *testing.T) {
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
  - name: lm-01
    host: 10.0.0.2
    apiKey: secret2
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with two systems sharing a name; want error")
	}
	if !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "lm-01") {
		t.Errorf("error = %q, want it to name the field (name) and the offending value (lm-01)", err.Error())
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error = %q leaks a credential", err.Error())
	}
}

// The uniqueness check above compares raw names, but the `system` label carries the
// name after invalid UTF-8 is replaced with U+FFFD (internal/kemp's cleanValue).
// Two names differing only in their invalid bytes are therefore distinct here and
// identical as labels -- which reinstates exactly the collision the uniqueness check
// exists to prevent. Rejecting invalid UTF-8 outright keeps the two comparisons
// equivalent: unlike an appliance-reported service name, an operator-authored config
// name with a bad byte is a config error we can surface at load time rather than
// data we would lose by refusing.
func TestLoadRejectsInvalidUTF8SystemName(t *testing.T) {
	// The invalid byte arrives through ${ENV} interpolation, not the YAML literal:
	// the YAML parser rejects a raw invalid byte in the document itself, so env
	// substitution is the route by which one actually reaches the `system` label.
	t.Setenv("KEMP_BAD_NAME", "lm-\x80")
	path := writeConfig(t, `
systems:
  - name: ${KEMP_BAD_NAME}
    host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with a system name containing invalid UTF-8; want error")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error = %q, want it to name the field (name)", err.Error())
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error = %q leaks a credential", err.Error())
	}
}

func TestLoadRejectsNegativeOTelInterval(t *testing.T) {
	path := writeConfig(t, `
otel:
  interval: "-1s"
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: secret
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded with a negative otel.interval; want error")
	}
	if !strings.Contains(err.Error(), "interval") || !strings.Contains(err.Error(), "-1s") {
		t.Errorf("error = %q, want it to name the field (interval) and the offending value (-1s)", err.Error())
	}
}

// SafeConfig is what gets logged. A credential appearing in its output is a
// security defect, so this test guards it directly.
func TestSafeConfigRedactsCredentials(t *testing.T) {
	path := writeConfig(t, `
systems:
  - name: lm-01
    host: 10.0.0.1
    apiKey: SUPERSECRETKEY
    password: ALSOSECRET
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := SafeConfig(cfg)
	for _, secret := range []string{"SUPERSECRETKEY", "ALSOSECRET"} {
		if contains(out, secret) {
			t.Errorf("SafeConfig output leaked %q:\n%s", secret, out)
		}
	}
	if !contains(out, "lm-01") {
		t.Errorf("SafeConfig dropped the system name; output:\n%s", out)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
