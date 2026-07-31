package kemp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/models"
	"github.com/sirupsen/logrus"
)

// Client is the per-LoadMaster API abstraction, satisfied by SystemClient and by
// test doubles. Nothing above this interface knows which wire encoding is in use.
type Client interface {
	// Name returns the configured system name, used as the `system` label.
	Name() string
	// GetStatistics fetches the stats payload.
	GetStatistics(ctx context.Context) (*models.Statistics, error)
	// ListVirtualServices fetches virtual-service metadata, which supplies the
	// service names the stats payload omits.
	ListVirtualServices(ctx context.Context) ([]models.VirtualServiceInfo, error)
	// TransportName reports the detected wire encoding ("xml", "json", or "" before
	// the first call).
	TransportName() string
}

// authFailureCooldown bounds how often a confirmed credential rejection (errAuth) is
// allowed to trigger a fresh login/probe against the appliance.
//
// Without this, a wrong JSON username/password -- or a runtime credential change on
// the appliance side after detection already succeeded -- produces one failed login
// attempt per scrape cycle, forever: auth.go's session.ensure deliberately re-attempts
// login on every call when the previous one failed (that behavior belongs at the
// session layer; Task 6's review routed the caching decision to this client instead).
// LoadMaster account lockout is commonly enabled with a 3-5 failed-attempt threshold;
// at a typical 15-60s scrape interval, that threshold is reached in one to a few
// minutes with no cooldown at all.
//
// This is deliberately scoped to errAuth alone -- a genuine, appliance-confirmed
// credential rejection -- never to errUnsupported or a bare transport-level failure.
// Neither of those represents a rejected login attempt in the sense LoadMaster
// lockout counts, and both may be transient (a network blip) or fixable without an
// exporter restart (a corrected command name once firmware is confirmed), so
// throttling them would only delay recovery without preventing any known harm.
//
// This also applies uniformly across both transports, not just JSON's login: a
// rejected static XML API key is throttled the same way. There is no confirmed
// evidence that repeated XML requests carry a comparable lockout risk, but there is
// also no confirmed evidence they don't, and a single consistent rule ("a confirmed
// credential rejection is retried at most once per cooldown window") is easier to
// reason about and log than two different policies per transport. The cost is a
// bounded worst-case delay (one cooldown window) before the exporter notices an
// operator has corrected credentials without restarting the process.
const authFailureCooldown = 60 * time.Second

// SystemClient talks to one LoadMaster over whichever transport it supports.
type SystemClient struct {
	name string

	// credFingerprint identifies the endpoint-and-credential set this client was
	// built from, so a reload can tell "same target, same credentials, rebuilt
	// object" from "the operator changed something". It is a hash, never the
	// material itself, and is never logged or rendered: see credentialFingerprint.
	credFingerprint [sha256.Size]byte

	mu       sync.Mutex
	active   transport
	xml      *xmlTransport
	json     *jsonTransport
	reprobed bool

	// authFailureAt/authFailureErr implement the sticky cooldown documented on
	// authFailureCooldown above. authFailureAt is the zero time.Time when no
	// failure is currently cached (the default, and the state after any success).
	authFailureAt  time.Time
	authFailureErr error

	// now is time.Now by default. Tests override it directly (same package) so the
	// cooldown can be exercised deterministically without a real sleep.
	now func() time.Time
}

