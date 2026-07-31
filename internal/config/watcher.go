package config

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
)

// Watcher reloads the configuration on SIGHUP and on file change.
//
// It watches the config file's DIRECTORY rather than the file itself: editors and
// config-management tools replace files by writing a temporary file and renaming it
// over the target, which detaches an inode-level watch after the first change.
type Watcher struct {
	path     string
	onReload func(*Config)
	fsw      *fsnotify.Watcher
}

// NewWatcher creates a watcher for path. onReload runs only for a config that
// parses and validates.
func NewWatcher(path string, onReload func(*Config)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = fsw.Close()
		return nil, err
	}
	if err := fsw.Add(filepath.Dir(abs)); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	return &Watcher{path: abs, onReload: onReload, fsw: fsw}, nil
}

// Start runs the watch loop until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	go func() {
		defer signal.Stop(sighup)
		// Coalesce bursts: a single save often emits several fsnotify events.
		var timer *time.Timer
		debounce := func() {
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(100*time.Millisecond, w.reload)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-sighup:
				w.reload()
			case ev, ok := <-w.fsw.Events:
				if !ok {
					return
				}
				if filepath.Clean(ev.Name) == w.path {
					debounce()
				}
			case err, ok := <-w.fsw.Errors:
				if !ok {
					return
				}
				logrus.WithError(err).Warn("config watcher error")
			}
		}
	}()
}

// reload re-reads the config, invoking the callback only on success.
func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		logrus.WithError(err).Error("config reload failed; keeping previous configuration")
		return
	}
	logrus.Info("configuration reloaded")
	w.onReload(cfg)
}

// Close releases the filesystem watch.
func (w *Watcher) Close() error { return w.fsw.Close() }
