// Package logging configures the process-wide logrus logger.
package logging

import (
	"os"
	"sync"

	"github.com/sirupsen/logrus"
)

// mu guards currentLog, the file handle (if any) that a prior Setup call opened.
var (
	mu         sync.Mutex
	currentLog *os.File
)

// Setup directs logs to stdout when logName is empty, or appends to the named file.
// Writing to stdout is the default so a systemd unit or container captures logs
// through the journal or the container runtime rather than a file the operator has
// to rotate.
//
// Setup may be called more than once (a future config-reload path may want to switch
// log destinations at runtime): each call closes the file handle opened by the
// previous call, if any, so repeated calls don't leak file descriptors. The previous
// handle is only closed after the new destination is in place, so a failed switch
// leaves logging on its prior destination rather than dropping it.
func Setup(logName string, debug bool) error {
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}

	mu.Lock()
	defer mu.Unlock()
	prev := currentLog

	if logName == "" {
		logrus.SetOutput(os.Stdout)
		currentLog = nil
		if prev != nil {
			_ = prev.Close()
		}
		return nil
	}

	f, err := os.OpenFile(logName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	logrus.SetOutput(f)
	currentLog = f
	if prev != nil {
		_ = prev.Close()
	}
	return nil
}
