package logging

import (
	"path/filepath"
	"testing"
)

// A future config-reload path may call Setup again to switch log destinations at
// runtime. Setup must close the previously opened file handle when it does, or every
// reload leaks a file descriptor.
func TestSetupClosesPreviousLogFile(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.log")
	fileB := filepath.Join(dir, "b.log")

	if err := Setup(fileA, false); err != nil {
		t.Fatalf("Setup(fileA): %v", err)
	}
	first := currentLog
	if first == nil {
		t.Fatal("currentLog is nil after Setup with a file path")
	}

	if err := Setup(fileB, false); err != nil {
		t.Fatalf("Setup(fileB): %v", err)
	}

	if _, err := first.Write([]byte("x")); err == nil {
		t.Error("previous log file handle is still open after switching to a new log file; want it closed")
	}

	if err := Setup("", false); err != nil {
		t.Fatalf("Setup(stdout): %v", err)
	}
	if currentLog != nil {
		t.Error("currentLog should be nil after switching to stdout")
	}
}
