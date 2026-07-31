package kemp

import (
	"context"
	"sync"
	"time"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/fjacquet/kemp_exporter/internal/models"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// CollectionLoop polls every LoadMaster on an interval and publishes an immutable
// snapshot. Decoupling collection from scraping means backend API load depends on
// the interval alone, not on how many Prometheus servers scrape the exporter.
type CollectionLoop struct {
	cc    config.Collection
	store *SnapshotStore

	mu      sync.RWMutex
	clients []Client
}

// NewCollectionLoop builds a loop over the given clients, in the order they must
// appear in every published Snapshot.
func NewCollectionLoop(clients []Client, cc config.Collection, store *SnapshotStore) *CollectionLoop {
	return &CollectionLoop{cc: cc, store: store, clients: clients}
}

// SetClients swaps the target set, for config hot reload. Safe to call while Run
// is executing concurrently: the mutex only ever guards the pointer/slice header
// itself, and a cycle already in flight has already taken its own private copy
// via snapshotClients, so a swap here can only affect the NEXT cycle to start.
func (l *CollectionLoop) SetClients(clients []Client) {
	l.mu.Lock()
	l.clients = clients
	l.mu.Unlock()
}

// snapshotClients returns a private copy of the current target set, in config
// order, so a concurrent SetClients can never change the slice a cycle already
// committed to iterating.
func (l *CollectionLoop) snapshotClients() []Client {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Client, len(l.clients))
	copy(out, l.clients)
	return out
}

// Run collects immediately, then on every interval tick until ctx is cancelled.
// Collecting up front means /metrics carries real data as soon as possible
// rather than after a full interval of emptiness.
func (l *CollectionLoop) Run(ctx context.Context) {
	l.store.Store(l.CollectOnce(ctx))

	ticker := time.NewTicker(l.cc.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.store.Store(l.CollectOnce(ctx))
		}
	}
}

// CollectOnce runs one full cycle across every target and returns the snapshot
// without publishing it. Exported so `--once` can collect, dump and exit.
//
// The fan-out is concurrent (bounded by cc.MaxConcurrent via errgroup.SetLimit)
// and therefore completes targets out of order; each goroutine writes its result
// into results at its own config index rather than appending as results arrive,
// so the Snapshot this returns always lists Systems in config order regardless of
// which target actually finished first. That determinism matters because both
// export readers dedup colliding samples first-wins: if Systems were assembled in
// completion order (or, worse, by ranging a map), the surviving series on any
// collision would flip cycle to cycle -- series churn instead of a stable
// degraded state.
//
// Everything returned here -- the Systems slice, every *SystemSnapshot, every
// Samples slice -- is freshly allocated by this call. Nothing from a previous
// cycle's Snapshot is ever reused or appended to, which is what lets a reader
// hold an old *Snapshot across a later Store and never see it change underneath
// it.
func (l *CollectionLoop) CollectOnce(ctx context.Context) *Snapshot {
	clients := l.snapshotClients()
	results := make([]*SystemSnapshot, len(clients))

	limit := l.cc.MaxConcurrent
	if limit <= 0 {
		// A misconfigured or zero-value config.Collection must not silently wedge
		// collection forever: errgroup.(*Group).SetLimit(0) blocks every future Go
		// call permanently, which would hang every future cycle rather than just
		// degrading it.
		limit = 1
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for i, c := range clients {
		i, c := i, c
		g.Go(func() error {
			results[i] = collectSystem(gctx, c, l.cc.Timeout)
			return nil // per-target failures degrade the cycle; they never fail it
		})
	}
	_ = g.Wait()

	return &Snapshot{BuiltAt: time.Now(), Systems: results}
}

// collectSystem gathers one LoadMaster's samples, bounding the whole per-target
// call (stats + listvs) at timeout starting from when THIS call begins -- not
// from when the enclosing cycle started. That distinction matters once
// MaxConcurrent queues a target behind others: timeout is documented to bound a
// single target's own work, so a target that had to wait behind the concurrency
// limit still gets its own full budget rather than whatever was left of a
// cycle-wide deadline by the time its goroutine finally got to run.
//
// stats and listvs are fetched concurrently. A stats failure marks the target
// down and emits kemp_up=0 with nothing else -- no stale series ever survives
// from a prior cycle, because this SystemSnapshot and its Samples are always
// freshly built. A listvs failure is tolerated: the metrics stay and service
// names fall back to empty, because losing a label value is far better than
// losing the metrics outright.
func collectSystem(ctx context.Context, c Client, timeout time.Duration) *SystemSnapshot {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &SystemSnapshot{
		System:        c.Name(),
		LastScrape:    time.Now(),
		TransportName: c.TransportName(),
	}

	var stats *models.Statistics
	var vsInfo []models.VirtualServiceInfo

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		s, err := c.GetStatistics(gctx)
		if err != nil {
			return err
		}
		stats = s
		return nil
	})
	g.Go(func() error {
		v, err := c.ListVirtualServices(gctx)
		if err != nil {
			// Tolerated: names degrade to empty, metrics survive.
			logrus.WithError(err).WithField("system", c.Name()).
				Warn("listvs failed; virtual-service names will be empty this cycle")
			return nil
		}
		vsInfo = v
		return nil
	})

	err := g.Wait()
	if err != nil || stats == nil {
		out.OK = false
		if err != nil {
			out.Err = err.Error()
		} else {
			out.Err = "no statistics returned"
		}
		logrus.WithError(err).WithField("system", c.Name()).Error("collection failed")
		out.Samples = []Sample{upSample(c.Name(), false)}
		return out
	}

	out.OK = true
	out.TransportName = c.TransportName() // detection may have resolved during this call
	out.Samples = append(out.Samples, upSample(c.Name(), true))
	out.Samples = append(out.Samples, deriveHealth(c.Name(), stats)...)
	out.Samples = append(out.Samples, deriveVirtualServices(c.Name(), stats, vsInfo)...)
	out.Samples = append(out.Samples, deriveRealServers(c.Name(), stats, vsInfo)...)
	return out
}
