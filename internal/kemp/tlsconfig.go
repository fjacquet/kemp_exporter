package kemp

import (
	"crypto/tls"

	"github.com/fjacquet/kemp_exporter/internal/config"
)

// tlsConfigFor builds the TLS settings for one LoadMaster.
//
// InsecureSkipVerify is operator-controlled, not a hardcoded default: it is a
// per-target config field that defaults to false, appears in the SafeConfig output an
// operator sees at startup, and is documented in config.yaml as a man-in-the-middle
// risk. Static scanners flag the field read here; the policy decision lives in the
// configuration, not in this function. This is the deliberate correction to
// giantswarm/kemp-client, which hardcoded InsecureSkipVerify: true with no opt-out.
//
// MinVersion is pinned to TLS 1.2, the family floor.
func tlsConfigFor(sys config.System) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: sys.InsecureSkipVerify.Value(),
	}
}
