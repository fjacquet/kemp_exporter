package kemp

// upSample reports whether the last collection cycle reached and authenticated
// against a LoadMaster.
//
// This is per-target and per-cycle: it describes the backend, not the liveness of
// the exporter's own HTTP handler. A wedged collection loop leaves every kemp_up at
// a stale 1, which is why /health reports snapshot age separately.
//
// Unlike every other sample in this package, upSample does not route through
// addSample: up is always present — it is 1 or 0, never absent, because "we could
// not reach the appliance" is exactly what 0 means. addSample's absent-never-zero
// choke point exists to keep unparsed appliance fields from being reported as a
// fabricated 0; there is no such field here to be absent, so bypassing it is
// correct rather than a gap in the choke point.
func upSample(system string, ok bool) Sample {
	v := 0.0
	if ok {
		v = 1
	}
	return Sample{Name: "kemp_up", Labels: systemLabels(system), Value: v}
}
