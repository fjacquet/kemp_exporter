package kemp

import (
	"testing"
	"time"
)

// TestObtainViaRecoversFromPanickingAttempt guards obtainVia's panic-safety.
// Without the defer wrapping the publish/clear pair (see the doc comment on
// obtainVia), a panicking attempt would leave s.pending set forever and never
// close p.done -- so this test also stands in for "the leader's own call
// still observes the panic": obtainVia must re-raise it after cleaning up,
// not swallow it silently.
func TestObtainViaRecoversFromPanickingAttempt(t *testing.T) {
	s := &session{}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("obtainVia did not re-raise the attempt's panic to its own caller")
			}
		}()
		_, _, _ = obtainVia(s, noGen, func() (string, error) {
			panic("simulated login panic")
		})
		t.Fatal("obtainVia returned normally after a panicking attempt; want it to panic")
	}()

	// s.pending must have been cleared by the deferred cleanup: a fresh call
	// (not concurrent with the panic above, which has already fully unwound)
	// must be free to attempt its own login rather than wedging on a stale
	// pending that nothing will ever clear.
	done := make(chan struct{})
	go func() {
		tok, _, err := obtainVia(s, noGen, func() (string, error) {
			return "recovered-token", nil
		})
		if err != nil {
			t.Errorf("obtainVia after a prior panicking attempt: %v", err)
		}
		if tok != "recovered-token" {
			t.Errorf("token = %q, want recovered-token", tok)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("obtainVia hung after a prior panicking attempt; s.pending was not cleared")
	}
}

// TestObtainViaUnblocksWaitersWhenAttemptPanics is the concurrency half of the
// panic-safety guard: a goroutine that joined an in-flight attempt (case 2 in
// obtainVia) must be released with a shared error when that attempt panics,
// not left blocked on <-p.done forever.
func TestObtainViaUnblocksWaitersWhenAttemptPanics(t *testing.T) {
	s := &session{}
	release := make(chan struct{})
	leaderStarted := make(chan struct{})

	// The leader becomes the single flight and blocks until released, then
	// panics. Its own panic is expected and recovered here so it doesn't fail
	// the test binary; TestObtainViaRecoversFromPanickingAttempt already
	// covers the leader-side re-raise behavior in isolation.
	leaderDone := make(chan struct{})
	go func() {
		defer func() {
			_ = recover()
			close(leaderDone)
		}()
		_, _, _ = obtainVia(s, noGen, func() (string, error) {
			close(leaderStarted)
			<-release
			panic("simulated login panic")
		})
	}()
	<-leaderStarted

	// The waiter must join the leader's in-flight attempt (case 2), not start
	// its own (which would fail this assertion).
	waiterDone := make(chan struct{})
	var waiterErr error
	go func() {
		_, _, err := obtainVia(s, noGen, func() (string, error) {
			t.Error("waiter started its own attempt instead of joining the leader's in-flight one")
			return "", nil
		})
		waiterErr = err
		close(waiterDone)
	}()

	// Give the waiter goroutine a chance to reach the pending-wait branch
	// before releasing the leader; if it hasn't, the failure mode is the
	// t.Error above (started its own attempt), not a flaky pass.
	time.Sleep(20 * time.Millisecond)
	close(release)
	<-leaderDone

	select {
	case <-waiterDone:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never unblocked after the leader's attempt panicked; s.pending/p.done were not published on the panic path")
	}
	if waiterErr == nil {
		t.Error("waiter got a nil error after the leader's attempt panicked; want a shared failure")
	}
}
