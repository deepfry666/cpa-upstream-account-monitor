package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestMonitorPrefsBatchPATActionsAreExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}

	if _, err := prefs.updateBatch([]providerPreferenceMutation{{
		ID:        "acct_a",
		Monitored: boolPtr(true),
		PATAction: patActionReplace,
		PAT:       "first-pat",
	}}); err != nil {
		t.Fatal(err)
	}
	if got := prefs.pat("acct_a"); got != "first-pat" {
		t.Fatalf("replaced PAT = %q, want first-pat", got)
	}

	if _, err := prefs.updateBatch([]providerPreferenceMutation{{
		ID:        "acct_a",
		Monitored: boolPtr(false),
		PATAction: patActionKeep,
	}}); err != nil {
		t.Fatal(err)
	}
	if got := prefs.pat("acct_a"); got != "first-pat" {
		t.Fatalf("keep action changed PAT to %q", got)
	}

	if _, err := prefs.updateBatch([]providerPreferenceMutation{{
		ID:        "acct_a",
		PATAction: patActionClear,
	}}); err != nil {
		t.Fatal(err)
	}
	if got := prefs.pat("acct_a"); got != "" {
		t.Fatalf("clear action left PAT %q", got)
	}
	if got := prefs.patStatus("acct_a"); got != patStatusMissing {
		t.Fatalf("cleared PAT status = %q, want %q", got, patStatusMissing)
	}
}

