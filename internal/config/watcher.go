package config

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
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
//
// onReload is never invoked concurrently with itself: a SIGHUP-triggered reload and a
// debounce-timer-triggered reload both run reload() under the same mutex, so the two
// trigger paths can never race each other or overlap in the callback.
//
// Close() guarantees that no onReload call fires after it returns: it stops any
// pending debounce timer, waits for a reload already in flight to finish, and blocks
// until the watch-loop goroutine has fully exited before releasing the filesystem
// watch. A caller may safely tear down any state onReload touches as soon as Close
// returns.
type Watcher struct {
	path     string
	onReload func(*Config)
	fsw      *fsnotify.Watcher

	mu     sync.Mutex // serializes reload() and guards closed/timer below
	closed bool
	timer  *time.Timer

	closeCh chan struct{} // closed by Close to stop the watch loop, independent of ctx
	wg      sync.WaitGroup
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
	return &Watcher{
		path:     abs,
		onReload: onReload,
		fsw:      fsw,
		closeCh:  make(chan struct{}),
	}, nil
}

// Start runs the watch loop until ctx is cancelled or Close is called.
func (w *Watcher) Start(ctx context.Context) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer signal.Stop(sighup)
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.closeCh:
				return
			case <-sighup:
				w.reload()
			case ev, ok := <-w.fsw.Events:
				if !ok {
					return
				}
				if filepath.Clean(ev.Name) == w.path {
					w.scheduleReload()
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

// scheduleReload (re)arms the debounce timer that coalesces the burst of fsnotify
// events a single save emits into one reload attempt. It is a no-op once Close has
// begun shutting the watcher down.
func (w *Watcher) scheduleReload() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(100*time.Millisecond, w.reload)
}

// reload re-reads the config, invoking the callback only on success. It holds mu for
// its entire body, which does two things at once: it serializes a SIGHUP-triggered
// reload against a debounce-timer-triggered reload so onReload is never entered
// concurrently, and it gives Close a way to block until a reload already in flight
// has finished before Close marks the watcher closed.
func (w *Watcher) reload() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	cfg, err := Load(w.path)
	if err != nil {
		logrus.WithError(err).Error("config reload failed; keeping previous configuration")
		return
	}
	logrus.Info("configuration reloaded")
	w.onReload(cfg)
}

// Close stops the watcher and blocks until no further onReload callback can fire.
// It stops the pending debounce timer (so it never fires), signals and waits for the
// watch-loop goroutine to exit (so its deferred signal.Stop has already run), and
// only then releases the filesystem watch. Calling Close more than once is safe; the
// second call is a no-op.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()

	close(w.closeCh)
	w.wg.Wait()

	return w.fsw.Close()
}
