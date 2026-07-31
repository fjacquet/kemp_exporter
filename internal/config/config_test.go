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
	if cfg.Server.Port != "9447" {
		t.Errorf("Server.Port = %q, want \"9447\"", cfg.Server.Port)
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
