package kemp

import (
	"sync"

	"github.com/sirupsen/logrus"
)

// dropWarnings bounds the "dropping sample" Warn lines both readers emit.
//
// Both readers used to log once per dropped sample, per scrape, forever. That is
// not an exceptional path: prometheus.go's own doc comment documents SubVS rows as
// a NORMAL source of byte-identical labels (a SubVS carries its parent's VIP and
// port, so two st.VirtualServices entries resolve to the same vsKey), and SubVSs
// are a common LoadMaster configuration. On such an appliance the drop is
// steady-state behaviour: 7 metrics x N SubVSs x 2 readers, every cycle, forever --
// into a log file that internal/logging never rotates, and burying the one-off
// lines that actually mean something.
//
// The bound is one line per distinct (reason, metric name, system) per process. A
// genuinely new drop -- a different metric, a different appliance, a different
// reason -- still logs immediately; only the repetition is suppressed. The first
// line says so, so an operator reading it knows the silence afterwards is the
// limiter and not the condition clearing.
//
// It is shared by both readers rather than duplicated in each, for the same reason
// the schema rules are: two independent copies of a policy are two things to keep
// in sync, and this project has already paid for one such divergence.
type dropWarnings struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// newDropWarnings returns an empty limiter. One is created per collector/exporter,
// so its memory is bounded by the (metric name x system x reason) product, which
// is bounded in turn by metrics.go's fixed builder set and the configured targets.
func newDropWarnings() *dropWarnings {
	return &dropWarnings{seen: make(map[string]struct{})}
}

// warn logs entry at Warn the first time this (reason, metric, system) triple is
// seen, and stays silent for every later occurrence.
func (d *dropWarnings) warn(reason, metric, system string, entry *logrus.Entry, msg string) {
	key := reason + "\x00" + metric + "\x00" + system
	d.mu.Lock()
	_, repeat := d.seen[key]
	if !repeat {
		d.seen[key] = struct{}{}
	}
	d.mu.Unlock()
	if repeat {
		return
	}
	entry.Warn(msg + " (this is logged once per metric name per system for the life of the process; " +
		"later occurrences of the same drop are suppressed)")
}
