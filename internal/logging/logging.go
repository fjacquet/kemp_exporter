// Package logging configures the process-wide logrus logger.
package logging

import (
	"os"

	"github.com/sirupsen/logrus"
)

// Setup directs logs to stdout when logName is empty, or appends to the named file.
// Writing to stdout is the default so a systemd unit or container captures logs
// through the journal or the container runtime rather than a file the operator has
// to rotate.
func Setup(logName string, debug bool) error {
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}
	if logName == "" {
		logrus.SetOutput(os.Stdout)
		return nil
	}
	f, err := os.OpenFile(logName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	logrus.SetOutput(f)
	return nil
}
