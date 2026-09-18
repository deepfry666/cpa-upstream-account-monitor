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

func TestSnapshotStoreMigratesLegacyAccountAndHistory(t *testing.T) {
	store := newSnapshotStore()
	store.put(accountSnapshot{
		AccountID:   "legacy-position",
		AccountName: "Legacy",
		Status:      statusWarning,
		CheckedAt:   time.Unix(1, 0).UTC(),
	})
	store.put(accountSnapshot{
		AccountID:   "legacy-position",
		AccountName: "Legacy",
		Status:      statusOK,
		CheckedAt:   time.Unix(2, 0).UTC(),
	})
	store.put(accountSnapshot{
		AccountID:   "acct_stable",
		AccountName: "Legacy",
		Status:      statusCritical,
		CheckedAt:   time.Unix(3, 0).UTC(),
	})

	store.migrateAccount("legacy-position", "acct_stable")
	store.migrateAccount("legacy-position", "acct_stable")

	if _, ok := store.get("legacy-position"); ok {
		t.Fatal("legacy account remained active after migration")
	}
	entry, ok := store.get("acct_stable")
	if !ok || entry.CheckedAt.Unix() != 3 {
		t.Fatalf("migrated active entry = %#v, ok=%v", entry, ok)
	}
	history := store.historyFor("acct_stable", 10)
	if len(history) != 3 {
		t.Fatalf("migrated history length = %d, want 3", len(history))
	}
	for index, snapshot := range history {
		if snapshot.AccountID != "acct_stable" {
			t.Fatalf("history[%d] account id = %q, want acct_stable", index, snapshot.AccountID)
		}
	}
	if history[0].CheckedAt.Unix() != 3 || history[1].CheckedAt.Unix() != 2 || history[2].CheckedAt.Unix() != 1 {
		t.Fatalf("migrated history order = %#v", history)
	}
}

func TestSnapshotStoreMigrationKeepsNewerLegacyActiveSnapshot(t *testing.T) {
	store := newSnapshotStore()
	store.put(accountSnapshot{
		AccountID:   "legacy-position",
		AccountName: "Legacy",
		Status:      statusOK,
		CheckedAt:   time.Unix(4, 0).UTC(),
	})
	store.put(accountSnapshot{
		AccountID:   "acct_stable",
		AccountName: "Legacy",
		Status:      statusCritical,
		CheckedAt:   time.Unix(3, 0).UTC(),
	})

	store.migrateAccount("legacy-position", "acct_stable")

	if _, ok := store.get("legacy-position"); ok {
		t.Fatal("legacy account remained active after migration")
	}
	entry, ok := store.get("acct_stable")
	if !ok || entry.AccountID != "acct_stable" || entry.CheckedAt.Unix() != 4 || entry.Status != statusOK {
		t.Fatalf("migrated active entry = %#v, ok=%v", entry, ok)
	}
}

func TestSnapshotStoreMigrationCapsCombinedHistoryAtOneHundred(t *testing.T) {
	store := newSnapshotStore()
	legacyHistory := make([]accountSnapshot, 80)
	stableHistory := make([]accountSnapshot, 80)
	for index := range legacyHistory {
		legacyHistory[index] = accountSnapshot{
			AccountID: "legacy-position",
			Status:    statusWarning,
			CheckedAt: time.Unix(int64(100+index), 0).UTC(),
		}
	}
	for index := range stableHistory {
		stableHistory[index] = accountSnapshot{
			AccountID: "acct_stable",
			Status:    statusOK,
			CheckedAt: time.Unix(int64(index), 0).UTC(),
		}
	}
	store.mu.Lock()
	store.entries["legacy-position"] = legacyHistory[len(legacyHistory)-1]
	store.entries["acct_stable"] = stableHistory[len(stableHistory)-1]
	store.history["legacy-position"] = legacyHistory
	store.history["acct_stable"] = stableHistory
	store.mu.Unlock()

	if migrated := store.migrateAccount("legacy-position", "acct_stable"); !migrated {
		t.Fatal("legacy account was not migrated")
	}
	history := store.historyFor("acct_stable", 200)
	if len(history) != 100 {
		t.Fatalf("migrated history length = %d, want 100", len(history))
	}
	if history[0].CheckedAt.Unix() != 179 || history[99].CheckedAt.Unix() != 60 {
		t.Fatalf("migrated history bounds = %s..%s, want 179..60", history[0].CheckedAt, history[99].CheckedAt)
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
