package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const snapshotCacheFormatVersion = 2

type persistedSnapshotCache struct {
	FormatVersion int                          `json:"format_version"`
	Entries       map[string]accountSnapshot   `json:"entries"`
	History       map[string][]accountSnapshot `json:"history"`
}

type snapshotStore struct {
	mu        sync.RWMutex
	saveMu    sync.Mutex
	entries   map[string]accountSnapshot
	history   map[string][]accountSnapshot
	loadErr   error
	writeFile func(string, []byte, os.FileMode) error
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

func (s *snapshotStore) removeActive(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
}

func (s *snapshotStore) reapplyActive(mutate func(accountSnapshot) accountSnapshot) {
	if s == nil || mutate == nil {
		return
	}
	s.mu.Lock()
	for id, snapshot := range s.entries {
		s.entries[id] = mutate(snapshot)
	}
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
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s.setLoadError(nil)
		return nil
	}
	if err != nil {
		wrapped := fmt.Errorf("read snapshot cache: %w", err)
		s.setLoadError(wrapped)
		return wrapped
	}
	entries, history, err := decodeSnapshotCache(raw)
	if err != nil {
		wrapped := fmt.Errorf("decode snapshot cache: %w", err)
		s.setLoadError(wrapped)
		return wrapped
	}
	s.mu.Lock()
	s.entries = entries
	s.history = history
	s.loadErr = nil
	s.mu.Unlock()
	return nil
}

func (s *snapshotStore) setLoadError(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.loadErr = err
	s.mu.Unlock()
}

func (s *snapshotStore) loadError() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	err := s.loadErr
	s.mu.RUnlock()
	return err
}

func decodeSnapshotCache(raw []byte) (map[string]accountSnapshot, map[string][]accountSnapshot, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, nil, err
	}
	_, hasVersion := top["format_version"]
	_, hasEntries := top["entries"]
	_, hasHistory := top["history"]
	if hasVersion || hasEntries || hasHistory {
		var cached persistedSnapshotCache
		if err := json.Unmarshal(raw, &cached); err != nil {
			return nil, nil, err
		}
		if cached.FormatVersion > snapshotCacheFormatVersion || cached.FormatVersion < 0 {
			return nil, nil, fmt.Errorf("unsupported snapshot cache format %d", cached.FormatVersion)
		}
		if cached.Entries == nil {
			cached.Entries = map[string]accountSnapshot{}
		}
		if cached.History == nil {
			cached.History = map[string][]accountSnapshot{}
		}
		entries, err := validateSnapshotEntries(cached.Entries)
		if err != nil {
			return nil, nil, err
		}
		history, err := validateSnapshotHistory(cached.History)
		if err != nil {
			return nil, nil, err
		}
		return entries, history, nil
	}

	var legacy map[string]accountSnapshot
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, nil, err
	}
	entries, err := validateSnapshotEntries(legacy)
	if err != nil {
		return nil, nil, err
	}
	history := make(map[string][]accountSnapshot, len(entries))
	for id, snapshot := range entries {
		history[id] = []accountSnapshot{snapshot}
	}
	return entries, history, nil
}

func validateSnapshotEntries(values map[string]accountSnapshot) (map[string]accountSnapshot, error) {
	entries := make(map[string]accountSnapshot, len(values))
	for id, snapshot := range values {
		normalized, err := normalizeCachedSnapshot(id, snapshot)
		if err != nil {
			return nil, err
		}
		entries[id] = normalized
	}
	return entries, nil
}

func validateSnapshotHistory(values map[string][]accountSnapshot) (map[string][]accountSnapshot, error) {
	history := make(map[string][]accountSnapshot, len(values))
	for id, snapshots := range values {
		if len(snapshots) > 100 {
			snapshots = snapshots[:100]
		}
		normalized := make([]accountSnapshot, 0, len(snapshots))
		for _, snapshot := range snapshots {
			value, err := normalizeCachedSnapshot(id, snapshot)
			if err != nil {
				return nil, err
			}
			normalized = append(normalized, value)
		}
		history[id] = normalized
	}
	return history, nil
}

func normalizeCachedSnapshot(key string, snapshot accountSnapshot) (accountSnapshot, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return accountSnapshot{}, fmt.Errorf("snapshot cache contains an empty account id")
	}
	if snapshot.AccountID != "" && snapshot.AccountID != key {
		return accountSnapshot{}, fmt.Errorf("snapshot %q contains mismatched account id %q", key, snapshot.AccountID)
	}
	snapshot.AccountID = key
	if snapshot.CheckedAt.IsZero() && snapshot.LastSuccessAt == nil {
		return accountSnapshot{}, fmt.Errorf("snapshot %q contains no valid timestamp", key)
	}
	if snapshot.Kind == "" {
		snapshot.Kind = inferAccountKind(snapshot)
	}
	return snapshot, nil
}

func (s *snapshotStore) prune(keep map[string]struct{}) {
	if s == nil {
		return
	}
	s.mu.Lock()
	for id := range s.entries {
		if _, ok := keep[id]; !ok {
			delete(s.entries, id)
		}
	}
	s.mu.Unlock()
}

func (s *snapshotStore) save(path string) error {
	if s == nil || path == "" {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := s.loadError(); err != nil {
		return fmt.Errorf("refusing to overwrite unreadable snapshot cache: %w", err)
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
	raw, err := json.Marshal(persistedSnapshotCache{
		FormatVersion: snapshotCacheFormatVersion,
		Entries:       entries,
		History:       history,
	})
	if err != nil {
		return fmt.Errorf("encode snapshot cache: %w", err)
	}
	writeFile := s.writeFile
	if writeFile == nil {
		writeFile = writeFileAtomic
	}
	if err := writeFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("persist snapshot cache: %w", err)
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
