package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type snapshotStore struct {
	mu      sync.RWMutex
	entries map[string]accountSnapshot
	history map[string][]accountSnapshot
}

func newSnapshotStore() *snapshotStore {
	return &snapshotStore{entries: make(map[string]accountSnapshot), history: make(map[string][]accountSnapshot)}
}

func (s *snapshotStore) put(snapshot accountSnapshot) {
	if s == nil || snapshot.AccountID == "" {
		return
	}
	s.mu.Lock()
	s.entries[snapshot.AccountID] = snapshot
	s.history[snapshot.AccountID] = append([]accountSnapshot{snapshot}, s.history[snapshot.AccountID]...)
	if len(s.history[snapshot.AccountID]) > 100 {
		s.history[snapshot.AccountID] = s.history[snapshot.AccountID][:100]
	}
	s.mu.Unlock()
}

func (s *snapshotStore) get(id string) (accountSnapshot, bool) {
	if s == nil {
		return accountSnapshot{}, false
	}
	s.mu.RLock()
	snapshot, ok := s.entries[id]
	s.mu.RUnlock()
	return snapshot, ok
}

func (s *snapshotStore) delete(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, id)
	delete(s.history, id)
	s.mu.Unlock()
}

func (s *snapshotStore) historyFor(id string, limit int) []accountSnapshot {
	if s == nil || id == "" {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	s.mu.RLock()
	items := s.history[id]
	if len(items) > limit {
		items = items[:limit]
	}
	out := append([]accountSnapshot(nil), items...)
	s.mu.RUnlock()
	return out
}

func (s *snapshotStore) list() []accountSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	out := make([]accountSnapshot, 0, len(s.entries))
	for _, snapshot := range s.entries {
		out = append(out, snapshot)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		leftName := strings.ToLower(strings.TrimSpace(out[i].AccountName))
		rightName := strings.ToLower(strings.TrimSpace(out[j].AccountName))
		if leftName != rightName {
			return leftName < rightName
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

func (s *snapshotStore) rename(id, name string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	if snapshot, ok := s.entries[id]; ok {
		snapshot.AccountName = name
		s.entries[id] = snapshot
	}
	for index := range s.history[id] {
		s.history[id][index].AccountName = name
	}
	s.mu.Unlock()
}

func (s *snapshotStore) load(path string) error {
	if s == nil || path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read snapshot cache: %w", err)
	}
	var entries map[string]accountSnapshot
	if err := json.Unmarshal(raw, &entries); err == nil {
		s.mu.Lock()
		for id, snapshot := range entries {
			if id != "" && !snapshot.CheckedAt.IsZero() {
				if snapshot.Kind == "" {
					snapshot.Kind = inferAccountKind(snapshot)
				}
				s.entries[id] = snapshot
				s.history[id] = []accountSnapshot{snapshot}
			}
		}
		s.mu.Unlock()
		return nil
	}
	var cached struct {
		Entries map[string]accountSnapshot   `json:"entries"`
		History map[string][]accountSnapshot `json:"history"`
	}
	if err := json.Unmarshal(raw, &cached); err != nil {
		return fmt.Errorf("decode snapshot cache: %w", err)
	}
	s.mu.Lock()
	for id, snapshot := range cached.Entries {
		if id != "" && !snapshot.CheckedAt.IsZero() {
			if snapshot.Kind == "" {
				snapshot.Kind = inferAccountKind(snapshot)
			}
			s.entries[id] = snapshot
		}
	}
	for id, snapshots := range cached.History {
		if len(snapshots) > 100 {
			snapshots = snapshots[:100]
		}
		s.history[id] = append([]accountSnapshot(nil), snapshots...)
	}
	s.mu.Unlock()
	return nil
}

func (s *snapshotStore) prune(keep map[string]struct{}) {
	if s == nil {
		return
	}
	s.mu.Lock()
	for id := range s.entries {
		if _, ok := keep[id]; !ok {
			delete(s.entries, id)
			delete(s.history, id)
		}
	}
	s.mu.Unlock()
}

func (s *snapshotStore) save(path string) error {
	if s == nil || path == "" {
		return nil
	}
	s.mu.RLock()
	entries := make(map[string]accountSnapshot, len(s.entries))
	history := make(map[string][]accountSnapshot, len(s.history))
	for id, snapshot := range s.entries {
		entries[id] = snapshot
	}
	for id, snapshots := range s.history {
		history[id] = append([]accountSnapshot(nil), snapshots...)
	}
	s.mu.RUnlock()
	raw, err := json.Marshal(struct {
		Entries map[string]accountSnapshot   `json:"entries"`
		History map[string][]accountSnapshot `json:"history"`
	}{Entries: entries, History: history})
	if err != nil {
		return fmt.Errorf("encode snapshot cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create snapshot cache directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upstream-monitor-cache-*.tmp")
	if err != nil {
		return fmt.Errorf("create snapshot cache temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect snapshot cache: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write snapshot cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close snapshot cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace snapshot cache: %w", err)
	}
	return nil
}

func markStale(snapshot accountSnapshot, now time.Time) accountSnapshot {
	snapshot.Stale = true
	if snapshot.Error == nil {
		snapshot.Error = &snapshotError{Code: "STALE", Message: "using the last successful snapshot"}
	}
	if snapshot.CheckedAt.IsZero() {
		snapshot.CheckedAt = now
	}
	return snapshot
}
