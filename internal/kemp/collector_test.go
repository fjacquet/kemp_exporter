package kemp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
)

func loopConfig() config.Collection {
	return config.Collection{
		Interval:      50 * time.Millisecond,
		Timeout:       2 * time.Second,
		MaxConcurrent: 4,
	}
}

func TestCollectOnceBuildsSnapshot(t *testing.T) {
	mc := &MockClient{
		SystemName: "lm-01",
		Transport:  "xml",
		Stats:      decodeStats(t, "stats.xml"),
		VSInfo:     decodeVSInfo(t, "listvs.xml"),
	}
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), NewSnapshotStore())
	snap := loop.CollectOnce(context.Background())

	if len(snap.Systems) != 1 {
		t.Fatalf("%d systems in snapshot, want 1", len(snap.Systems))
	}
	sys := snap.Systems[0]
	if !sys.OK {
		t.Errorf("system OK = false, want true; err = %q", sys.Err)
	}
	if sys.TransportName != "xml" {
		t.Errorf("TransportName = %q, want xml", sys.TransportName)
	}
	if up, ok := findSample(sys.Samples, "kemp_up", "lm-01"); !ok || up.Value != 1 {
		t.Errorf("kemp_up = %+v, want 1", up)
	}
	// Health, virtual-service and real-server samples all landed.
	for _, name := range []string{
		"kemp_connections_per_second",
		"kemp_virtual_service_active_connections",
		"kemp_real_server_active_connections",
	} {
		if !hasSample(sys.Samples, name) {
			t.Errorf("%s missing from the snapshot", name)
		}
	}
}

// A failing target reports down and contributes NO stale series -- frozen values
// that look live are worse than an obvious gap.
func TestCollectOnceFailedTargetEmitsOnlyDown(t *testing.T) {
	mc := &MockClient{SystemName: "lm-01", StatsErr: errors.New("connection refused")}
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), NewSnapshotStore())
	snap := loop.CollectOnce(context.Background())

	sys := snap.Systems[0]
	if sys.OK {
		t.Error("system OK = true after a stats failure")
	}
	if sys.Err == "" {
		t.Error("system Err is empty after a failure")
	}
	if up, ok := findSample(sys.Samples, "kemp_up", "lm-01"); !ok || up.Value != 0 {
		t.Errorf("kemp_up = %+v, want 0", up)
	}
	if len(sys.Samples) != 1 {
		t.Errorf("%d samples for a down target, want only kemp_up", len(sys.Samples))
	}
}

// listvs failing must NOT discard the stats: metrics stay, names go empty.
func TestCollectOnceKeepsStatsWhenListVSFails(t *testing.T) {
	mc := &MockClient{
		SystemName: "lm-01",
		Stats:      decodeStats(t, "stats.xml"),
		VSInfoErr:  errors.New("listvs unavailable"),
	}
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), NewSnapshotStore())
	sys := loop.CollectOnce(context.Background()).Systems[0]

	if !sys.OK {
		t.Errorf("system OK = false; a listvs failure must not mark the target down (err=%q)", sys.Err)
	}
	s, ok := findSample(sys.Samples, "kemp_virtual_service_active_connections")
	if !ok {
		t.Fatal("virtual-service metrics dropped when listvs failed")
	}
	if len(s.Labels) != 5 || s.Labels[1].Key != "name" || s.Labels[1].Value != "" {
		t.Errorf("labels = %+v, want five keys with an empty name value", s.Labels)
	}
	// Without listvs there is no status, so the status metrics stay absent.
	if hasSample(sys.Samples, "kemp_virtual_service_up") {
		t.Error("kemp_virtual_service_up emitted without listvs data")
	}
}

