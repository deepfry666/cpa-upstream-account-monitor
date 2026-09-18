package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationRestartAndRollbackBackupRemainConsistent(t *testing.T) {
	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Migration Relay\n"+
		"    base-url: https://relay.example/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: migration-key\n")
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()

	const legacyID = "config:openai-compatibility:0:0"
	const legacyPAT = "legacy-test-pat"
	key, err := loadOrCreateKey(cfg.PreferencesPath + ".key")
	if err != nil {
		t.Fatal(err)
	}
	encryptedPAT, err := encryptPreference(key, legacyID, legacyPAT)
	if err != nil {
		t.Fatal(err)
	}
	legacyPrefs := map[string]any{
		"revision":        1,
		"known":           map[string]bool{legacyID: true},
		"monitored":       map[string]bool{legacyID: true},
		"adapters":        map[string]string{legacyID: "newapi-usage"},
		"management_pats": map[string]string{legacyID: encryptedPAT},
		"names":           map[string]string{legacyID: "迁移测试"},
	}
	legacyPrefsRaw, err := json.Marshal(legacyPrefs)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(cfg.PreferencesPath, legacyPrefsRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	history := []accountSnapshot{
		{
			AccountID:   legacyID,
			AccountName: "迁移测试",
			Status:      statusOK,
			CheckedAt:   time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC),
		},
		{
			AccountID:   legacyID,
			AccountName: "迁移测试",
			Status:      statusWarning,
			CheckedAt:   time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC),
		},
	}
	legacyCacheRaw, err := json.Marshal(map[string]any{
		"entries": map[string]accountSnapshot{legacyID: history[0]},
		"history": map[string][]accountSnapshot{legacyID: history},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(cfg.CachePath, legacyCacheRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	backupDir := t.TempDir()
	backupPrefsPath := filepath.Join(backupDir, "upstream-monitor-preferences.json")
	backupKeyPath := backupPrefsPath + ".key"
	backupCachePath := filepath.Join(backupDir, "upstream-monitor-snapshots.json")
	for source, destination := range map[string]string{
		cfg.PreferencesPath:          backupPrefsPath,
		cfg.PreferencesPath + ".key": backupKeyPath,
		cfg.CachePath:                backupCachePath,
	} {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(destination, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	restart := func(paths pluginConfig) {
		t.Helper()
		state.prefs = newMonitorPrefs()
		state.store = newSnapshotStore()
		raw, err := json.Marshal(map[string]any{
			"source_config_path": paths.SourceConfigPath,
			"cache_path":         paths.CachePath,
			"preferences_path":   paths.PreferencesPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := configure(raw); err != nil {
			t.Fatal(err)
		}
	}

	restart(cfg)
	failWrites := true
	state.store.writeFile = func(path string, raw []byte, mode os.FileMode) error {
		if failWrites {
			return errors.New("simulated snapshot write failure")
		}
		return writeFileAtomic(path, raw, mode)
	}
	assigned, err := assignStableAccountIDs([]staticAccount{{
		candidate: credentialCandidate{
			LegacyID:  legacyID,
			AuthIndex: legacyID,
			Name:      "迁移测试",
			Provider:  "Migration Relay",
			BaseURL:   "https://relay.example/v1",
			Source:    "static",
		},
		storage: []byte(`{"api_key":"migration-key"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 1 {
		t.Fatalf("assigned accounts = %d, want 1", len(assigned))
	}
	stableID := assigned[0].candidate.AccountID
	if stableID == legacyID {
		t.Fatalf("legacy id was not replaced: %q", stableID)
	}
	if got := state.prefs.pat(stableID); got != legacyPAT {
		t.Fatalf("migrated PAT = %q, want test fixture value", got)
	}
	if _, ok := state.store.get(legacyID); ok {
		t.Fatal("legacy cache entry remained active after first startup")
	}
	if got := state.store.historyFor(stableID, 10); len(got) != 2 {
		t.Fatalf("first-start history length = %d, want 2", len(got))
	}
	if !state.store.hasPendingMigration() {
		t.Fatal("migration write failure was not retained for retry")
	}
	if err := state.store.migrationError(); err == nil {
		t.Fatal("migration write failure was not exposed independently")
	}
	failWrites = false
	if err := state.store.save(cfg.CachePath); err != nil {
		t.Fatalf("retry migrated snapshot persistence: %v", err)
	}
	if state.store.hasPendingMigration() {
		t.Fatal("successful retry did not clear the pending migration")
	}
	if err := state.store.migrationError(); err != nil {
		t.Fatalf("successful retry did not clear the migration warning: %v", err)
	}

	restart(cfg)
	assigned, err = assignStableAccountIDs([]staticAccount{{
		candidate: credentialCandidate{
			LegacyID:  legacyID,
			AuthIndex: legacyID,
			Name:      "迁移测试",
			Provider:  "Migration Relay",
			BaseURL:   "https://relay.example/v1",
			Source:    "static",
		},
		storage: []byte(`{"api_key":"migration-key"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := assigned[0].candidate.AccountID; got != stableID {
		t.Fatalf("second-start stable id = %q, want %q", got, stableID)
	}
	if got := state.prefs.pat(stableID); got != legacyPAT {
		t.Fatalf("second-start PAT = %q, want test fixture value", got)
	}
	if got := state.store.historyFor(stableID, 10); len(got) != 2 {
		t.Fatalf("second-start history length = %d, want 2", len(got))
	}

	backupPrefsRaw, err := os.ReadFile(backupPrefsPath)
	if err != nil {
		t.Fatal(err)
	}
	var backupPrefs persistedMonitorPrefs
	if err := json.Unmarshal(backupPrefsRaw, &backupPrefs); err != nil {
		t.Fatal(err)
	}
	backupKey, err := loadKey(backupKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptPreference(backupKey, legacyID, backupPrefs.PATs[legacyID])
	if err != nil {
		t.Fatal(err)
	}
	if plain != legacyPAT {
		t.Fatalf("rollback backup PAT = %q, want test fixture value", plain)
	}
	rollbackStore := newSnapshotStore()
	if err := rollbackStore.load(backupCachePath); err != nil {
		t.Fatal(err)
	}
	rollbackHistory := rollbackStore.historyFor(legacyID, 10)
	if len(rollbackHistory) != len(history) {
		t.Fatalf("rollback history = %#v, want %#v", rollbackHistory, history)
	}
	for index := range history {
		if rollbackHistory[index].AccountID != history[index].AccountID ||
			rollbackHistory[index].AccountName != history[index].AccountName ||
			rollbackHistory[index].Status != history[index].Status ||
			!rollbackHistory[index].CheckedAt.Equal(history[index].CheckedAt) {
			t.Fatalf("rollback history[%d] = %#v, want %#v", index, rollbackHistory[index], history[index])
		}
	}
}
