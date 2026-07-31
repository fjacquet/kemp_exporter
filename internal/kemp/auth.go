package kemp

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-resty/resty/v2"
)

// session holds a LoadMaster JSON-API session token.
//
// The XML path has no equivalent: it authenticates with a static, long-lived API
// key that is never refreshed. That asymmetry is deliberate and recorded in ADR 0004.
//
// token is shared mutable state: Do (and therefore ensure/refresh) may be called
// concurrently for the same target, so every access is guarded by mu.
type session struct {
	mu    sync.Mutex
	token string
}

// loginResponse is the token-bearing login payload. Responses from this endpoint are
// never logged by the trace hook: the request path "/access/login" contains the
// fragment "login", which installTracing's isAuthPath check matches, so the body
// (and therefore the token) is suppressed. See TestJSONLoginPathIsTreatedAsAuth.
type loginResponse struct {
	Success struct {
		Data struct {
			Token string `json:"token"`
		} `json:"Data"`
	} `json:"Success"`
}

// ensure returns a valid token, logging in if none is cached.
func (s *session) ensure(ctx context.Context, c *resty.Client, user, pass string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" {
		return s.token, nil
	}
	return s.loginLocked(ctx, c, user, pass)
}

// refresh discards the cached token and logs in again. Called at most once per Do:
// the caller (jsonTransport.Do) invokes this from a single, non-looping code path,
// so the bound is structural rather than a comment.
func (s *session) refresh(ctx context.Context, c *resty.Client, user, pass string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	return s.loginLocked(ctx, c, user, pass)
}

// loginLocked performs the login. The caller must hold s.mu.
func (s *session) loginLocked(ctx context.Context, c *resty.Client, user, pass string) (string, error) {
	var out loginResponse
	resp, err := c.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]string{"user": user, "password": pass}).
		SetResult(&out).
		Post("/access/login")
	if err != nil {
		return "", fmt.Errorf("login: %w", sanitizeTransportError(err))
	}
	if resp.StatusCode() == http.StatusNotFound {
		return "", fmt.Errorf("login: %w (status 404)", errUnsupported)
	}
	if resp.StatusCode() == http.StatusUnauthorized || resp.StatusCode() == http.StatusForbidden {
		return "", fmt.Errorf("login: %w (status %d)", errAuth, resp.StatusCode())
	}
	if resp.StatusCode() >= 400 {
		return "", fmt.Errorf("login: status %d", resp.StatusCode())
	}
	if out.Success.Data.Token == "" {
		return "", fmt.Errorf("login: %w (no token in response)", errUnsupported)
	}
	s.token = out.Success.Data.Token
	return s.token, nil
}