// credentialFingerprint hashes everything that decides which credential is sent
// where, so two clients can be compared for "same target, same secrets" without
// either the comparison or any diagnostic ever holding the secrets themselves.
//
// Each field is length-prefixed before hashing so no pair of different field
// splits can produce the same input (a "ab"+"c" vs "a"+"bc" collision would make
// a credential change look like no change, which is the one error that matters
// here). The digest is never logged, exported, or included in an error: its only
// consumer is the equality test in carryAuthCooldown.
func credentialFingerprint(sys config.System) [sha256.Size]byte {
	h := sha256.New()
	for _, field := range []string{
		sys.Host,
		strconv.Itoa(sys.Port),
		sys.APIKey,
		sys.Username,
		sys.Password,
		strconv.FormatBool(sys.InsecureSkipVerify.Value()),
	} {
		_, _ = fmt.Fprintf(h, "%d:%s", len(field), field)
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// carryAuthCooldown moves the anti-lockout cooldown state from a superseded client
// set onto its replacements, for every system whose name AND credentials are
// unchanged.
//
// Without this a config reload defeated the cooldown entirely: buildClients
// constructs brand-new SystemClients, each with a zero authFailureAt, and
// config.Watcher.reload fires on any qualifying filesystem event with no content
// comparison -- so a content-identical rewrite by a config-management agent, or the
// shipped systemd unit's ExecReload=kill -HUP, bought the appliance one more login
// attempt with a credential already known to be rejected. Repeat that against a
// LoadMaster with lockout enabled and the account locks, which is the exact outcome
// authFailureCooldown exists to prevent.
//
// The cooldown is deliberately NOT carried when the credentials changed: an
// operator who has just corrected a password must not have to wait out a window
// recorded against the old one. It is also not carried across a rename, because
// the system name is how a target is identified everywhere else in this exporter.
func carryAuthCooldown(previous, next []Client) {
	if len(previous) == 0 || len(next) == 0 {
		return
	}
	prior := make(map[string]*SystemClient, len(previous))
	for _, c := range previous {
		if sc, ok := c.(*SystemClient); ok {
			prior[sc.name] = sc
		}
	}
	for _, c := range next {
		sc, ok := c.(*SystemClient)
		if !ok {
			continue
		}
		old, found := prior[sc.name]
		if !found || old == sc || old.credFingerprint != sc.credFingerprint {
			continue
		}
		old.mu.Lock()
		at, cause := old.authFailureAt, old.authFailureErr
		old.mu.Unlock()
		if at.IsZero() {
			continue
		}
		sc.mu.Lock()
		sc.authFailureAt, sc.authFailureErr = at, cause
		sc.mu.Unlock()
		logrus.WithFields(logrus.Fields{"system": sc.name, "cooldown": authFailureCooldown.String()}).
			Info("reload: carrying the credential-rejection cooldown forward; credentials unchanged")
	}
}

// NewSystemClient builds both transports; detection happens on first use.
func NewSystemClient(sys config.System, trace bool) (*SystemClient, error) {
	c := &SystemClient{name: sys.Name, now: time.Now, credFingerprint: credentialFingerprint(sys)}
	if sys.APIKey != "" {
		xt, err := newXMLTransport(sys, trace)
		if err != nil {
			return nil, err
		}
		c.xml = xt
	}
	if sys.Username != "" && sys.Password != "" {
		jt, err := newJSONTransport(sys, trace)
		if err != nil {
			return nil, err
		}
		c.json = jt
	}
	if c.xml == nil && c.json == nil {
		return nil, fmt.Errorf("system %s: no usable credentials", sys.Name)
	}
	return c, nil
}

// Name returns the configured system name.
func (c *SystemClient) Name() string { return c.name }

// TransportName reports the detected encoding, or "" before the first call.
func (c *SystemClient) TransportName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return ""
	}
	return c.active.Name()
}

// do runs cmd on the detected transport, probing on first use and re-probing once
// after a hard failure.
//
// Detection treats a 4xx as the expected negative signal that a firmware lacks the
// JSON path; that is distinct from the runtime rule in newRestyClient, which never
// retries a 4xx on an already-detected transport. cooldownError/noteAuthResult add a
// second, orthogonal control: whether a *confirmed credential rejection* (errAuth) is
// allowed to trigger a brand new network attempt at all, independent of whether that
// attempt happens during detection or against an already-detected transport.
func (c *SystemClient) do(ctx context.Context, cmd string, params map[string]string, out any) error {
	if err := c.cooldownError(); err != nil {
		return err
	}

	tr, err := c.ensureTransport(ctx, cmd, params, out)
	if err != nil {
		c.noteAuthResult(err)
		return err
	}
	if tr == nil {
		c.noteAuthResult(nil)
		return nil // ensureTransport already satisfied the request during probing
	}

	err = tr.Do(ctx, cmd, params, out)
	if err == nil {
		c.noteAuthResult(nil)
		return nil
	}
	// An auth rejection is final: do not re-probe, do not retry.
	if errors.Is(err, errAuth) {
		c.noteAuthResult(err)
		return err
	}
	// reprobe handles its own cooldown bookkeeping (see its doc comment): the
	// cooldown must reflect only the transport that will actually serve the next
	// call, which reprobe -- not do -- is in a position to know.
	return c.reprobe(ctx, cmd, params, out, err)
}