// One failing target must not take down the others.
func TestCollectOnceIsolatesTargetFailures(t *testing.T) {
	good := &MockClient{SystemName: "good", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml")}
	bad := &MockClient{SystemName: "bad", StatsErr: errors.New("unreachable")}
	loop := NewCollectionLoop([]Client{good, bad}, loopConfig(), NewSnapshotStore())
	snap := loop.CollectOnce(context.Background())

	if len(snap.Systems) != 2 {
		t.Fatalf("%d systems, want 2", len(snap.Systems))
	}
	byName := map[string]*SystemSnapshot{}
	for _, s := range snap.Systems {
		byName[s.System] = s
	}
	if !byName["good"].OK {
		t.Error("healthy target marked down because a sibling failed")
	}
	if byName["bad"].OK {
		t.Error("failing target marked up")
	}
	// The healthy target must still have actually published its metrics, not just
	// avoided being marked down.
	if !hasSample(byName["good"].Samples, "kemp_virtual_service_active_connections") {
		t.Error("healthy target's virtual-service metrics missing even though it succeeded")
	}
	if len(byName["bad"].Samples) != 1 {
		t.Errorf("failing target has %d samples, want only kemp_up", len(byName["bad"].Samples))
	}
}

// The fan-out is concurrent, so results arrive out of order; CollectOnce must
// still assemble Systems in CONFIG order, never completion order (and never by
// ranging a map). Both export readers dedup colliding samples first-wins, so if
// assembly order depended on which target happened to finish first, the
// surviving series on any collision would flip cycle to cycle -- series churn
// instead of a stable degraded state. "slow" is listed FIRST in config but
// finishes LAST; a completion-order (or map-ranged) assembly would put "fast"
// first instead.
func TestCollectOnceOrdersSystemsByConfigNotCompletion(t *testing.T) {
	slow := &MockClient{
		SystemName: "slow",
		Stats:      decodeStats(t, "stats.xml"),
		VSInfo:     decodeVSInfo(t, "listvs.xml"),
		StatsDelay: 150 * time.Millisecond,
	}
	fast := &MockClient{
		SystemName: "fast",
		Stats:      decodeStats(t, "stats.xml"),
		VSInfo:     decodeVSInfo(t, "listvs.xml"),
	}
	loop := NewCollectionLoop([]Client{slow, fast}, loopConfig(), NewSnapshotStore())
	snap := loop.CollectOnce(context.Background())

	if len(snap.Systems) != 2 {
		t.Fatalf("%d systems, want 2", len(snap.Systems))
	}
	if snap.Systems[0].System != "slow" || snap.Systems[1].System != "fast" {
		t.Fatalf("Systems = [%s %s], want config order [slow fast] regardless of which finished first",
			snap.Systems[0].System, snap.Systems[1].System)
	}
}

// A Snapshot handed to the store must never change afterward: a reader holding
// an older *Snapshot across a later Store must keep seeing exactly what it held.
// Run two cycles back to back where the target's outcome flips between them, and
// confirm the FIRST Snapshot's samples are untouched by the second cycle -- only
// possible if CollectOnce builds an entirely fresh Snapshot/SystemSnapshot/Samples
// slice each call rather than reusing (e.g. appending into) anything from before.
func TestCollectOnceDoesNotMutatePreviousSnapshot(t *testing.T) {
	mc := &MockClient{SystemName: "lm-01", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml")}
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), NewSnapshotStore())

	first := loop.CollectOnce(context.Background())
	firstUp, ok := findSample(first.Systems[0].Samples, "kemp_up", "lm-01")
	if !ok || firstUp.Value != 1 {
		t.Fatalf("first cycle kemp_up = %+v, ok=%v; want 1", firstUp, ok)
	}
	firstSampleCount := len(first.Systems[0].Samples)

	// Flip the target to failing for the second cycle.
	mc.StatsErr = errors.New("now unreachable")
	second := loop.CollectOnce(context.Background())
	secondUp, ok := findSample(second.Systems[0].Samples, "kemp_up", "lm-01")
	if !ok || secondUp.Value != 0 {
		t.Fatalf("second cycle kemp_up = %+v, ok=%v; want 0", secondUp, ok)
	}

	// The FIRST Snapshot must be exactly as it was: unaffected by the second cycle.
	firstUpAfter, ok := findSample(first.Systems[0].Samples, "kemp_up", "lm-01")
	if !ok || firstUpAfter.Value != 1 {
		t.Errorf("first snapshot mutated by the second cycle: kemp_up now %+v (ok=%v), want unchanged at 1", firstUpAfter, ok)
	}
	if len(first.Systems[0].Samples) != firstSampleCount {
		t.Errorf("first snapshot's Samples length changed from %d to %d after a later cycle",
			firstSampleCount, len(first.Systems[0].Samples))
	}
	if !first.Systems[0].OK {
		t.Error("first snapshot's OK flag flipped to false after a later cycle failed")
	}
}

