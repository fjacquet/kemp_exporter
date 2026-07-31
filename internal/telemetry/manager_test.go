package telemetry

import (
	"context"
	"errors"
	"testing"
)

type fakeShutdowner struct {
	err      error
	shutdown bool
}

func (f *fakeShutdowner) Shutdown(_ context.Context) error {
	f.shutdown = true
	return f.err
}

func TestShutdownAllCallsEveryProvider(t *testing.T) {
	a := &fakeShutdowner{}
	b := &fakeShutdowner{}
	ShutdownAll(context.Background(), a, b)
	if !a.shutdown || !b.shutdown {
		t.Fatalf("ShutdownAll did not call Shutdown on every provider: a=%v b=%v", a.shutdown, b.shutdown)
	}
}

// A failing provider must not stop ShutdownAll from reaching the rest, and must
// not panic: process shutdown has nothing left to react to an error, so the doc
// comment says this logs rather than returns or propagates it.
func TestShutdownAllContinuesPastAnError(t *testing.T) {
	failing := &fakeShutdowner{err: errors.New("boom")}
	after := &fakeShutdowner{}
	ShutdownAll(context.Background(), failing, after)
	if !after.shutdown {
		t.Fatal("a failing provider's error stopped ShutdownAll from reaching a later provider")
	}
}

// A nil Shutdowner slot is a real shape at the intended call site: a feature
// disabled at config time (e.g. OTLP push) leaves that slot nil rather than
// omitted from the call. ShutdownAll must skip it without panicking and still
// reach the providers around it.
func TestShutdownAllSkipsNilWithoutPanic(t *testing.T) {
	before := &fakeShutdowner{}
	after := &fakeShutdowner{}
	ShutdownAll(context.Background(), before, nil, after)
	if !before.shutdown || !after.shutdown {
		t.Fatalf("ShutdownAll did not reach providers around a nil entry: before=%v after=%v", before.shutdown, after.shutdown)
	}
}
