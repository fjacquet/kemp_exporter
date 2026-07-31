// Package kemp implements the LoadMaster API client, the collection loop, and the
// Prometheus and OTLP export paths.
package kemp

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/go-resty/resty/v2"
)

// transport is one wire encoding of the LoadMaster API. Both implementations decode
// into the same models types, so nothing above this interface branches on encoding.
type transport interface {
	// Name reports the wire encoding: "xml" or "json".
	Name() string
	// Do issues an API command and decodes the response payload into out.
	Do(ctx context.Context, cmd string, params map[string]string, out any) error
}

// errAuth marks a credential rejection (4xx). It is never retried: retrying a 401
// against a LoadMaster with account lockout enabled locks the account.
var errAuth = errors.New("authentication rejected")

// errUnsupported marks a transport the appliance does not speak. Detection treats it
// as the signal to fall back; it is not a runtime failure.
var errUnsupported = errors.New("transport not supported by this appliance")

// newRestyClient builds the shared HTTP client for either transport.
//
// Retry deliberately excludes 4xx: a rejected credential or an unsupported command
// will fail identically on every attempt, and retrying costs an account lockout.
//
// SetLogger replaces resty's built-in default logger (created unconditionally by
// resty.New) with a no-op. That default logger writes straight to os.Stderr, not
// through logrus, and is not gated on the trace flag: on any transport-level
// failure (TLS handshake error, DNS failure, connection refused) it logs the full
// request URL, query parameters included. Both transports carry credentials in
// the URL or query string (the XML apikey; a future JSON session call could too),
// so left enabled it is an unconditional credential leak to stderr. installTracing
// above is this package's sole controlled, redacted logging path; resty's own is
// silenced rather than duplicated.
func newRestyClient(sys config.System, trace bool) (*resty.Client, error) {
	c := resty.New().
		SetLogger(discardLogger{}).
		SetBaseURL(sys.BaseURL()).
		SetTimeout(30 * time.Second).
		SetTLSClientConfig(tlsConfigFor(sys)).
		SetRetryCount(3).
		SetRetryWaitTime(500 * time.Millisecond).
		SetRetryMaxWaitTime(5 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			if r == nil {
				return err != nil // transport error: retry
			}
			return r.StatusCode() >= 500
		})
	if trace {
		installTracing(c)
	}
	return c, nil
}

// sanitizeTransportError strips the request URL from a client-level transport
// failure before it enters this package's own error chain.
//
// Go's net/http wraps client-level failures (TLS handshake errors, DNS failures,
// connection refused) in a *url.Error, whose Error() string is "<Op> \"<URL>\":
// <reason>" — the full URL, apikey query parameter included. Do's caller wraps
// whatever this returns with %w, so without this step the credential would
// propagate into any log line a caller writes for that error. HTTP-level
// failures (4xx/5xx responses) never reach this path: Do builds its own message
// for those from the status code alone.
func sanitizeTransportError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}