// MaxConcurrent must actually cap simultaneous target collection, not merely be
// accepted without being enforced -- and the test must prove real contention
// happened, not just that nothing broke while running one target at a time. Six
// targets share one Inflight/Peak counter pair; MaxConcurrent=2 means Peak must
// never exceed 2, and because every target sleeps long enough to overlap with
// its neighbors, Peak must also reach 2 -- a "concurrency" test that stayed at 1
// throughout would mean nothing ever actually ran in parallel.
func TestCollectOnceMaxConcurrentBoundsRealContention(t *testing.T) {
	var inflight, peak atomic.Int32
	const n = 6
	const limit = 2
	clients := make([]Client, n)
	for i := 0; i < n; i++ {
		clients[i] = &MockClient{
			SystemName: fmt.Sprintf("lm-%d", i),
			Stats:      decodeStats(t, "stats.xml"),
			VSInfo:     decodeVSInfo(t, "listvs.xml"),
			StatsDelay: 80 * time.Millisecond,
			Inflight:   &inflight,
			Peak:       &peak,
		}
	}
	cc := loopConfig()
	cc.MaxConcurrent = limit
	cc.Timeout = 5 * time.Second
	loop := NewCollectionLoop(clients, cc, NewSnapshotStore())

	snap := loop.CollectOnce(context.Background())
	if len(snap.Systems) != n {
		t.Fatalf("%d systems, want %d", len(snap.Systems), n)
	}
	if got := peak.Load(); got > limit {
		t.Errorf("peak concurrent targets = %d, want <= %d (MaxConcurrent not enforced)", got, limit)
	}
	if got := peak.Load(); got < limit {
		t.Errorf("peak concurrent targets = %d, want >= %d (targets never actually ran concurrently)", got, limit)
	}
}

// Timeout must actually fire and truncate a hung target's work, not merely pass
// because the target happened to finish quickly. StatsDelay is deliberately far
// longer than Timeout; CollectOnce returning well before StatsDelay elapses is
// only possible if the timeout cut the call off.
func TestCollectOnceTimeoutTruncatesHungTarget(t *testing.T) {
	mc := &MockClient{
		SystemName: "lm-01",
		Stats:      decodeStats(t, "stats.xml"),
		StatsDelay: 5 * time.Second, // far longer than Timeout below
	}
	cc := loopConfig()
	cc.Timeout = 50 * time.Millisecond
	loop := NewCollectionLoop([]Client{mc}, cc, NewSnapshotStore())

	start := time.Now()
	snap := loop.CollectOnce(context.Background())
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Fatalf("CollectOnce took %s; Timeout (%s) did not cut off the hung target", elapsed, cc.Timeout)
	}
	sys := snap.Systems[0]
	if sys.OK {
		t.Error("system OK = true for a target that never returned inside Timeout")
	}
	if !strings.Contains(sys.Err, "deadline exceeded") {
		t.Errorf("Err = %q, want it to mention the deadline/timeout", sys.Err)
	}
}

