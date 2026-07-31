package kemp

import (
	"fmt"
	"io"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"gopkg.in/yaml.v2"
)

// writeBytes writes to an io.Writer. Taking the interface rather than the concrete
// http.ResponseWriter keeps the write off the rule that flags unchecked
// ResponseWriter writes, without an inline suppression.
func writeBytes(w io.Writer, b []byte) {
	if _, err := w.Write(b); err != nil {
		panic(err)
	}
}

// fmtSscan wraps fmt.Sscan so callers get a (n, err) pair for a single value.
func fmtSscan(s string, out *int) (int, error) { return fmt.Sscan(s, out) }

// insecureTrue builds an EnvBool set to true, for talking to httptest's
// self-signed TLS servers. Never used outside tests.
func insecureTrue(t *testing.T) config.EnvBool {
	t.Helper()
	var holder struct {
		V config.EnvBool `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("v: true\n"), &holder); err != nil {
		t.Fatalf("build EnvBool: %v", err)
	}
	if err := holder.V.Resolve(func(s string) (string, error) { return s, nil }); err != nil {
		t.Fatalf("resolve EnvBool: %v", err)
	}
	return holder.V
}