// cooldownError reports a cached credential rejection as a fresh error, without
// making any network call, while still inside authFailureCooldown of when it was
// recorded. See authFailureCooldown's doc comment for why this exists.
func (c *SystemClient) cooldownError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authFailureAt.IsZero() {
		return nil
	}
	if c.now().Sub(c.authFailureAt) >= authFailureCooldown {
		return nil
	}
	return fmt.Errorf("system %s: %w (cached; suppressing further attempts until cooldown elapses)", c.name, c.authFailureErr)
}

// noteAuthResult records or clears the sticky auth-failure cooldown based on the
// outcome of a real attempt (never called for a cooldownError short-circuit itself).
// Only errAuth is sticky -- see authFailureCooldown's doc comment for why
// errUnsupported and transport-level failures are deliberately left alone.
func (c *SystemClient) noteAuthResult(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if errors.Is(err, errAuth) {
		if c.authFailureAt.IsZero() {
			// Logged only on the transition into cooldown, not on every cached hit
			// (those never reach this function), so this cannot spam once per
			// scrape cycle. err is always one of this package's own errAuth-wrapped
			// messages, which never carry a credential (verified across the XML and
			// JSON transports' own error-construction sites).
			logrus.WithFields(logrus.Fields{"system": c.name, "cooldown": authFailureCooldown.String()}).
				WithError(err).Warn("credential rejected; suppressing further attempts until cooldown elapses")
		}
		c.authFailureAt = c.now()
		c.authFailureErr = err
		return
	}
	if err == nil {
		c.authFailureAt = time.Time{}
		c.authFailureErr = nil
	}
}

// ensureTransport selects a transport, preferring JSON. It returns a nil transport
// when the probe itself already produced the caller's result.
//
// c.mu is held across the whole probe, network round trip included, rather than
// released after the active == nil check. That check-then-act shape was not a rare
// interleaving: collectSystem fans GetStatistics and ListVirtualServices out
// CONCURRENTLY through one client, so on the first cycle BOTH callers always found
// active == nil and both probed. Two probes can reach opposite conclusions -- JSON
// `stats` succeeding while the (unconfirmed) JSON `listvs` command name 404s is
// enough -- and the last writer then decided the transport, the credential, and the
// wire path for the entire process lifetime, differently across restarts.
//
// The cost of serialising is a bounded wait for the peers, once per client per
// process, and it is already bounded twice over: by the probe's own context and by
// the 30s client timeout. Detection happens once; nothing here is on the steady-state
// path, which takes the mutex only for the active != nil read below.
func (c *SystemClient) ensureTransport(ctx context.Context, cmd string, params map[string]string, out any) (transport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return c.active, nil
	}

	// probeErr carries the JSON probe's classified failure (errUnsupported or
	// errAuth) through to the final error if no fallback transport is configured, so
	// the caller can see *why* detection failed instead of a generic message.
	var probeErr error
	if c.json != nil {
		err := c.json.Do(ctx, cmd, params, out)
		if err == nil {
			c.active = c.json
			logrus.WithFields(logrus.Fields{"system": c.name, "transport": "json"}).
				Info("detected LoadMaster API transport")
			return nil, nil
		}
		if !errors.Is(err, errUnsupported) && !errors.Is(err, errAuth) {
			// A transport-level failure is not evidence about firmware support.
			return nil, err
		}
		probeErr = err
	}
	if c.xml != nil {
		c.active = c.xml
		logrus.WithFields(logrus.Fields{"system": c.name, "transport": "xml"}).
			Info("detected LoadMaster API transport")
		return c.xml, nil
	}

	if probeErr != nil {
		return nil, fmt.Errorf("system %s: no transport accepted by appliance: %w", c.name, probeErr)
	}
	return nil, fmt.Errorf("system %s: no transport accepted by appliance", c.name)
}