// Timeout bounds a SINGLE target's own work, starting when that target's own
// collection begins -- not one deadline shared across the whole cycle and
// anchored at CollectOnce's start. With MaxConcurrent=1, "second" cannot even
// start until "first" (100ms of work) finishes, so a shared-deadline
// implementation with Timeout=150ms would leave "second" only ~50ms of its own
// 100ms of work before the (already 100ms-consumed) deadline fired, and would
// wrongly report it down. A correct per-target budget gives EACH target its own
// full 150ms starting when it begins, so both finish comfortably inside their
// own budget despite the pair needing ~200ms serialized end to end.
func TestCollectOnceTimeoutIsPerTargetNotSharedAcrossCycle(t *testing.T) {
	first := &MockClient{SystemName: "first", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml"), StatsDelay: 100 * time.Millisecond}
	second := &MockClient{SystemName: "second", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml"), StatsDelay: 100 * time.Millisecond}

	cc := config.Collection{Interval: time.Second, Timeout: 150 * time.Millisecond, MaxConcurrent: 1}
	loop := NewCollectionLoop([]Client{first, second}, cc, NewSnapshotStore())

	snap := loop.CollectOnce(context.Background())
	if len(snap.Systems) != 2 {
		t.Fatalf("%d systems, want 2", len(snap.Systems))
	}
	for _, sys := range snap.Systems {
		if !sys.OK {
			t.Errorf("system %s OK = false (err=%q); a per-target timeout budget should have covered its 100ms of work", sys.System, sys.Err)
		}
	}
}

// A zero-value config.Collection (e.g. a caller that skipped config.Load's
// defaulting) must not silently wedge collection forever:
// errgroup.(*Group).SetLimit(0) blocks every future Go call permanently, which
// would hang every future cycle rather than just degrading it.
func TestCollectOnceZeroMaxConcurrentDoesNotDeadlock(t *testing.T) {
	mc := &MockClient{SystemName: "lm-01", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml")}
	cc := loopConfig()
	cc.MaxConcurrent = 0
	loop := NewCollectionLoop([]Client{mc}, cc, NewSnapshotStore())

	done := make(chan *Snapshot, 1)
	go func() { done <- loop.CollectOnce(context.Background()) }()

	select {
	case snap := <-done:
		if len(snap.Systems) != 1 {
			t.Fatalf("%d systems, want 1", len(snap.Systems))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CollectOnce hung with MaxConcurrent=0 (errgroup.SetLimit(0) deadlocks Go calls)")
	}
}

func TestRunPublishesAndStops(t *testing.T) {
	mc := &MockClient{SystemName: "lm-01", Stats: decodeStats(t, "stats.xml"), VSInfo: decodeVSInfo(t, "listvs.xml")}
	store := NewSnapshotStore()
	loop := NewCollectionLoop([]Client{mc}, loopConfig(), store)

	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(store.Load().Systems) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(store.Load().Systems) == 0 {
		t.Fatal("no snapshot published within 3s")
	}
	cancel()

	// After cancellation the loop must stop issuing calls.
	time.Sleep(150 * time.Millisecond)
	before := mc.StatsCalls.Load()
	time.Sleep(200 * time.Millisecond)
	if mc.StatsCalls.Load() != before {
		t.Errorf("loop kept collecting after cancel: %d -> %d", before, mc.StatsCalls.Load())
	}
}

// Run must return promptly on cancellation even while a cycle is in flight, and
// must not leave its goroutines running afterward. A slow target keeps a cycle
// in flight; cancelling shortly after starting Run (well before StatsDelay would
// naturally return) proves Run's own return -- observed via a closed done
// channel -- is caused by cancellation propagating into the in-flight target
// call, not by the target simply finishing on its own.
func TestRunExitsPromptlyWhenCancelledMidCycle(t *testing.T) {
	mc := &MockClient{
		SystemName: "lm-01",
		Stats:      decodeStats(t, "stats.xml"),
		VSInfo:     decodeVSInfo(t, "listvs.xml"),
		StatsDelay: 2 * time.Second,
	}
	cc := loopConfig()
	cc.Timeout = 5 * time.Second // long enough that only cancellation, not Timeout, ends the cycle early
	store := NewSnapshotStore()
	loop := NewCollectionLoop([]Client{mc}, cc, store)

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loop.Run(ctx)
		close(done)
	}()

	// Give the first cycle time to actually enter the slow GetStatistics call.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not return within 1s of cancellation while a cycle was in flight")
	}

	// The goroutine count should settle back down rather than staying elevated
	// forever, which is what a goroutine leaked behind by the aborted cycle would
	// look like.
	deadline := time.Now().Add(1 * time.Second)
	for {
		if runtime.NumGoroutine() <= before+2 { // small slack for GC/runtime bookkeeping goroutines
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count stayed elevated after Run returned: before=%d, after=%d", before, runtime.NumGoroutine())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SetClients can be called for hot config reload while Run is executing
// concurrently; that is shared mutable state and must be race-safe under
// `go test -race`, not just logically correct on a single-goroutine reading of
// the code. Run continuously while another goroutine repeatedly swaps the
// client set; the assertions are about not crashing/racing and never observing
// a half-built snapshot, not about which particular client set won any race.
func TestSetClientsConcurrentWithRun(t *testing.T) {
	mkClients := func(prefix string, n int) []Client {
		out := make([]Client, n)
		for i := range out {
			out[i] = &MockClient{
				SystemName: fmt.Sprintf("%s-%d", prefix, i),
				Stats:      decodeStats(t, "stats.xml"),
				VSInfo:     decodeVSInfo(t, "listvs.xml"),
			}
		}
		return out
	}

	cc := config.Collection{Interval: 5 * time.Millisecond, Timeout: 2 * time.Second, MaxConcurrent: 4}
	store := NewSnapshotStore()
	loop := NewCollectionLoop(mkClients("a", 2), cc, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if i%2 == 0 {
				loop.SetClients(mkClients("a", 2))
			} else {
				loop.SetClients(mkClients("b", 3))
			}
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()

	// Give one more cycle time to settle, then sanity-check the store never
	// exposed a nil or half-built snapshot.
	time.Sleep(20 * time.Millisecond)
	snap := store.Load()
	if snap == nil {
		t.Fatal("store.Load() returned nil")
	}
	for _, sys := range snap.Systems {
		if sys == nil {
			t.Error("snapshot contains a nil *SystemSnapshot")
		}
	}
}
