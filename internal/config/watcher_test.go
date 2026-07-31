package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestWatcherReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(host string) {
		body := "systems:\n  - name: lm-01\n    host: " + host + "\n    apiKey: k\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("10.0.0.1")

	var mu sync.Mutex
	var got []string
	w, err := NewWatcher(path, func(c *Config) {
		mu.Lock()
		got = append(got, c.Systems[0].Host)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	write("10.0.0.2")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no reload callback fired within 5s")
	}
	if got[len(got)-1] != "10.0.0.2" {
		t.Errorf("reloaded host = %q, want 10.0.0.2", got[len(got)-1])
	}
}

// A broken config must not fire the callback: the process keeps running on the
// last good configuration rather than losing its targets to a typo. The test also
// proves the watcher is genuinely alive both before and after the bad write, so a
// silently-dead watch mechanism (which would trivially satisfy "fired == 0") fails
// this test instead of passing it by accident.
func TestWatcherIgnoresInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	validBody := func(host string) string {
		return "systems:\n  - name: lm-01\n    host: " + host + "\n    apiKey: k\n"
	}
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write(validBody("10.0.0.1"))

	var mu sync.Mutex
	fired := 0
	var got []string
	w, err := NewWatcher(path, func(c *Config) {
		mu.Lock()
		fired++
		got = append(got, c.Systems[0].Host)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitForFired := func(n int, msg string) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			c := fired
			mu.Unlock()
			if c >= n {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(msg)
	}

	// Baseline: prove the watcher actually reloads on a valid edit before we poke it
	// with garbage. Without this, a watcher that never started at all would also
	// report "fired == 0" for the bad write below and pass vacuously.
	write(validBody("10.0.0.2"))
	waitForFired(1, "watcher never reloaded a valid config before the bad write; watch mechanism looks dead")

	if err := os.WriteFile(path, []byte("systems: [\n"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	firedAfterBad := fired
	mu.Unlock()
	if firedAfterBad != 1 {
		t.Errorf("callback fired %d times total after an invalid config write; want still 1 (unchanged by the bad write)", firedAfterBad)
	}

	// Prove the watcher is still alive after the bad write rather than wedged or
	// dead: a subsequent valid edit must still reload. This is what makes the
	// "unchanged by the bad write" assertion above meaningful instead of vacuous.
	write(validBody("10.0.0.3"))
	waitForFired(2, "watcher did not reload after the bad write; watch mechanism looks dead post-error")

	mu.Lock()
	defer mu.Unlock()
	if got[len(got)-1] != "10.0.0.3" {
		t.Errorf("final reloaded host = %q, want 10.0.0.3", got[len(got)-1])
	}
}

// Reproduces the reviewer's Critical 1: a file write arms the debounce timer, then
// Close is called well before the timer would fire. Close must cancel the pending
// timer so onReload never fires afterward, no matter how long the test then waits.
func TestWatcherCloseStopsPendingReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(host string) {
		body := "systems:\n  - name: lm-01\n    host: " + host + "\n    apiKey: k\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("10.0.0.1")

	var mu sync.Mutex
	fired := 0
	w, err := NewWatcher(path, func(*Config) {
		mu.Lock()
		fired++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Arm the 100ms debounce timer, then close well before it would fire.
	write("10.0.0.2")
	time.Sleep(20 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Wait comfortably past the original debounce deadline (100ms from the write,
	// i.e. ~80ms after Close). If Close failed to cancel the timer, onReload fires
	// in this window despite Close having already returned.
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("onReload fired %d time(s) after Close; want 0 — Close must cancel the pending debounce timer", fired)
	}
}

// Reproduces the reviewer's Critical 2: a SIGHUP-triggered reload and a
// debounce-timer-triggered reload racing each other. onReload must never be entered
// concurrently by the two trigger paths. reloadCount is deliberately left
// unsynchronized: if the Watcher ever let two reloads run at once, -race would catch
// the unsynchronized increment on its own, independent of the maxInFlight check.
func TestWatcherSerializesReloadAcrossTriggers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(host string) {
		body := "systems:\n  - name: lm-01\n    host: " + host + "\n    apiKey: k\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("10.0.0.1")

	var reloadCount int // deliberately unsynchronized; see doc comment above
	var inFlight int32
	var maxInFlight int32
	w, err := NewWatcher(path, func(*Config) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m {
				break
			}
			if atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		reloadCount++
		// Wide enough that a debounce-timer-triggered reload landing during a
		// SIGHUP-triggered reload (or vice versa) is virtually certain to overlap
		// if the two paths aren't serialized.
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Flood SIGHUP continuously so the watch loop is, for practical purposes,
	// always either inside a SIGHUP-triggered reload or about to start one.
	stop := make(chan struct{})
	var swg sync.WaitGroup
	swg.Add(1)
	go func() {
		defer swg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Let the flood get going, then arm the debounce timer with a write. If the
	// two trigger paths aren't serialized, the timer's reload (100ms from now)
	// fires squarely inside an in-flight SIGHUP-triggered reload.
	time.Sleep(30 * time.Millisecond)
	write("10.0.0.2")

	// Let the debounce timer fire and its reload play out while the flood
	// continues, then stop.
	time.Sleep(250 * time.Millisecond)
	close(stop)
	swg.Wait()

	if m := atomic.LoadInt32(&maxInFlight); m > 1 {
		t.Errorf("onReload observed %d concurrent invocation(s); want serialized (max 1)", m)
	}
}
