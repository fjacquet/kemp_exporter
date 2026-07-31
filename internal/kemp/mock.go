package kemp

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// MockClient is a Client double for tests. It lives in a non-test file so tests in
// other packages (and main's wiring tests) can use it too.
//
// StatsCalls/VSInfoCalls are atomic.Int64 rather than plain ints: the collection
// loop's own tests read these counters from the main test goroutine while Run
// calls GetStatistics/ListVirtualServices concurrently from its own goroutines. A
// plain int would be a genuine data race under `go test -race` even when the two
// accesses never land at the exact same instant -- the race detector flags the
// missing happens-before edge between the accesses, not literal overlap.
type MockClient struct {
	SystemName string
	Transport  string
	Stats      *models.Statistics
	StatsErr   error
	VSInfo     []models.VirtualServiceInfo
	VSInfoErr  error

	// StatsDelay/VSInfoDelay, when non-zero, make the corresponding method block
	// for that long -- or until ctx is done, whichever comes first -- before
	// returning. Tests use this to simulate a slow or hung target for timeout,
	// cancellation, and concurrency-limit coverage.
	StatsDelay  time.Duration
	VSInfoDelay time.Duration

	// Inflight/Peak, when non-nil, are shared across every MockClient in one test
	// so the test can observe cross-target concurrency directly: Inflight counts
	// GetStatistics calls currently in progress across every client sharing the
	// pointer, and Peak records the high-water mark. This lets a MaxConcurrent
	// test assert on a real, measured bound instead of inferring one from
	// wall-clock timing.
	Inflight *atomic.Int32
	Peak     *atomic.Int32

	StatsCalls  atomic.Int64
	VSInfoCalls atomic.Int64
}

// Name returns the configured system name.
func (m *MockClient) Name() string { return m.SystemName }

// TransportName reports the configured transport label.
func (m *MockClient) TransportName() string { return m.Transport }

// GetStatistics returns the canned statistics or error, honoring StatsDelay and
// the Inflight/Peak bookkeeping.
func (m *MockClient) GetStatistics(ctx context.Context) (*models.Statistics, error) {
	m.StatsCalls.Add(1)
	m.trackInflight()
	defer m.untrackInflight()

	if m.StatsDelay > 0 {
		select {
		case <-time.After(m.StatsDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.StatsErr != nil {
		return nil, m.StatsErr
	}
	return m.Stats, nil
}

// ListVirtualServices returns the canned virtual-service metadata or error,
// honoring VSInfoDelay.
func (m *MockClient) ListVirtualServices(ctx context.Context) ([]models.VirtualServiceInfo, error) {
	m.VSInfoCalls.Add(1)
	if m.VSInfoDelay > 0 {
		select {
		case <-time.After(m.VSInfoDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.VSInfoErr != nil {
		return nil, m.VSInfoErr
	}
	return m.VSInfo, nil
}

func (m *MockClient) trackInflight() {
	if m.Inflight == nil {
		return
	}
	n := m.Inflight.Add(1)
	if m.Peak == nil {
		return
	}
	for {
		p := m.Peak.Load()
		if n <= p || m.Peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (m *MockClient) untrackInflight() {
	if m.Inflight == nil {
		return
	}
	m.Inflight.Add(-1)
}

var _ Client = (*MockClient)(nil)
