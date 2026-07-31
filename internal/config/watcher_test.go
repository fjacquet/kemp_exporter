package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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
// last good configuration rather than losing its targets to a typo.
func TestWatcherIgnoresInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("systems:\n  - name: lm-01\n    host: 10.0.0.1\n    apiKey: k\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

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
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	if err := os.WriteFile(path, []byte("systems: [\n"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("callback fired %d times for an invalid config; want 0", fired)
	}
}
