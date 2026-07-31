// Package config loads and validates the exporter configuration.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// System is one LoadMaster to monitor.
//
// Two credential shapes are supported because the two wire transports authenticate
// differently: the XML path uses a static API key, the JSON path a username/password
// session login. A system may configure either or both; the transport picked at
// detection time decides which is used.
type System struct {
	Name               string  `yaml:"name"`
	Host               string  `yaml:"host"`
	Port               int     `yaml:"port"` // defaults to 443
	APIKey             string  `yaml:"apiKey"`
	APIKeyFile         string  `yaml:"apiKeyFile"`
	Username           string  `yaml:"username"`
	Password           string  `yaml:"password"`
	PasswordFile       string  `yaml:"passwordFile"`
	InsecureSkipVerify EnvBool `yaml:"insecureSkipVerify"`
}

// BaseURL returns the https://host:port root for the LoadMaster REST API.
func (s System) BaseURL() string {
	port := s.Port
	if port == 0 {
		port = 443
	}
	return fmt.Sprintf("https://%s:%d", s.Host, port)
}

// Server holds HTTP-server settings.
type Server struct {
	Host    string `yaml:"host"`
	Port    string `yaml:"port"`
	URI     string `yaml:"uri"`
	LogName string `yaml:"logName"` // "" -> stdout
}

// Collection holds loop timing and concurrency.
type Collection struct {
	Interval      time.Duration `yaml:"interval"`
	Timeout       time.Duration `yaml:"timeout"`
	MaxConcurrent int           `yaml:"maxConcurrent"`
}

// OTelConfig configures the OTLP push exporter.
type OTelConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Endpoint string        `yaml:"endpoint"`
	Insecure bool          `yaml:"insecure"`
	Interval time.Duration `yaml:"interval"`
}

// Config is the whole file.
type Config struct {
	Server     Server     `yaml:"server"`
	Collection Collection `yaml:"collection"`
	OTel       OTelConfig `yaml:"otel"`
	Systems    []System   `yaml:"systems"`
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// interpolate replaces every ${VAR} in s with its environment value, returning an
// error if any referenced variable is unset. Failing fast turns a typo'd secret
// name into a config-load error instead of repeated runtime auth failures.
func interpolate(s string) (string, error) {
	var missing []string
	out := envRef.ReplaceAllStringFunc(s, func(m string) string {
		name := envRef.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unset environment variable(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// readSecretFile returns the trimmed contents of path.
func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Load reads, interpolates ${ENV} references, applies defaults, and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	for i := range cfg.Systems {
		s := &cfg.Systems[i]
		// Interpolate name first so later errors can quote the resolved name.
		for _, f := range []struct {
			label string
			ptr   *string
		}{
			{"name", &s.Name},
			{"host", &s.Host},
			{"apiKey", &s.APIKey},
			{"username", &s.Username},
			{"password", &s.Password},
		} {
			v, err := interpolate(*f.ptr)
			if err != nil {
				return nil, fmt.Errorf("system %s %s: %w", s.Name, f.label, err)
			}
			*f.ptr = v
		}
		if s.APIKeyFile != "" && s.APIKey == "" {
			v, err := readSecretFile(s.APIKeyFile)
			if err != nil {
				return nil, fmt.Errorf("system %s apiKeyFile: %w", s.Name, err)
			}
			s.APIKey = v
		}
		if s.PasswordFile != "" && s.Password == "" {
			v, err := readSecretFile(s.PasswordFile)
			if err != nil {
				return nil, fmt.Errorf("system %s passwordFile: %w", s.Name, err)
			}
			s.Password = v
		}
		if err := s.InsecureSkipVerify.Resolve(interpolate); err != nil {
			return nil, fmt.Errorf("system %s insecureSkipVerify: %w", s.Name, err)
		}
		if s.Host == "" {
			return nil, fmt.Errorf("system %s: host is required", s.Name)
		}
		if s.APIKey == "" && (s.Username == "" || s.Password == "") {
			return nil, fmt.Errorf("system %s: needs apiKey (XML path) or username+password (JSON path)", s.Name)
		}
	}

	if cfg.Server.Port == "" {
		cfg.Server.Port = "9447"
	}
	if cfg.Server.URI == "" {
		cfg.Server.URI = "/metrics"
	}
	// http.ServeMux itself would catch both of these -- but only as a panic at
	// server-start time, deep inside net/http's pattern parser, with no mention
	// of which config field caused it. Every other misconfiguration in this file
	// fails loudly here instead, naming the field; server.uri gets the same
	// treatment.
	if !strings.HasPrefix(cfg.Server.URI, "/") {
		return nil, fmt.Errorf("server.uri: must start with '/', got %q", cfg.Server.URI)
	}
	if cfg.Server.URI == "/" {
		return nil, fmt.Errorf(`server.uri: must not be exactly "/" (it would collide with the landing page)`)
	}
	// A negative value here would otherwise reach time.NewTicker (Collection.Interval,
	// in the collection loop's Run) or bypass the ==0 defaulting below entirely
	// (Collection.Timeout, Collection.MaxConcurrent, OTel.Interval) and flow through
	// as-is. time.NewTicker panics on a non-positive duration, which would take the
	// whole process down from a background goroutine, potentially a full cycle
	// after startup rather than failing loudly here with a message naming the field.
	if cfg.Collection.Interval < 0 {
		return nil, fmt.Errorf("collection.interval: must not be negative, got %s", cfg.Collection.Interval)
	}
	if cfg.Collection.Interval == 0 {
		cfg.Collection.Interval = 60 * time.Second
	}
	if cfg.Collection.Timeout < 0 {
		return nil, fmt.Errorf("collection.timeout: must not be negative, got %s", cfg.Collection.Timeout)
	}
	if cfg.Collection.Timeout == 0 {
		cfg.Collection.Timeout = 60 * time.Second
	}
	// Rejected outright rather than treated as errgroup's own "negative means
	// unlimited" convention: that convention is not exposed as a config knob, so an
	// operator writing maxConcurrent: -1 gets a clear load-time error instead of
	// silently full serial collection (see the collection loop's own clamp, which
	// exists only for a directly-constructed config.Collection{} that skipped this
	// validation).
	if cfg.Collection.MaxConcurrent < 0 {
		return nil, fmt.Errorf("collection.maxConcurrent: must not be negative, got %d", cfg.Collection.MaxConcurrent)
	}
	if cfg.Collection.MaxConcurrent == 0 {
		cfg.Collection.MaxConcurrent = 4
	}
	if cfg.OTel.Endpoint == "" {
		cfg.OTel.Endpoint = "localhost:4317"
	}
	if cfg.OTel.Interval < 0 {
		return nil, fmt.Errorf("otel.interval: must not be negative, got %s", cfg.OTel.Interval)
	}
	if cfg.OTel.Interval == 0 {
		cfg.OTel.Interval = 10 * time.Second
	}
	if len(cfg.Systems) == 0 {
		return nil, fmt.Errorf("no systems configured")
	}
	return &cfg, nil
}
