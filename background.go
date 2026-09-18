package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

var errScheduledRefreshIncomplete = errors.New("one or more accounts failed to refresh")

type backgroundRefreshFunc func(context.Context) error

type monitorBackgroundScheduler struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	running bool
}

var backgroundScheduler = monitorBackgroundScheduler{wake: make(chan struct{}, 1)}

func startBackgroundScheduler() {
	state.mu.RLock()
	enabled := state.cfg.Enabled
	ttl := time.Duration(state.cfg.CacheTTLSeconds) * time.Second
	syncInterval := time.Duration(state.cfg.SyncIntervalSeconds) * time.Second
	state.mu.RUnlock()
	if !enabled {
		stopBackgroundScheduler()
		return
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if syncInterval <= 0 {
		syncInterval = time.Minute
	}
	if ttl < syncInterval {
		syncInterval = ttl
	}
	backgroundScheduler.start(syncInterval, func(ctx context.Context) error {
		if err := refreshAccountsContext(ctx, "", false, nil); err != nil {
			return err
		}
		for _, snapshot := range state.store.list() {
			if snapshot.Status == statusError || snapshot.Error != nil {
				return errScheduledRefreshIncomplete
			}
		}
		return nil
	})
}

func stopBackgroundScheduler() {
	backgroundScheduler.stop()
}

func wakeBackgroundScheduler() {
	backgroundScheduler.notify()
}

func (s *monitorBackgroundScheduler) start(interval time.Duration, refresh backgroundRefreshFunc) {
	if interval <= 0 {
		interval = time.Minute
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		s.notify()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.running = true
	s.mu.Unlock()

	go func() {
		defer close(done)
		defer func() {
			s.mu.Lock()
			s.running = false
			s.cancel = nil
			s.done = nil
			s.mu.Unlock()
		}()
		s.loop(ctx, interval, refresh)
	}()
}

func (s *monitorBackgroundScheduler) stop() {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()
	if cancel == nil || done == nil {
		return
	}
	cancel()
	<-done
}

func (s *monitorBackgroundScheduler) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *monitorBackgroundScheduler) loop(ctx context.Context, interval time.Duration, refresh backgroundRefreshFunc) {
	failures := 0
	for {
		delay := nextBackgroundDelay(interval, failures)
		if hasPendingBackgroundWork() {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-s.wake:
			if !timer.Stop() {
				<-timer.C
			}
			continue
		case <-timer.C:
		}
		err := refresh(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failures++
			continue
		}
		failures = 0
	}
}

func nextBackgroundDelay(interval time.Duration, failures int) time.Duration {
	if interval <= 0 {
		interval = time.Minute
	}
	if failures > 0 {
		shift := failures
		if shift > 4 {
			shift = 4
		}
		interval *= time.Duration(1 << shift)
	}
	if max := 15 * time.Minute; interval > max {
		interval = max
	}
	jitter := interval / 10
	if jitter <= 0 {
		return interval
	}
	return interval - jitter + time.Duration(rand.Int64N(int64(jitter*2)+1))
}

func hasPendingBackgroundWork() bool {
	state.mu.RLock()
	ttl := time.Duration(state.cfg.CacheTTLSeconds) * time.Second
	state.mu.RUnlock()
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	now := time.Now().UTC()
	for _, snapshot := range state.store.list() {
		if !state.prefs.isMonitored(snapshot.AccountID, false) {
			continue
		}
		if snapshotNeedsRefresh(snapshot.AccountID, ttl, now) {
			return true
		}
	}
	views, err := discoverProviderViews("")
	if err != nil {
		return true
	}
	for _, view := range views {
		if view.Monitored && view.Latest == nil {
			return true
		}
	}
	return false
}
