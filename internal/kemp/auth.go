package kemp

import (
	"bytes"
	"context"
	"encoding/json"
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
// concurrently for the same target, so every access is guarded by mu. Beyond the
// data race that guarding prevents, gen and pending below exist to collapse
// concurrent login attempts: without them, N goroutines that all observe the same
// stale (or absent) token at once would each perform their own login, stampeding
// the appliance -- and with account lockout enabled, a wrong password turns one
// concurrent scrape round into a locked account. See
// TestJSONTransportRefreshCollapsesConcurrentStampede and
// TestJSONTransportEnsureCollapsesConcurrentStampedeOnBadCredentials.
//
// gen (not the token string) is what staleness is measured against. The token
// itself is not a reliable staleness signal: a real LoadMaster is not guaranteed
// to issue a syntactically different string on every login, so comparing
// "did the string change" could misjudge a fresh login as having produced
// nothing new. gen is bumped by obtain on every completed attempt, success or
// failure, and is exactly what "stale" means: "the generation this caller last
// observed."
type session struct {
	mu      sync.Mutex
	token   string
	gen     int
	pending *loginAttempt
}

// loginAttempt is a single login round-trip shared by every caller waiting on
// it. done is closed once token/gen/err are set, so a waiter's `<-p.done`
// never races the write.
type loginAttempt struct {
	done  chan struct{}
	token string
	gen   int
	err   error
}

// noGen is the sentinel staleness value ensure passes: "I have never obtained
// a generation from this session." gen's zero value (0) is a real, reachable
// generation (the state before any login has ever completed), so it cannot
// double as this sentinel -- noGen must be a value gen can never actually hold.
const noGen = -1

// loginResponse is the token-bearing login payload. Responses from this endpoint are
// never logged by the trace hook: the request path "/access/login" contains the
// fragment "login", which installTracing's isAuthPath check matches, so the body
// (and therefore the token) is suppressed. See TestJSONLoginResponseTokenNotLogged.
type loginResponse struct {
	Success struct {
		Data struct {
			Token string `json:"token"`
		} `json:"Data"`
	} `json:"Success"`
}

// ensure returns a valid token and the generation it belongs to, logging in if
// none is cached. Concurrent first calls (no token yet) collapse onto a single
// login attempt.
func (s *session) ensure(ctx context.Context, c *resty.Client, user, pass string) (string, int, error) {
	return s.obtain(ctx, c, user, pass, noGen)
}

// refresh returns a token newer than staleGen -- the generation the caller
// just tried and had rejected -- logging in again only if no one else already
// has. Called at most once per Do: the caller (jsonTransport.Do) invokes this
// from a single, non-looping code path, so the per-Do bound is structural, not
// a comment. Across concurrent Do calls that all observe the same staleGen,
// this collapses onto a single shared login: see the type doc on session.
func (s *session) refresh(ctx context.Context, c *resty.Client, user, pass string, staleGen int) (string, int, error) {
	return s.obtain(ctx, c, user, pass, staleGen)
}

// obtain returns a token from a generation newer than staleGen, performing at
// most one login per group of callers that observe the same staleGen
// concurrently. It delegates to obtainVia with s.login as the attempt, which
// exists as a separate, parameterized function so its panic-safety (see the
// doc comment there) can be exercised directly in tests without a real login
// round trip.
func (s *session) obtain(ctx context.Context, c *resty.Client, user, pass string, staleGen int) (string, int, error) {
	return obtainVia(s, staleGen, func() (string, error) {
		return s.login(ctx, c, user, pass)
	})
}

// obtainVia implements session's singleflight collapse: at most one call to
// attempt runs per group of callers that observe the same staleGen
// concurrently.
//
// Three cases, in order:
//  1. s.gen already differs from staleGen and a token is cached: someone
//     already completed a login past what this caller has seen. Use it; no
//     network call.
//  2. A login is already in flight (s.pending != nil): wait for it and share
//     its result -- success or failure alike -- rather than starting a second
//     one. This is what collapses N concurrent callers into one login.
//  3. Otherwise this caller is the first to notice the staleness: become the
//     single flight, run attempt, and hand the result to anyone who joined via
//     case 2 while it was in flight.
//
// The lock is held only for the brief state checks/transitions around the
// call to attempt, not across the call itself, so unrelated session
// operations are never blocked on a slow login.
//
// This bounds collapsing to callers that are concurrent with an in-flight
// attempt. A caller that arrives after a failed attempt has already finished
// (pending cleared, gen bumped, token still absent) starts a fresh attempt of
// its own rather than being blocked forever by the earlier failure -- there is
// deliberately no permanent negative caching here; see the non-blocking note
// in the Task 6 review about where that policy belongs instead.
//
// Publishing the result and clearing s.pending happen in a defer, so both
// still run if attempt panics: without this, a panicking login would leave
// s.pending set forever (every subsequent caller with no cached token wedges
// on the pending branch above) and every current waiter blocked on <-p.done
// forever, with no timeout -- permanent goroutine accumulation. The panic
// itself is re-raised after the defer runs, so the leader's own goroutine
// still observes and propagates it exactly as it would have before this
// singleflight existed; only peers that were waiting to share the leader's
// result are protected from hanging.
func obtainVia(s *session, staleGen int, attempt func() (string, error)) (string, int, error) {
	s.mu.Lock()
	if s.gen != staleGen && s.token != "" {
		tok, gen := s.token, s.gen
		s.mu.Unlock()
		return tok, gen, nil
	}
	if s.pending != nil {
		p := s.pending
		s.mu.Unlock()
		<-p.done
		return p.token, p.gen, p.err
	}
	p := &loginAttempt{done: make(chan struct{})}
	s.pending = p
	s.mu.Unlock()

	var tok string
	var err error
	func() {
		defer func() {
			r := recover()
			if r != nil {
				err = fmt.Errorf("login: panic: %v", r)
			}

			s.mu.Lock()
			s.gen++
			if err == nil {
				s.token = tok
			}
			newGen := s.gen
			s.pending = nil
			s.mu.Unlock()

			p.token, p.gen, p.err = tok, newGen, err
			close(p.done)

			if r != nil {
				panic(r)
			}
		}()
		tok, err = attempt()
	}()

	return p.token, p.gen, p.err
}

// login performs one login round-trip. It touches no session state itself;
// obtain applies the result under s.mu.
//
// The response body is decoded with an explicit json.Unmarshal, the same way
// jsonTransport.Do decodes command responses, rather than via resty's
// SetResult. SetResult only auto-unmarshals when the *response* Content-Type
// header sniffs as JSON or XML (resty v2.17.2 middleware.go:408-416); a
// LoadMaster answering a perfectly valid token body with Content-Type
// "text/plain", "application/octet-stream", or no header at all would
// otherwise leave out.Success.Data.Token silently empty, misreporting a
// JSON-capable appliance as errUnsupported. Decoding explicitly makes this
// path immune to the response's declared content type, matching the command
// path's existing behavior.
func (s *session) login(ctx context.Context, c *resty.Client, user, pass string) (string, error) {
	resp, err := c.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]string{"user": user, "password": pass}).
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
	var out loginResponse
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		// A decode failure is only a "this firmware doesn't speak JSON" signal
		// when the body isn't even trying to be JSON (XML, HTML, plain text --
		// the same /access/<cmd> namespace the XML transport uses, so a
		// JSON-less firmware or a captive portal answering HTTP 200 here is a
		// real shape, not hypothetical). A body that starts like JSON but
		// fails to parse is a truncated or corrupt response from an appliance
		// that DOES speak JSON, and must stay a hard error: classifying it as
		// errUnsupported would let Task 7 silently downgrade to XML and mask
		// a genuine fault.
		if !looksLikeJSON(resp.Body()) {
			return "", fmt.Errorf("login: %w (response is not JSON)", errUnsupported)
		}
		return "", fmt.Errorf("login: decode response: %w", err)
	}
	if out.Success.Data.Token == "" {
		return "", fmt.Errorf("login: %w (no token in response)", errUnsupported)
	}
	return out.Success.Data.Token, nil
}

// looksLikeJSON reports whether body appears to be JSON, ignoring leading
// whitespace. It does not validate the body -- json.Unmarshal already does
// that -- it only distinguishes "this is JSON that failed to parse" (a real
// fault) from "this isn't JSON at all" (the appliance doesn't speak JSON).
func looksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}
