package kemp

import (
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
)

// installTracing logs each API response's method, path, status and BODY.
//
// Never use resty.SetDebug for this: it dumps request headers, which carry the API
// key and any session token straight into the log. Responses from authentication
// endpoints are skipped entirely, because the JSON login returns its token in the
// response body — body-only logging is not sufficient there.
func installTracing(c *resty.Client) {
	c.OnAfterResponse(func(_ *resty.Client, r *resty.Response) error {
		path := redactQuery(r.Request.URL)
		if isAuthPath(path) {
			logrus.WithFields(logrus.Fields{
				"method": r.Request.Method,
				"path":   path,
				"status": r.StatusCode(),
			}).Debug("api response (body suppressed: authentication endpoint)")
			return nil
		}
		logrus.WithFields(logrus.Fields{
			"method": r.Request.Method,
			"path":   path,
			"status": r.StatusCode(),
			"body":   string(r.Body()),
		}).Debug("api response")
		return nil
	})
}

// redactQuery drops the query string from a request URL before it is logged.
//
// resty's Request.URL is the fully resolved URL, query parameters included: the
// XML transport's apikey travels there, so logging it verbatim would put the
// credential in every trace line regardless of the isAuthPath check below (which
// only gates whether the body is also logged). An unparseable URL is reported as
// "" rather than logged raw, since a raw string that failed to parse as a URL
// could itself be exactly the credential-bearing value this is guarding against.
func redactQuery(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	return u.String()
}

// isAuthPath reports whether a response from this path may carry credentials.
func isAuthPath(path string) bool {
	p := strings.ToLower(path)
	for _, frag := range []string{"login", "logon", "session", "token"} {
		if strings.Contains(p, frag) {
			return true
		}
	}
	return false
}

// discardLogger implements resty.Logger as a no-op. It replaces resty's built-in
// default logger, which writes directly to os.Stderr independent of installTracing
// and the trace flag; see the comment on SetLogger in newRestyClient for why that
// default is a credential leak here.
type discardLogger struct{}

func (discardLogger) Errorf(_ string, _ ...any) {}
func (discardLogger) Warnf(_ string, _ ...any)  {}
func (discardLogger) Debugf(_ string, _ ...any) {}
