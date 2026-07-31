// Package telemetry owns the OTLP exporter lifecycle so main.go does not have to.
package telemetry

import (
	"context"

	"github.com/sirupsen/logrus"
)

// Shutdowner is anything with a Shutdown method — satisfied by kemp.OTLPExporter.
// Declaring the interface here rather than importing kemp keeps the dependency
// pointing one way: main wires the two together, neither package imports the other.
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// ShutdownAll flushes and stops every provider, logging rather than returning
// errors: this runs during process shutdown, where there is nothing left to react.
//
// The nil check below only catches a genuinely nil Shutdowner interface value —
// not a non-nil interface wrapping a nil concrete pointer (e.g. a `var e
// *kemp.OTLPExporter` left unset because OTLP push is disabled). Passing the
// latter here would pass the nil check, then panic inside that type's own
// Shutdown method. Callers must only append a provider once it has actually been
// constructed (e.g. behind `if cfg.OTel.Enabled`), never an unset typed pointer.
func ShutdownAll(ctx context.Context, providers ...Shutdowner) {
	for _, p := range providers {
		if p == nil {
			continue
		}
		if err := p.Shutdown(ctx); err != nil {
			logrus.WithError(err).Warn("telemetry shutdown failed")
		}
	}
}
