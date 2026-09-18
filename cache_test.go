package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSnapshotStoreSavesVersionedCacheAndReloadsHistory(t *testing.T) {
	t.Setenv("UPSTREAM_MONITOR_TEST_TIME", "2026-09-19T00:00:00Z")
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	store := newSnapshotStore()
	first := accountSnapshot{AccountID: "acct_a", AccountName: "A", Status: statusOK, CheckedAt: time.Unix(1, 0).UTC()}
	second := accountSnapshot{AccountID: "acct_a", AccountName: "A", Status: statusWarning, CheckedAt: time.Unix(2, 0).UTC()}
	store.put(first)
	store.put(second)
	if err := store.save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		FormatVersion int `json:"format_version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	if header.FormatVersion != snapshotCacheFormatVersion {
		t.Fatalf("cache format version = %d, want %d", header.FormatVersion, snapshotCacheFormatVersion)
	}
	reloaded := newSnapshotStore()
	if err := reloaded.load(path); err != nil {
		t.Fatal(err)
	}
	entry, ok := reloaded.get("acct_a")
	if !ok || entry.CheckedAt.Unix() != 2 {
		t.Fatalf("reloaded entry = %#v, ok=%v", entry, ok)
	}
	history := reloaded.historyFor("acct_a", 10)
	if len(history) != 2 || history[0].CheckedAt.Unix() != 2 || history[1].CheckedAt.Unix() != 1 {
		t.Fatalf("reloaded history = %#v", history)
	}
}

func TestSnapshotStoreRejectsUnknownFutureVersionWithoutChangingMemory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	contents := `{"format_version":999,"entries":{"acct_new":{"id":"acct_new","checked_at":"2026-09-19T00:00:00Z"}},"history":{}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSnapshotStore()
	existing := accountSnapshot{AccountID: "acct_existing", CheckedAt: time.Unix(1, 0).UTC()}
	store.put(existing)
	if err := store.load(path); err == nil {
		t.Fatal("future cache version was accepted")
	}
	if got, ok := store.get("acct_existing"); !ok || got.AccountID != existing.AccountID {
		t.Fatalf("existing memory was changed after rejected load: %#v, ok=%v", got, ok)
	}
	if _, ok := store.get("acct_new"); ok {
		t.Fatal("future cache entries were loaded")
	}
}

func TestSnapshotStorePruneKeepsHistoryForRemovedAccount(t *testing.T) {
	store := newSnapshotStore()
	snapshot := accountSnapshot{AccountID: "acct_a", CheckedAt: time.Unix(1, 0).UTC()}
	store.put(snapshot)
	store.prune(map[string]struct{}{})
	if _, ok := store.get("acct_a"); ok {
		t.Fatal("pruned account remained active")
	}
	history := store.historyFor("acct_a", 10)
	if len(history) != 1 || history[0].AccountID != "acct_a" {
		t.Fatalf("pruned history = %#v, want archived snapshot", history)
	}
}

func TestSnapshotStoreLoadsNoVersionEntriesHistoryFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	contents := `{
  "entries": {
    "acct_a": {"id":"acct_a","name":"A","checked_at":"2026-09-19T00:00:00Z"}
  },
  "history": {
    "acct_a": [
      {"id":"acct_a","name":"A","checked_at":"2026-09-19T00:00:00Z"},
      {"id":"acct_a","name":"A","checked_at":"2026-09-18T00:00:00Z"}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSnapshotStore()
	if err := store.load(path); err != nil {
		t.Fatal(err)
	}
	entry, ok := store.get("acct_a")
	if !ok || entry.CheckedAt.UTC() != time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("entry = %#v, ok=%v", entry, ok)
	}
	history := store.historyFor("acct_a", 10)
	if len(history) != 2 || history[0].CheckedAt.UTC() != time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) || history[1].CheckedAt.UTC() != time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("history = %#v", history)
	}
}

func TestSnapshotStoreLoadsLegacyAccountMapFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	contents := `{"acct_a":{"id":"acct_a","name":"A","checked_at":"2026-09-19T00:00:00Z"}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSnapshotStore()
	if err := store.load(path); err != nil {
		t.Fatal(err)
	}
	entry, ok := store.get("acct_a")
	if !ok || entry.AccountName != "A" {
		t.Fatalf("entry = %#v, ok=%v", entry, ok)
	}
	history := store.historyFor("acct_a", 10)
	if len(history) != 1 || history[0].AccountID != "acct_a" {
		t.Fatalf("history = %#v", history)
	}
}

func TestSnapshotStoreTruncatedLoadPreservesFileAndMemory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	contents := []byte(`{"format_version":2,"entries":{"acct_a":`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSnapshotStore()
	existing := accountSnapshot{AccountID: "acct_existing", AccountName: "Existing", CheckedAt: time.Unix(1, 0).UTC()}
	store.put(existing)

	if err := store.load(path); err == nil {
		t.Fatal("truncated cache was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, contents) {
		t.Fatalf("truncated cache file changed:\nbefore=%q\nafter=%q", contents, after)
	}
	if got, ok := store.get("acct_existing"); !ok || !reflect.DeepEqual(got, existing) {
		t.Fatalf("memory changed after rejected load: %#v, ok=%v", got, ok)
	}
}

func TestSnapshotStoreVersionedSaveReloadRoundTripsExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	store := newSnapshotStore()
	first := accountSnapshot{
		AccountID:   "acct_a",
		AccountName: "A",
		Kind:        kindQuota,
		Status:      statusOK,
		CheckedAt:   time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC),
	}
	second := accountSnapshot{
		AccountID:   "acct_b",
		AccountName: "B",
		Kind:        kindBalance,
		Status:      statusWarning,
		CheckedAt:   time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC),
	}
	store.put(first)
	store.put(second)
	if err := store.save(path); err != nil {
		t.Fatal(err)
	}
	reloaded := newSnapshotStore()
	if err := reloaded.load(path); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []accountSnapshot{first, second} {
		got, ok := reloaded.get(snapshot.AccountID)
		if !ok || !reflect.DeepEqual(got, snapshot) {
			t.Fatalf("reloaded snapshot = %#v, want %#v", got, snapshot)
		}
	}
}

func TestSnapshotStoreSerializesConcurrentSaves(t *testing.T) {
	store := newSnapshotStore()
	store.put(accountSnapshot{AccountID: "acct_a", AccountName: "first", CheckedAt: time.Unix(1, 0).UTC()})
	entered := make(chan string, 2)
	release := make(chan struct{})
	store.writeFile = func(_ string, raw []byte, _ os.FileMode) error {
		var cache persistedSnapshotCache
		if err := json.Unmarshal(raw, &cache); err != nil {
			return err
		}
		entered <- cache.Entries["acct_a"].AccountName
		<-release
		return nil
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	path := filepath.Join(t.TempDir(), "cache.json")
	for range 2 {
		go func() {
			<-start
			results <- store.save(path)
		}()
	}
	close(start)

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first snapshot save did not reach the writer")
	}
	select {
	case <-entered:
		t.Fatal("snapshot saves entered the writer concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}
