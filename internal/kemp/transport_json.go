package kemp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/go-resty/resty/v2"
)

// jsonTransport speaks the LoadMaster JSON API (firmware 7.2.50+), which requires
// session management enabled and Basic authentication disabled on the appliance.
//
// user and pass are read-only after construction; sess is the only shared mutable
// state and guards itself (see session in auth.go), so jsonTransport is safe for
// concurrent use by multiple goroutines calling Do for the same target.
type jsonTransport struct {
	client *resty.Client
	user   string
	pass   string
	sess   session
}

// newJSONTransport builds the JSON wire path for one system.
func newJSONTransport(sys config.System, trace bool) (*jsonTransport, error) {
	c, err := newRestyClient(sys, trace)
	if err != nil {
		return nil, err
	}
	return &jsonTransport{client: c, user: sys.Username, pass: sys.Password}, nil
}

// Name reports the wire encoding.
func (t *jsonTransport) Name() string { return "json" }

// jsonEnvelope is the response wrapper. Data is decoded into the caller's type via
// json.RawMessage so one envelope definition serves every command.
type jsonEnvelope struct {
	Status  string `json:"status"`
	Code    int    `json:"code"`
	Error   string `json:"Error"`
	Success struct {
		Data json.RawMessage `json:"Data"`
	} `json:"Success"`
}

// Do issues cmd and decodes Success.Data into out, refreshing the session at most
// once if the appliance rejects the cached token.
//
// The single re-login is a deliberate, bounded exception to the no-4xx-retry rule
// in newRestyClient: retrying a 401 against a LoadMaster with account lockout
// enabled locks the account. The bound is structural, not a counter or a comment —
// there is exactly one call to t.sess.refresh and exactly one retried t.post in
// this function body, both inline in a straight-line sequence with no loop.
func (t *jsonTransport) Do(ctx context.Context, cmd string, params map[string]string, out any) error {
	token, gen, err := t.sess.ensure(ctx, t.client, t.user, t.pass)
	if err != nil {
		return fmt.Errorf("json %s: %w", cmd, err)
	}

	body, status, err := t.post(ctx, cmd, params, token)
	if err != nil {
		return fmt.Errorf("json %s: %w", cmd, err)
	}
	if status == http.StatusUnauthorized {
		// Expired token: log in again and retry exactly once. Concurrent Do
		// calls that all observed the same gen collapse onto a single shared
		// login inside refresh; see session.obtain in auth.go.
		token, _, err = t.sess.refresh(ctx, t.client, t.user, t.pass, gen)
		if err != nil {
			return fmt.Errorf("json %s: %w", cmd, err)
		}
		body, status, err = t.post(ctx, cmd, params, token)
		if err != nil {
			return fmt.Errorf("json %s: %w", cmd, err)
		}
		if status == http.StatusUnauthorized {
			return fmt.Errorf("json %s: %w (after refresh)", cmd, errAuth)
		}
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("json %s: %w (status 404)", cmd, errUnsupported)
	}
	if status >= 400 {
		return fmt.Errorf("json %s: status %d", cmd, status)
	}

	var env jsonEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("json %s: decode envelope: %w", cmd, err)
	}
	if err := checkJSONEnvelope(cmd, env); err != nil {
		return err
	}
	if len(env.Success.Data) == 0 {
		return fmt.Errorf("json %s: empty payload", cmd)
	}
	if err := json.Unmarshal(env.Success.Data, out); err != nil {
		return fmt.Errorf("json %s: decode payload: %w", cmd, err)
	}
	return nil
}

// checkJSONEnvelope maps a non-"ok" status to an actionable error, checked
// before the Data payload is ever decoded.
//
// The appliance can report a rejected credential, or any other appliance-level
// failure, purely through the status/code fields -- with no "Error" string at
// all, e.g. HTTP 200 with body `{"status":"fail","code":401,"Success":{"Data":
// {...}}}`. Left unchecked, that shape falls straight through to the Data
// decode and is reported as success: indistinguishable from a genuine 200,
// and not errors.Is(err, errAuth)-matchable by a caller that needs to know not
// to retry. This is the JSON counterpart of transport_xml.go's
// checkEnvelopeStat, which exists for the identical reason on the XML side.
func checkJSONEnvelope(cmd string, env jsonEnvelope) error {
	if env.Status == "" || env.Status == "ok" {
		return nil
	}
	if env.Code == http.StatusUnauthorized || env.Code == http.StatusForbidden {
		return fmt.Errorf("json %s: %w (code %d)", cmd, errAuth, env.Code)
	}
	if env.Error != "" {
		return fmt.Errorf("json %s: appliance error: %s", cmd, env.Error)
	}
	return fmt.Errorf("json %s: appliance returned status %q (code %d)", cmd, env.Status, env.Code)
}

// post issues one command request and returns the raw body and status.
func (t *jsonTransport) post(ctx context.Context, cmd string, params map[string]string, token string) ([]byte, int, error) {
	payload := map[string]string{}
	for k, v := range params {
		payload[k] = v
	}
	resp, err := t.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("X-API-Key", token).
		SetBody(payload).
		Post("/access/" + cmd)
	if err != nil {
		return nil, 0, sanitizeTransportError(err)
	}
	return resp.Body(), resp.StatusCode(), nil
}

// compile-time assertions that both transports satisfy the interface.
var (
	_ transport = (*jsonTransport)(nil)
	_ transport = (*xmlTransport)(nil)
)