func TestMonitorPrefsBatchFailureDoesNotPartiallyCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	name := "original"
	if _, err := prefs.updateBatch([]providerPreferenceMutation{{
		ID:         "acct_a",
		Monitored:  boolPtr(false),
		CustomName: &name,
		PATAction:  patActionReplace,
		PAT:        "original-pat",
	}}); err != nil {
		t.Fatal(err)
	}
	before := clonePersistedPrefs(prefs.data)
	beforeRevision := prefs.revision()

	nextName := "must-not-commit"
	_, err := prefs.updateBatch([]providerPreferenceMutation{
		{ID: "acct_a", Monitored: boolPtr(true), CustomName: &nextName},
		{ID: "acct_b", PATAction: patActionClear, PAT: "ambiguous-replacement"},
	})
	if err == nil {
		t.Fatal("ambiguous PAT action was accepted")
	}
	after := clonePersistedPrefs(prefs.data)
	if prefs.revision() != beforeRevision {
		t.Fatalf("revision changed after rejected batch: got %d want %d", prefs.revision(), beforeRevision)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("preferences changed after rejected batch:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestMonitorPrefsConcurrentBatchesPreserveBothChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, mutation := range []providerPreferenceMutation{
		{ID: "acct_a", Monitored: boolPtr(true), PATAction: patActionReplace, PAT: "pat-a"},
		{ID: "acct_b", Monitored: boolPtr(true), PATAction: patActionReplace, PAT: "pat-b"},
	} {
		mutation := mutation
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := prefs.updateBatch([]providerPreferenceMutation{mutation})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.pat("acct_a"); got != "pat-a" {
		t.Fatalf("acct_a PAT = %q, want pat-a", got)
	}
	if got := reloaded.pat("acct_b"); got != "pat-b" {
		t.Fatalf("acct_b PAT = %q, want pat-b", got)
	}
	if got := reloaded.revision(); got != 2 {
		t.Fatalf("revision after concurrent batches = %d, want 2", got)
	}
}

func TestMonitorPrefsConcurrentCASRejectsStaleWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	baseRevision := prefs.revision()
	type result struct {
		id       string
		revision int64
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, mutation := range []providerPreferenceMutation{
		{ID: "acct_a", Monitored: boolPtr(true), PATAction: patActionReplace, PAT: "pat-a"},
		{ID: "acct_b", Monitored: boolPtr(true), PATAction: patActionReplace, PAT: "pat-b"},
	} {
		mutation := mutation
		go func() {
			<-start
			revision, err := prefs.updateBatchAtRevision(&baseRevision, []providerPreferenceMutation{mutation})
			results <- result{id: mutation.ID, revision: revision, err: err}
		}()
	}
	close(start)

	var succeeded, conflicted int
	var winningID string
	for range 2 {
		result := <-results
		if result.err == nil {
			succeeded++
			winningID = result.id
			if result.revision != baseRevision+1 {
				t.Fatalf("successful revision = %d, want %d", result.revision, baseRevision+1)
			}
			continue
		}
		var conflict *revisionConflictError
		if !errors.As(result.err, &conflict) {
			t.Fatalf("concurrent stale writer error = %T %v, want revision conflict", result.err, result.err)
		}
		conflicted++
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent CAS results: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	if got := prefs.revision(); got != baseRevision+1 {
		t.Fatalf("revision after CAS = %d, want %d", got, baseRevision+1)
	}
	if got := prefs.pat(winningID); got == "" {
		t.Fatalf("winning mutation %q was not persisted", winningID)
	}
	losingID := "acct_a"
	if winningID == losingID {
		losingID = "acct_b"
	}
	if got := prefs.pat(losingID); got != "" {
		t.Fatalf("losing mutation %q was applied: %q", losingID, got)
	}
}

func TestMonitorPrefsEncryptsAndReloadsPAT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	if err := prefs.update("account-1", boolPtr(true), "newapi-usage", "pat-secret-value", false, false, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || string(raw) == `{"known":{"account-1":true},"monitored":{"account-1":true},"adapters":{"account-1":"newapi-usage"},"management_pats":{"account-1":"pat-secret-value"}}` {
		t.Fatal("PAT was written as plaintext")
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.pat("account-1"); got != "pat-secret-value" {
		t.Fatalf("reloaded PAT = %q", got)
	}
	if err := reloaded.update("account-1", nil, "", "", true, false, nil); err != nil {
		t.Fatal(err)
	}
	if reloaded.pat("account-1") != "" || reloaded.patConfigured("account-1") {
		t.Fatal("PAT was not cleared")
	}
}

func TestMonitorPrefsStoresAndClearsCustomName(t *testing.T) {
	dir := t.TempDir()
	prefs := newMonitorPrefs()
	if err := prefs.configure(filepath.Join(dir, "prefs.json")); err != nil {
		t.Fatal(err)
	}
	name := "主力中转"
	if err := prefs.update("account-1", boolPtr(true), "", "", false, false, &name); err != nil {
		t.Fatal(err)
	}
	if got := prefs.name("account-1", "fallback"); got != name {
		t.Fatalf("custom name = %q, want %q", got, name)
	}
	clearName := ""
	if err := prefs.update("account-1", nil, "", "", false, false, &clearName); err != nil {
		t.Fatal(err)
	}
	if got := prefs.name("account-1", "fallback"); got != "fallback" {
		t.Fatalf("cleared custom name = %q, want fallback", got)
	}
}

func TestMonitorPrefsRejectsInvalidCustomName(t *testing.T) {
	prefs := newMonitorPrefs()
	tooLong := strings.Repeat("x", 81)
	if err := prefs.update("account-1", nil, "", "", false, false, &tooLong); err == nil {
		t.Fatal("too-long custom name was accepted")
	}
	control := "bad\nname"
	if err := prefs.update("account-1", nil, "", "", false, false, &control); err == nil {
		t.Fatal("control character custom name was accepted")
	}
}

func TestMonitorPrefsIdentityStableAcrossReorder(t *testing.T) {
	prefs := newMonitorPrefs()
	first := []credentialCandidate{
		{Source: "static", BaseURL: "https://relay.example/v1", Fingerprint: "fingerprint-a"},
		{Source: "static", BaseURL: "https://relay.example/v1", Fingerprint: "fingerprint-b"},
	}
	firstIDs, err := prefs.resolveIdentities(first)
	if err != nil {
		t.Fatal(err)
	}
	reordered := []credentialCandidate{first[1], first[0]}
	reorderedIDs, err := prefs.resolveIdentities(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if firstIDs[0] == firstIDs[1] {
		t.Fatalf("different credentials received the same id: %q", firstIDs[0])
	}
	if reorderedIDs[0] != firstIDs[1] || reorderedIDs[1] != firstIDs[0] {
		t.Fatalf("ids changed after reorder: first=%v reordered=%v", firstIDs, reorderedIDs)
	}
}

func TestMonitorPrefsIdentityWithoutStableBasisDoesNotMergeHostAccounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	if err := prefs.update("legacy-position", boolPtr(true), "newapi-usage", "legacy-pat", false, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".key"); err != nil {
		t.Fatal(err)
	}
	prefs = newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	candidates := []credentialCandidate{
		{LegacyID: "position-1", Source: "host", BaseURL: "https://relay.example/v1", AuthIndex: "position-1", AuthID: "account-a"},
		{LegacyID: "position-2", Source: "host", BaseURL: "https://relay.example/v1", AuthIndex: "position-2", AuthID: "account-b"},
	}
	firstIDs, err := prefs.resolveIdentities(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if firstIDs[0] == firstIDs[1] {
		t.Fatalf("host accounts without stable identity were merged: %q", firstIDs[0])
	}
	secondIDs, err := prefs.resolveIdentities(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if secondIDs[0] == secondIDs[1] {
		t.Fatalf("second resolution merged host accounts without stable identity: %q", secondIDs[0])
	}
	for _, secondID := range secondIDs {
		for _, firstID := range firstIDs {
			if secondID == firstID {
				t.Fatalf("host account without stable identity reused %q", secondID)
			}
		}
	}
}

func TestMonitorPrefsIdentityUsesHostSourceIDWithoutBaseURL(t *testing.T) {
	prefs := newMonitorPrefs()
	first := []credentialCandidate{{Source: "host", SourceID: "host-auth-1", BaseURL: "https://one.example/v1"}}
	second := []credentialCandidate{{Source: "host", SourceID: "host-auth-1", BaseURL: "https://two.example/v1"}}
	firstIDs, err := prefs.resolveIdentities(first)
	if err != nil {
		t.Fatal(err)
	}
	secondIDs, err := prefs.resolveIdentities(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstIDs[0] != secondIDs[0] {
		t.Fatalf("host source id changed with base URL: first=%q second=%q", firstIDs[0], secondIDs[0])
	}
}

func TestMonitorPrefsIdentitySurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	candidate := credentialCandidate{Source: "static", BaseURL: "https://relay.example/v1", Fingerprint: "fingerprint-a"}
	ids, err := prefs.resolveIdentities([]credentialCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	reloadedIDs, err := reloaded.resolveIdentities([]credentialCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if reloadedIDs[0] != ids[0] {
		t.Fatalf("identity after reload = %q, want %q", reloadedIDs[0], ids[0])
	}
}

func TestMonitorPrefsMigratesPATAADToStableIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	if err := prefs.update("legacy-position-1", boolPtr(true), "newapi-usage", "legacy-pat", false, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := prefs.migratePAT("legacy-position-1", "acct_stable"); err != nil {
		t.Fatal(err)
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.pat("acct_stable"); got != "legacy-pat" {
		t.Fatalf("migrated PAT = %q, want legacy-pat", got)
	}
	if got := reloaded.pat("legacy-position-1"); got != "" {
		t.Fatalf("legacy PAT remained readable under old id: %q", got)
	}
}

func TestMonitorPrefsMissingKeyDoesNotOverwriteCiphertext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	if err := prefs.update("acct_a", boolPtr(true), "newapi-usage", "secret-pat", false, false, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".key"); err != nil {
		t.Fatal(err)
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("preference ciphertext changed when the key was missing")
	}
	if _, err := os.Stat(path + ".key"); !os.IsNotExist(err) {
		t.Fatalf("missing key file was unexpectedly created: %v", err)
	}
	if got := reloaded.patStatus("acct_a"); got != "missing" {
		t.Fatalf("PAT status = %q, want missing", got)
	}
	if got := reloaded.pat("acct_a"); got != "" {
		t.Fatalf("PAT unexpectedly decrypted without a key: %q", got)
	}
}

func TestMonitorPrefsWrongKeyReportsDecryptError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	name := "保留的备注"
	if err := prefs.update("acct_a", boolPtr(true), "newapi-usage", "secret-pat", false, false, &name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".key", []byte(strings.Repeat("x", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.patStatus("acct_a"); got != "decrypt_error" {
		t.Fatalf("PAT status = %q, want decrypt_error", got)
	}
	if got := reloaded.name("acct_a", ""); got != name {
		t.Fatalf("non-PAT preference = %q, want %q", got, name)
	}
}

func TestMonitorPrefsMissingKeyStillSavesNonPATChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	if err := prefs.update("acct_a", boolPtr(true), "newapi-usage", "secret-pat", false, false, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".key"); err != nil {
		t.Fatal(err)
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	name := "保留备注"
	if err := reloaded.update("acct_a", boolPtr(false), "", "", false, false, &name); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oldData, newData persistedMonitorPrefs
	if err := json.Unmarshal(before, &oldData); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &newData); err != nil {
		t.Fatal(err)
	}
	if newData.PATs["acct_a"] != oldData.PATs["acct_a"] {
		t.Fatal("non-PAT save changed the existing PAT ciphertext")
	}
	if newData.Monitored["acct_a"] || newData.Names["acct_a"] != name {
		t.Fatalf("non-PAT update was not persisted: monitored=%v name=%q", newData.Monitored["acct_a"], newData.Names["acct_a"])
	}
}

func TestMonitorPrefsMigratesUniqueLegacyPositionID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	if err := prefs.update("config:openai-compatibility:0:0", boolPtr(true), "newapi-usage", "legacy-pat", false, false, nil); err != nil {
		t.Fatal(err)
	}

	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	candidate := credentialCandidate{
		LegacyID:    "config:openai-compatibility:0:0",
		AuthIndex:   "config:openai-compatibility:0:0",
		Source:      "static",
		BaseURL:     "https://relay.example/v1",
		Fingerprint: "fingerprint-a",
	}
	ids, err := reloaded.resolveIdentities([]credentialCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if ids[0] == candidate.LegacyID || !strings.HasPrefix(ids[0], "acct_") {
		t.Fatalf("legacy id was not replaced with a stable id: %q", ids[0])
	}
	if got := reloaded.pat(ids[0]); got != "legacy-pat" {
		t.Fatalf("migrated PAT = %q, want legacy-pat", got)
	}
	if got := reloaded.pat(candidate.LegacyID); got != "" {
		t.Fatalf("legacy PAT remained readable under the old id: %q", got)
	}
}

func TestMonitorPrefsAmbiguousLegacyIDRequiresRelink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	const legacyID = "config:openai-compatibility:0:0"
	if err := prefs.update(legacyID, boolPtr(true), "newapi-usage", "legacy-pat", false, false, nil); err != nil {
		t.Fatal(err)
	}

	ids, err := prefs.resolveIdentities([]credentialCandidate{
		{
			LegacyID:    legacyID,
			AuthIndex:   legacyID,
			Source:      "static",
			BaseURL:     "https://relay.example/v1",
			Fingerprint: "fingerprint-a",
		},
		{
			LegacyID:    legacyID,
			AuthIndex:   legacyID,
			Source:      "static",
			BaseURL:     "https://relay.example/v1",
			Fingerprint: "fingerprint-b",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if got := prefs.pat(id); got != "" {
			t.Fatalf("ambiguous legacy PAT was inherited by %q: %q", id, got)
		}
		if got := prefs.patStatus(id); got != patStatusRelink {
			t.Fatalf("PAT status for %q = %q, want %q", id, got, patStatusRelink)
		}
	}
}

func TestMonitorPrefsMissingLegacyIdentityBasisRequiresRelink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	const legacyID = "config:openai-compatibility:0:0"
	if err := prefs.update(legacyID, boolPtr(true), "newapi-usage", "legacy-pat", false, false, nil); err != nil {
		t.Fatal(err)
	}

	ids, err := prefs.resolveIdentities([]credentialCandidate{{
		LegacyID:  legacyID,
		AuthIndex: legacyID,
		Source:    "static",
		BaseURL:   "https://relay.example/v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := prefs.pat(ids[0]); got != "" {
		t.Fatalf("legacy PAT was inherited without an identity basis: %q", got)
	}
	if got := prefs.patStatus(ids[0]); got != patStatusRelink {
		t.Fatalf("PAT status = %q, want %q", got, patStatusRelink)
	}
}

func TestMonitorPrefsInvalidKeyStillLoadsNonPATState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	name := "仍可读取"
	if err := prefs.update("acct_a", boolPtr(true), "newapi-usage", "secret-pat", false, false, &name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".key", []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.name("acct_a", ""); got != name {
		t.Fatalf("non-PAT preference = %q, want %q", got, name)
	}
	if got := reloaded.patStatus("acct_a"); got != patStatusError {
		t.Fatalf("PAT status = %q, want %q", got, patStatusError)
	}
}

func boolPtr(value bool) *bool { return &value }