// reprobe switches to the other transport once after a hard failure, then gives up.
//
// It owns its own cooldown bookkeeping rather than leaving it to the caller,
// because only reprobe knows, at the point an attempt completes, which transport
// will actually serve the *next* call: cause (the active transport's own failure)
// when the alternate also fails and active is left unchanged, or nothing at all
// (the alternate just proved itself healthy) when the alternate succeeds and
// active swaps to it. The alternate's own error is never fed to noteAuthResult: a
// transport that is not, and does not become, active has no bearing on whether the
// transport actually in use should be throttled. See the caller-facing error
// below for why the alternate's failure is still surfaced, just not to the
// cooldown.
func (c *SystemClient) reprobe(ctx context.Context, cmd string, params map[string]string, out any, cause error) error {
	c.mu.Lock()
	if c.reprobed {
		c.mu.Unlock()
		return cause
	}
	c.reprobed = true
	var alt transport
	if c.active == c.json && c.xml != nil {
		alt = c.xml
	} else if c.active == c.xml && c.json != nil {
		alt = c.json
	}
	c.mu.Unlock()

	if alt == nil {
		return cause
	}
	logrus.WithFields(logrus.Fields{"system": c.name, "transport": alt.Name()}).
		WithError(cause).Warn("transport failed; re-probing the alternate path once")
	if err := alt.Do(ctx, cmd, params, out); err != nil {
		// The cooldown is deliberately left untouched here. There used to be a
		// c.noteAuthResult(cause) call on this line, described as "can only ever
		// clear a stale cooldown" -- it could not: cause is non-nil (this is the
		// failure path) and non-errAuth (do() intercepts an errAuth active-transport
		// failure before ever calling reprobe), and noteAuthResult acts on exactly
		// those two cases and no other. It was a no-op with a comment that
		// misdescribed a lockout-prevention mechanism, resting on an invariant no
		// compiler or test enforced. What matters is the rule itself, which the
		// absence of a call states as plainly as the call did: neither the active
		// transport's non-auth failure nor the ALTERNATE's failure -- whatever its
		// kind -- may prime or clear the cooldown, because an errAuth belonging to a
		// transport that is not, and does not become, active must never suppress
		// calls to the transport actually in use.
		//
		// The RETURNED error, by contrast, preserves both failures: discarding the
		// alternate's error here (as an earlier version of this function did) would
		// lose the operator's diagnosis of which transport/credential is actually
		// broken -- the log would say only "XML returned 500" with no trace of "and
		// your JSON password is also wrong."
		return fmt.Errorf("%w (alternate %s also failed: %w)", cause, alt.Name(), err)
	}
	c.mu.Lock()
	c.active = alt
	c.mu.Unlock()
	c.noteAuthResult(nil)
	return nil
}

// GetStatistics fetches the stats payload.
func (c *SystemClient) GetStatistics(ctx context.Context) (*models.Statistics, error) {
	var st models.Statistics
	if err := c.do(ctx, "stats", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// listVSPayload wraps the listvs response, whose Data holds repeated VS elements.
type listVSPayload struct {
	VS []models.VirtualServiceInfo `xml:"VS" json:"VS"`
}

// ListVirtualServices fetches virtual-service metadata for the name join.
func (c *SystemClient) ListVirtualServices(ctx context.Context) ([]models.VirtualServiceInfo, error) {
	var out listVSPayload
	if err := c.do(ctx, "listvs", nil, &out); err != nil {
		return nil, err
	}
	return out.VS, nil
}

var _ Client = (*SystemClient)(nil)
