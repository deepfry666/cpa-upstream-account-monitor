package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func useTestPluginState(t *testing.T, sourceConfig string) {
	t.Helper()
	previous := state
	state = &pluginState{
		store:         newSnapshotStore(),
		prefs:         newMonitorPrefs(),
		schemaVersion: 5,
	}
	t.Cleanup(func() {
		stopBackgroundScheduler()
		waitForRefreshJobs(t, state.refreshJobs())
		state = previous
	})

	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(sourcePath, []byte(sourceConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"source_config_path": sourcePath,
		"cache_path":         filepath.Join(dir, "cache.json"),
		"preferences_path":   filepath.Join(dir, "prefs.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
}

func managementResponseBody(t *testing.T, raw []byte) (int, []byte) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("management response failed: %+v", env.Error)
	}
	var response pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Body
}

func providerIDForTest(t *testing.T) string {
	t.Helper()
	views, err := discoverProviderViews("")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("providers = %d, want 1", len(views))
	}
	return views[0].ID
}

func TestProviderViewsPreserveFirstSeenOrder(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Zulu Relay
    base-url: https://zulu.example/v1
    api-key-entries:
      - api-key: zulu-key
  - name: Alpha Relay
    base-url: https://alpha.example/v1
    api-key-entries:
      - api-key: alpha-key
`)

	views, err := discoverProviderViews("")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("providers = %d, want 2", len(views))
	}
	if views[0].Name != "Zulu Relay" || views[1].Name != "Alpha Relay" {
		t.Fatalf("provider order = [%q, %q], want first-seen order", views[0].Name, views[1].Name)
	}
}

func TestProviderViewsIncludeFirstSeenTime(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Timed Relay
    base-url: https://timed.example/v1
    api-key-entries:
      - api-key: timed-key
`)

	views, err := discoverProviderViews("")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("providers = %d, want 1", len(views))
	}
	if views[0].FirstSeenAt == nil || views[0].FirstSeenAt.IsZero() {
		t.Fatal("provider view omitted its persistent first-seen time")
	}
}

func TestMonitorUIStateKeepsLastDirectoryWhenSyncFails(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Resilient Relay
    base-url: https://resilient.example/v1
    api-key-entries:
      - api-key: resilient-key
`)
	first, err := buildMonitorUIState("")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Providers) != 1 {
		t.Fatalf("initial providers = %d, want 1", len(first.Providers))
	}
	if first.DirectorySync.Status != "ok" || first.DirectorySync.LastSuccessAt == nil {
		t.Fatalf("initial directory sync = %+v", first.DirectorySync)
	}
	state.mu.RLock()
	sourcePath := state.cfg.SourceConfigPath
	state.mu.RUnlock()
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}

	second, err := buildMonitorUIState("")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Providers) != 1 || second.Providers[0].ID != first.Providers[0].ID {
		t.Fatalf("providers after failed sync = %+v", second.Providers)
	}
	if second.DirectorySync.Status != "error" || second.DirectorySync.Error == "" {
		t.Fatalf("failed directory sync = %+v", second.DirectorySync)
	}
	if second.DirectorySync.LastSuccessAt == nil || !second.DirectorySync.LastSuccessAt.Equal(*first.DirectorySync.LastSuccessAt) {
		t.Fatalf("last successful directory time was not retained: %+v", second.DirectorySync)
	}
}

func TestNewlyDiscoveredProviderIsNotMonitoredByDefault(t *testing.T) {
	var hits atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"limits":[]}}`))
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: newapi\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: new-key\n")
	views, err := discoverProviderViews("")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("providers = %d, want 1", len(views))
	}
	if views[0].Monitored {
		t.Fatal("newly discovered provider was monitored by default")
	}
	if err := refreshDiscoveredAccounts("", false); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("unmonitored new provider issued %d upstream requests, want 0", got)
	}
}

func TestProviderConfigRouteAtomicallySavesPATWithoutEcho(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Test Relay
    base-url: https://relay.example/v1
    api-key-entries:
      - api-key: rk-secret
`)
	id := providerIDForTest(t)
	request := providerMutationRequest{
		BaseRevision: int64Ptr(state.prefs.revision()),
		Providers: []providerMutation{{
			ID:            id,
			CustomName:    stringPtr("主力中转"),
			Monitored:     boolPtr(true),
			Adapter:       "newapi-usage",
			PATAction:     patActionReplace,
			ManagementPAT: "management-secret",
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/upstream-monitor/providers",
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if strings.Contains(string(responseBody), "management-secret") {
		t.Fatal("provider response echoed the PAT")
	}
	if got := state.prefs.pat(id); got != "management-secret" {
		t.Fatalf("saved PAT = %q", got)
	}
	var view monitorUIState
	if err := json.Unmarshal(responseBody, &view); err != nil {
		t.Fatal(err)
	}
	if view.Revision != state.prefs.revision() || view.Revision <= 0 {
		t.Fatalf("response revision = %d, current = %d", view.Revision, state.prefs.revision())
	}
}

func TestProviderConfigRouteAtomicallySavesAccountCashThreshold(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Threshold Relay
    base-url: https://relay.example/v1
    api-key-entries:
      - api-key: rk-threshold
`)
	id := providerIDForTest(t)
	request := providerMutationRequest{
		BaseRevision: int64Ptr(state.prefs.revision()),
		Providers: []providerMutation{{
			ID:            id,
			CustomName:    stringPtr("阈值账户"),
			CashThreshold: &cashThreshold{Warning: "10", Critical: "1"},
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/upstream-monitor/providers",
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	var view monitorUIState
	if err := json.Unmarshal(responseBody, &view); err != nil {
		t.Fatal(err)
	}
	if got := view.Config.CashByAccount[id]; got != (cashThreshold{Warning: "10", Critical: "1"}) {
		t.Fatalf("account threshold = %+v", got)
	}
	if got := state.prefs.thresholds(thresholdConfig{}).CashByAccount[id]; got.Warning != "10" || got.Critical != "1" {
		t.Fatalf("persisted account threshold = %+v", got)
	}
}

func TestProviderConfigRouteNoteOnlyChangeDoesNotRefresh(t *testing.T) {
	var hits atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"limits":[]}}`))
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Note Only Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: note-only-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "newapi-usage"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state.store.put(accountSnapshot{
		AccountID:      id,
		AccountName:    "Note Only Relay",
		AdapterID:      "newapi-usage",
		Status:         statusOK,
		Kind:           kindQuota,
		CheckedAt:      now,
		LastAttemptAt:  now,
		LastSuccessAt:  &now,
		ConfigRevision: state.prefs.queryRevision(id),
	})
	queryRevision := state.prefs.queryRevision(id)

	request := providerMutationRequest{
		BaseRevision: int64Ptr(state.prefs.revision()),
		Providers: []providerMutation{{
			ID:         id,
			CustomName: stringPtr("仅修改显示名称"),
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{Method: http.MethodPut, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if got := state.prefs.queryRevision(id); got != queryRevision {
		t.Fatalf("query revision = %d, want %d", got, queryRevision)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("note-only change issued %d upstream requests, want 0", got)
	}
	var view monitorUIState
	if err := json.Unmarshal(responseBody, &view); err != nil {
		t.Fatal(err)
	}
	if view.RefreshJob != nil {
		t.Fatalf("note-only change queued refresh job %+v", view.RefreshJob)
	}
}

func TestProviderConfigRouteReturnsTrackedRefreshJob(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"limits":[]}}`))
	}))
	defer relay.Close()
	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Tracked Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: tracked-key\n")
	id := providerIDForTest(t)
	request := providerMutationRequest{
		BaseRevision: int64Ptr(state.prefs.revision()),
		Providers: []providerMutation{{
			ID:            id,
			Monitored:     boolPtr(true),
			Adapter:       "newapi-usage",
			PATAction:     patActionReplace,
			ManagementPAT: "tracked-pat",
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{Method: http.MethodPut, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	var view monitorUIState
	if err := json.Unmarshal(responseBody, &view); err != nil {
		t.Fatal(err)
	}
	if view.RefreshJob == nil || view.RefreshJob.ID == "" {
		t.Fatal("provider save did not return a tracked refresh job")
	}
	if view.RefreshJob.Total != 1 || len(view.RefreshJob.AccountIDs) != 1 || view.RefreshJob.AccountIDs[0] != id {
		t.Fatalf("refresh job = %+v", view.RefreshJob)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, ok := state.refreshJobs().get(view.RefreshJob.ID)
		if !ok {
			t.Fatalf("refresh job %q disappeared", view.RefreshJob.ID)
		}
		if job.Status == "completed" || job.Status == "failed" {
			if job.Status != "completed" {
				t.Fatalf("refresh job failed: %+v", job)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh job did not finish: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMonitorSettingsThresholdChangeReappliesSnapshotWithoutRefresh(t *testing.T) {
	var hits atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"limits":[]}}`))
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Threshold Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: threshold-key\n")
	id := providerIDForTest(t)
	state.cfg.Thresholds = thresholdConfig{WarningPercent: 5, CriticalPercent: 2}
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "newapi-usage"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	remaining := 0.15
	state.store.put(accountSnapshot{
		AccountID:      id,
		AccountName:    "Threshold Relay",
		AdapterID:      "newapi-usage",
		Status:         statusOK,
		Kind:           kindPeriodQuota,
		Windows:        []quotaWindow{{Name: "weekly", RemainingFraction: &remaining}},
		CheckedAt:      now,
		LastAttemptAt:  now,
		LastSuccessAt:  &now,
		ConfigRevision: state.prefs.queryRevision(id),
	})
	queryRevision := state.prefs.queryRevision(id)
	body := []byte(`{"base_revision":` + fmt.Sprint(state.prefs.revision()) + `,"warning_percent":20,"critical_percent":10}`)

	raw, err := handleConfigRoute(managementRequest{Method: http.MethodPut, Path: "/v0/management/upstream-monitor/config", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	var view monitorUIState
	if err := json.Unmarshal(responseBody, &view); err != nil {
		t.Fatal(err)
	}
	if view.Config.WarningPercent != 20 || view.Config.CriticalPercent != 10 {
		t.Fatalf("saved thresholds = %.1f/%.1f, want 20/10", view.Config.WarningPercent, view.Config.CriticalPercent)
	}
	if len(view.Report.Accounts) != 1 || view.Report.Accounts[0].Status != statusWarning {
		t.Fatalf("recomputed report = %+v, want warning account", view.Report.Accounts)
	}
	if got := state.prefs.queryRevision(id); got != queryRevision {
		t.Fatalf("threshold change updated query revision: got %d want %d", got, queryRevision)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("threshold change issued %d upstream requests, want 0", got)
	}
}

func TestMonitorSettingsSurviveRestart(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"limits":[]}}`))
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Restart Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: restart-key\n")
	id := providerIDForTest(t)
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "newapi-usage"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	remaining := 0.15
	state.store.put(accountSnapshot{
		AccountID:      id,
		AccountName:    "Restart Relay",
		AdapterID:      "newapi-usage",
		Status:         statusOK,
		Kind:           kindPeriodQuota,
		Windows:        []quotaWindow{{Name: "weekly", RemainingFraction: &remaining}},
		CheckedAt:      now,
		LastAttemptAt:  now,
		LastSuccessAt:  &now,
		ConfigRevision: state.prefs.queryRevision(id),
	})
	body := []byte(`{"base_revision":` + fmt.Sprint(state.prefs.revision()) + `,"warning_percent":20,"critical_percent":10}`)
	raw, err := handleConfigRoute(managementRequest{Method: http.MethodPut, Path: "/v0/management/upstream-monitor/config", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", status, responseBody)
	}

	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	state = &pluginState{
		store:         newSnapshotStore(),
		prefs:         newMonitorPrefs(),
		schemaVersion: 5,
	}
	t.Cleanup(stopBackgroundScheduler)
	restartConfig, err := json.Marshal(map[string]any{
		"source_config_path": cfg.SourceConfigPath,
		"cache_path":         cfg.CachePath,
		"preferences_path":   cfg.PreferencesPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(restartConfig); err != nil {
		t.Fatal(err)
	}

	view, err := buildMonitorUIState("")
	if err != nil {
		t.Fatal(err)
	}
	if view.Config.WarningPercent != 20 || view.Config.CriticalPercent != 10 {
		t.Fatalf("restarted thresholds = %.1f/%.1f, want 20/10", view.Config.WarningPercent, view.Config.CriticalPercent)
	}
	if len(view.Report.Accounts) != 1 || view.Report.Accounts[0].Status != statusWarning {
		t.Fatalf("restarted report = %+v, want warning account", view.Report.Accounts)
	}
}

func TestInvalidReconfigureKeepsRunningSchedulerAndPublishedConfig(t *testing.T) {
	useTestPluginState(t, "")
	state.mu.Lock()
	state.cfg.Enabled = true
	state.cfg.SyncIntervalSeconds = 3600
	state.mu.Unlock()
	startBackgroundScheduler()
	t.Cleanup(stopBackgroundScheduler)
	if !backgroundSchedulerIsRunning() {
		t.Fatal("background scheduler did not start")
	}

	err := configure([]byte(`enabled: true
thresholds:
  cash_by_currency:
    CNY:
      warning: "1"
      critical: "2"
`))
	if err == nil {
		t.Fatal("invalid cash thresholds were accepted")
	}
	state.mu.RLock()
	enabled := state.cfg.Enabled
	state.mu.RUnlock()
	if !enabled {
		t.Fatal("invalid reconfiguration changed the published config")
	}
	if !backgroundSchedulerIsRunning() {
		t.Fatal("invalid reconfiguration stopped the existing scheduler")
	}
}

func TestMonitorSettingsRouteSavesCashThresholds(t *testing.T) {
	useTestPluginState(t, "")
	body := []byte(`{
		"base_revision": ` + fmt.Sprint(state.prefs.revision()) + `,
		"warning_percent": 20,
		"critical_percent": 10,
		"cash_by_currency": {"CNY":{"warning":"10","critical":"1"}},
		"cash_by_account": {"acct-premium":{"warning":"100","critical":"50"}}
	}`)
	raw, err := handleConfigRoute(managementRequest{Method: http.MethodPut, Path: "/v0/management/upstream-monitor/config", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", status, responseBody)
	}
	var view monitorUIState
	if err := json.Unmarshal(responseBody, &view); err != nil {
		t.Fatal(err)
	}
	if got := view.Config.CashByCurrency["CNY"]; got != (cashThreshold{Warning: "10", Critical: "1"}) {
		t.Fatalf("currency threshold = %+v", got)
	}
	if got := view.Config.CashByAccount["acct-premium"]; got != (cashThreshold{Warning: "100", Critical: "50"}) {
		t.Fatalf("account threshold = %+v", got)
	}
	saved := state.prefs.thresholds(thresholdConfig{})
	if saved.CashByCurrency["CNY"].Critical != "1" || saved.CashByAccount["acct-premium"].Warning != "100" {
		t.Fatalf("persisted cash thresholds = %+v", saved)
	}
}

func TestCleanupInvalidatesInFlightQueryResults(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Cleanup Relay
    base-url: https://cleanup.example/v1
    api-key-entries:
      - api-key: cleanup-key
`)
	id := providerIDForTest(t)
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "newapi-usage"}}); err != nil {
		t.Fatal(err)
	}
	inFlightRevision := state.prefs.queryRevision(id)
	if !state.prefs.canCommitQueryResult(id, inFlightRevision) {
		t.Fatal("test setup did not produce a committable query revision")
	}

	body := []byte(`{"ids":[` + fmt.Sprintf("%q", id) + `]}`)
	raw, err := handleCleanupRoute(managementRequest{Method: http.MethodPost, Path: "/v0/management/upstream-monitor/cleanup", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if got := state.prefs.queryRevision(id); got == inFlightRevision {
		t.Fatalf("cleanup did not invalidate query revision %d", inFlightRevision)
	}
	if state.prefs.canCommitQueryResult(id, inFlightRevision) {
		t.Fatal("a request started before cleanup can still commit its result")
	}
}

func TestProviderConfigRouteRejectsStaleRevisionWithoutChangingState(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Test Relay
    base-url: https://relay.example/v1
    api-key-entries:
      - api-key: rk-secret
`)
	id := providerIDForTest(t)
	revision := state.prefs.revision()
	request := providerMutationRequest{
		BaseRevision: int64Ptr(revision - 1),
		Providers: []providerMutation{{
			ID:         id,
			CustomName: stringPtr("stale change"),
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{Method: http.MethodPut, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if got := state.prefs.revision(); got != revision {
		t.Fatalf("revision changed on conflict: got %d want %d", got, revision)
	}
	if got := state.prefs.name(id, "fallback"); got != "fallback" {
		t.Fatalf("stale mutation changed name to %q", got)
	}
}

func TestProviderConfigRouteRejectsInvalidBatchWithoutPartialCommit(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Test Relay
    base-url: https://relay.example/v1
    api-key-entries:
      - api-key: rk-secret
`)
	id := providerIDForTest(t)
	revision := state.prefs.revision()
	request := providerMutationRequest{
		BaseRevision: int64Ptr(revision),
		Providers: []providerMutation{
			{ID: id, CustomName: stringPtr("must not commit")},
			{ID: id, PATAction: patActionClear, ManagementPAT: "ambiguous"},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{Method: http.MethodPut, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if got := state.prefs.revision(); got != revision {
		t.Fatalf("revision changed on rejected batch: got %d want %d", got, revision)
	}
	if got := state.prefs.name(id, "fallback"); got != "fallback" {
		t.Fatalf("rejected batch changed name to %q", got)
	}
}

func TestProviderConfigRouteReportsSnapshotPersistenceWarning(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Test Relay
    base-url: https://relay.example/v1
    api-key-entries:
      - api-key: rk-secret
`)
	id := providerIDForTest(t)
	cachePath := filepath.Join(t.TempDir(), "cache-as-directory")
	if err := os.MkdirAll(cachePath, 0o700); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.cfg.CachePath = cachePath
	state.mu.Unlock()

	request := providerMutationRequest{
		BaseRevision: int64Ptr(state.prefs.revision()),
		Providers: []providerMutation{{
			ID:         id,
			CustomName: stringPtr("配置已保存"),
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{Method: http.MethodPut, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if got := state.prefs.name(id, ""); got != "配置已保存" {
		t.Fatalf("preference was not committed: %q", got)
	}
	var view monitorUIState
	if err := json.Unmarshal(responseBody, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Warnings) == 0 || !strings.Contains(view.Warnings[0], "快照") {
		t.Fatalf("snapshot persistence warning = %#v", view.Warnings)
	}
}

func TestProviderConfigRouteReportsPreferencePersistenceFailure(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Test Relay
    base-url: https://relay.example/v1
    api-key-entries:
      - api-key: rk-secret
`)
	id := providerIDForTest(t)
	blockingParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingParent, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	state.prefs.mu.Lock()
	state.prefs.path = filepath.Join(blockingParent, "prefs.json")
	state.prefs.mu.Unlock()

	request := providerMutationRequest{
		BaseRevision: int64Ptr(state.prefs.revision()),
		Providers: []providerMutation{{
			ID:         id,
			CustomName: stringPtr("不能保存"),
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{Method: http.MethodPut, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if !strings.Contains(string(responseBody), "persist monitor preferences") {
		t.Fatalf("persistence error was not retained: %s", responseBody)
	}
}

func TestRefreshRouteWithAccountIDsSkipsOtherAccounts(t *testing.T) {
	var relayAHits atomic.Int32
	relayA := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		relayAHits.Add(1)
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
	}))
	defer relayA.Close()
	var relayBHits atomic.Int32
	relayB := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		relayBHits.Add(1)
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
	}))
	defer relayB.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Relay A\n"+
		"    base-url: "+relayA.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: key-a\n"+
		"  - name: Relay B\n"+
		"    base-url: "+relayB.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: key-b\n")
	views, err := discoverProviderViews("")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("providers = %d, want 2", len(views))
	}
	var accountA, accountB string
	for _, view := range views {
		switch view.Name {
		case "Relay A":
			accountA = view.ID
		case "Relay B":
			accountB = view.ID
		}
	}
	if accountA == "" || accountB == "" {
		t.Fatalf("provider ids not found: A=%q B=%q", accountA, accountB)
	}
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{
		accountA: {AllowInternalHTTP: true},
		accountB: {AllowInternalHTTP: true},
	}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{
		{ID: accountA, Monitored: boolPtr(true), Adapter: "newapi-usage"},
		{ID: accountB, Monitored: boolPtr(true), Adapter: "newapi-usage"},
	}); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{
		"account_ids": []string{accountA},
		"force":       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	managementRaw, err := json.Marshal(managementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/upstream-monitor/refresh",
		Body:   body,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleManagement(managementRaw)
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if relayAHits.Load() == 0 {
		snapshot, ok := state.store.get(accountA)
		t.Fatalf("selected account was not refreshed; snapshot=%+v present=%v", snapshot, ok)
	}
	if relayBHits.Load() != 0 {
		t.Fatalf("unselected account received %d requests", relayBHits.Load())
	}
}

func TestRefreshRouteAsyncReturnsTrackableJob(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Async Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: key-a\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "newapi-usage"}}); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(accountRefreshRequest{AccountIDs: []string{id}, Force: true, Async: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleManagement(mustJSON(managementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/upstream-monitor/refresh",
		Body:   body,
	}))
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	var started accountRefreshJob
	if err := json.Unmarshal(responseBody, &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Status == "" {
		t.Fatalf("invalid refresh job: %+v", started)
	}

	statusRaw, err := handleManagement(mustJSON(managementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/upstream-monitor/refresh",
		Query:  map[string][]string{"job_id": {started.ID}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	statusCode, statusBody := managementResponseBody(t, statusRaw)
	if statusCode != http.StatusOK {
		t.Fatalf("status lookup = %d, body = %s", statusCode, statusBody)
	}
	var job accountRefreshJob
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := json.Unmarshal(statusBody, &job); err != nil {
			t.Fatal(err)
		}
		if job.ID != started.ID {
			t.Fatalf("job id = %q, want %q", job.ID, started.ID)
		}
		if job.Status == "completed" || job.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh job did not reach a terminal state: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
		statusRaw, err = handleManagement(mustJSON(managementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/upstream-monitor/refresh",
			Query:  map[string][]string{"job_id": {started.ID}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		statusCode, statusBody = managementResponseBody(t, statusRaw)
		if statusCode != http.StatusOK {
			t.Fatalf("status lookup = %d, body = %s", statusCode, statusBody)
		}
	}
}

func TestRefreshRouteAsyncReportsPerAccountResult(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"usage":100,"currentValue":50,"remaining":50,"percentage":50}]}}`))
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Result Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: result-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "zai-usage"}}); err != nil {
		t.Fatal(err)
	}
	body := mustJSON(accountRefreshRequest{AccountIDs: []string{id}, Force: true, Async: true})
	raw, err := handleManagement(mustJSON(managementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/upstream-monitor/refresh",
		Body:   body,
	}))
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	var started accountRefreshJob
	if err := json.Unmarshal(responseBody, &started); err != nil {
		t.Fatal(err)
	}

	var job accountRefreshJob
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err = handleManagement(mustJSON(managementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/upstream-monitor/refresh",
			Query:  map[string][]string{"job_id": {started.ID}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		status, responseBody = managementResponseBody(t, raw)
		if status != http.StatusOK {
			t.Fatalf("status lookup = %d, body = %s", status, responseBody)
		}
		if err := json.Unmarshal(responseBody, &job); err != nil {
			t.Fatal(err)
		}
		if job.Status == "completed" || job.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh job did not finish: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(job.Results) != 1 {
		t.Fatalf("refresh results = %+v, want one account result", job.Results)
	}
	if job.Results[0].AccountID != id || job.Results[0].Status != "success" {
		t.Fatalf("refresh result = %+v, want account success", job.Results[0])
	}
}

func TestRefreshRouteAsyncReportsSkippedFreshAccount(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "fresh snapshot should skip upstream", http.StatusInternalServerError)
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Skipped Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: skipped-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.CacheTTLSeconds = 300
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "zai-usage"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state.store.put(accountSnapshot{
		AccountID:      id,
		AccountName:    "Skipped Relay",
		AdapterID:      "zai-usage",
		Status:         statusOK,
		Kind:           kindPeriodQuota,
		ConfigRevision: state.prefs.queryRevision(id),
		CheckedAt:      now,
		LastAttemptAt:  now,
		LastSuccessAt:  &now,
	})

	raw, err := handleManagement(mustJSON(managementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/upstream-monitor/refresh",
		Body:   mustJSON(accountRefreshRequest{AccountIDs: []string{id}, Async: true}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	var started accountRefreshJob
	if err := json.Unmarshal(responseBody, &started); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err = handleManagement(mustJSON(managementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/upstream-monitor/refresh",
			Query:  map[string][]string{"job_id": {started.ID}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		status, responseBody = managementResponseBody(t, raw)
		if status != http.StatusOK {
			t.Fatalf("status lookup = %d, body = %s", status, responseBody)
		}
		var job accountRefreshJob
		if err := json.Unmarshal(responseBody, &job); err != nil {
			t.Fatal(err)
		}
		if job.Status == "completed" || job.Status == "failed" {
			if len(job.Results) != 1 || job.Results[0].Status != "skipped" || job.Skipped != 1 {
				t.Fatalf("skipped result = %+v", job)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh job did not finish: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRefreshRouteRejectsUnknownScope(t *testing.T) {
	useTestPluginState(t, "")
	raw, err := handleManagement(mustJSON(managementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/upstream-monitor/refresh",
		Body:   []byte(`{"scope":"selected","account_ids":["acct_1"],"force":true}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if !strings.Contains(string(responseBody), "scope") {
		t.Fatalf("error = %s, want scope validation message", responseBody)
	}
}

func TestRefreshRouteRejectsUnknownAccountBeforeQueueing(t *testing.T) {
	useTestPluginState(t, "")
	raw, err := handleManagement(mustJSON(managementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/upstream-monitor/refresh",
		Body:   []byte(`{"account_ids":["acct_missing"],"force":true,"async":true}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	status, responseBody := managementResponseBody(t, raw)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	if !strings.Contains(string(responseBody), "account") {
		t.Fatalf("error = %s, want unknown account message", responseBody)
	}
}

func TestRefreshAccountsEnforcesAccountBudget(t *testing.T) {
	var hits atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(600 * time.Millisecond):
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{}`))
		case <-request.Context().Done():
			return
		}
	}))
	defer relay.Close()

	useTestPluginState(t, fmt.Sprintf("openai-compatibility:\n  - name: Budget Relay\n    base-url: %s/v1\n    api-key-entries:\n      - api-key: budget-key\n", relay.URL))
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.AccountTimeoutSecond = 1
	state.cfg.RequestTimeoutSecond = 2
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{
		ID:        id,
		Monitored: boolPtr(true),
		Adapter:   "newapi-usage",
		PATAction: patActionReplace,
		PAT:       "management-key",
	}}); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if err := refreshAccounts("", true, []string{id}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 1600*time.Millisecond {
		t.Fatalf("account refresh took %s without enforcing its total budget", elapsed)
	}
	if got := hits.Load(); got >= 3 {
		t.Fatalf("account refresh issued %d sequential requests after its total budget expired", got)
	}
}

func TestRefreshAccountsDoesNotSerializeDifferentAccounts(t *testing.T) {
	accountAStarted := make(chan struct{}, 1)
	relayA := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case accountAStarted <- struct{}{}:
		default:
		}
		time.Sleep(700 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer relayA.Close()
	accountBStarted := make(chan struct{}, 1)
	relayB := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case accountBStarted <- struct{}{}:
		default:
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer relayB.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Slow Relay\n"+
		"    base-url: "+relayA.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: slow-key\n"+
		"  - name: Fast Relay\n"+
		"    base-url: "+relayB.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: fast-key\n")
	views, err := discoverProviderViews("")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, view := range views {
		ids[view.Name] = view.ID
	}
	slowID, fastID := ids["Slow Relay"], ids["Fast Relay"]
	if slowID == "" || fastID == "" {
		t.Fatalf("provider ids not found: %+v", ids)
	}
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{
		slowID: {AllowInternalHTTP: true},
		fastID: {AllowInternalHTTP: true},
	}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{
		{ID: slowID, Monitored: boolPtr(true), Adapter: "newapi-usage"},
		{ID: fastID, Monitored: boolPtr(true), Adapter: "newapi-usage"},
	}); err != nil {
		t.Fatal(err)
	}

	slowDone := make(chan error, 1)
	go func() { slowDone <- refreshAccounts("", true, []string{slowID}) }()
	select {
	case <-accountAStarted:
	case <-time.After(time.Second):
		t.Fatal("slow account request did not start")
	}
	fastDone := make(chan error, 1)
	go func() { fastDone <- refreshAccounts("", true, []string{fastID}) }()
	select {
	case <-accountBStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("fast account was blocked by the slow account refresh")
	}
	if err := <-fastDone; err != nil {
		t.Fatal(err)
	}
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
}

func TestRefreshAccountsCoalescesConcurrentSameAccount(t *testing.T) {
	var hits atomic.Int32
	requestStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-release:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"success":true,"data":{"limits":[]}}`))
		case <-request.Context().Done():
			return
		}
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Coalesced Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: coalesced-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "zai-usage"}}); err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- refreshAccounts("", true, []string{id}) }()
	select {
	case <-requestStarted:
	case err := <-firstDone:
		snapshot, present := state.store.get(id)
		t.Fatalf("first refresh ended before request: err=%v snapshot=%+v error=%+v present=%v", err, snapshot, snapshot.Error, present)
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- refreshAccounts("", true, []string{id}) }()
	time.Sleep(100 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("same-account refresh issued %d upstream requests, want 1", got)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("same-account refresh issued %d upstream requests, want 1", got)
	}
}

func TestRefreshAccountsDiscardsSnapshotAfterMonitoringCanceled(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-release:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"data":{"limits":[{"type":"TOKENS_LIMIT","remaining_percent":50,"unit":3,"number":5}]}}`))
		case <-request.Context().Done():
			return
		}
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Canceled Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: canceled-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "zai-usage"}}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- refreshAccounts("", true, []string{id}) }()
	select {
	case <-requestStarted:
	case err := <-done:
		t.Fatalf("refresh ended before the upstream request started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("refresh did not start the upstream request")
	}
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(false), PATAction: patActionKeep}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if _, present := state.store.get(id); present {
		t.Fatal("snapshot from a canceled account was committed as active")
	}
	if report := buildReport(); report.Summary.Total != 0 {
		t.Fatalf("report includes a canceled account: %+v", report.Summary)
	}
}

func TestRefreshAccountsCancelsSlowResponseBodyAtAccountBudget(t *testing.T) {
	requestCanceled := make(chan struct{}, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("test relay does not support flushing")
			return
		}
		_, _ = writer.Write([]byte(`{"data":{"limits":[`))
		flusher.Flush()
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-request.Context().Done():
				select {
				case requestCanceled <- struct{}{}:
				default:
				}
				return
			case <-ticker.C:
				_, _ = writer.Write([]byte(`{"type":"TOKENS_LIMIT","remaining_percent":50},`))
				flusher.Flush()
			}
		}
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Slow Body Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: slow-body-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.AccountTimeoutSecond = 1
	state.cfg.RequestTimeoutSecond = 5
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "zai-usage"}}); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if err := refreshAccounts("", true, []string{id}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 1600*time.Millisecond {
		t.Fatalf("slow response body exceeded account budget: %s", elapsed)
	}
	select {
	case <-requestCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("upstream request context was not canceled after the account budget expired")
	}
}

func TestProviderConfigRefreshStartsAfterQueryConfigurationChanges(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":{"limits":[]}}`))
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Config Refresh Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: config-refresh-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "newapi-usage"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state.store.put(accountSnapshot{AccountID: id, AccountName: "Config Refresh Relay", AdapterID: "newapi-usage", Status: statusOK, Kind: kindQuota, CheckedAt: now, LastAttemptAt: now, LastSuccessAt: &now})

	request := providerMutationRequest{
		BaseRevision: int64Ptr(state.prefs.revision()),
		Providers: []providerMutation{{
			ID:            id,
			PATAction:     patActionReplace,
			ManagementPAT: "management-secret",
			Monitored:     boolPtr(true),
			Adapter:       "newapi-usage",
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleProviderConfigRoute(managementRequest{Method: http.MethodPut, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if status, responseBody := managementResponseBody(t, raw); status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, responseBody)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("query configuration change did not queue an account refresh")
	}
	waitForRefreshToFinish(t)
}

func TestRefreshAccountsSkipsFreshSnapshotsWithoutForce(t *testing.T) {
	var hits atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"limits":[{"type":"TOKENS_LIMIT","remaining_percent":50}]}}`))
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Fresh Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: fresh-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.CacheTTLSeconds = 300
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "zai-usage"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	revision := state.prefs.queryRevision(id)
	state.store.put(accountSnapshot{AccountID: id, AccountName: "Fresh Relay", AdapterID: "zai-usage", Status: statusOK, Kind: kindPeriodQuota, ConfigRevision: revision, CheckedAt: now, LastAttemptAt: now, LastSuccessAt: &now})

	if err := refreshAccounts("", false, []string{id}); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("fresh snapshot issued %d upstream requests, want 0", got)
	}
}

func TestAccountRefreshGroupDoesNotReuseAcrossRevisions(t *testing.T) {
	group := accountRefreshGroup{calls: map[accountRefreshKey]*accountRefreshCall{}}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	type refreshResult struct {
		snapshot accountSnapshot
		err      error
	}
	firstDone := make(chan refreshResult, 1)
	secondDone := make(chan refreshResult, 1)
	var calls atomic.Int32

	go func() {
		snapshot, err := group.do(context.Background(), accountRefreshKey{accountID: "acct", revision: 1, signature: "old"}, func() (accountSnapshot, error) {
			calls.Add(1)
			close(firstStarted)
			<-releaseFirst
			return accountSnapshot{AccountID: "acct", ConfigRevision: 1}, nil
		})
		firstDone <- refreshResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}

	go func() {
		snapshot, err := group.do(context.Background(), accountRefreshKey{accountID: "acct", revision: 2, signature: "new"}, func() (accountSnapshot, error) {
			calls.Add(1)
			close(secondStarted)
			return accountSnapshot{AccountID: "acct", ConfigRevision: 2}, nil
		})
		secondDone <- refreshResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("a refresh from a different revision reused the in-flight request")
	}
	close(releaseFirst)

	first := <-firstDone
	second := <-secondDone
	if first.err != nil || second.err != nil {
		t.Fatalf("refresh errors: first=%v second=%v", first.err, second.err)
	}
	if calls.Load() != 2 {
		t.Fatalf("query calls = %d, want 2", calls.Load())
	}
	if first.snapshot.ConfigRevision != 1 || second.snapshot.ConfigRevision != 2 {
		t.Fatalf("snapshot revisions = %d/%d, want 1/2", first.snapshot.ConfigRevision, second.snapshot.ConfigRevision)
	}
}

func TestBackgroundSchedulerStopsAndCancelsInflightRefresh(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	requestCanceled := make(chan struct{}, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		<-request.Context().Done()
		select {
		case requestCanceled <- struct{}{}:
		default:
		}
	}))
	defer relay.Close()

	useTestPluginState(t, "openai-compatibility:\n"+
		"  - name: Scheduler Relay\n"+
		"    base-url: "+relay.URL+"/v1\n"+
		"    api-key-entries:\n"+
		"      - api-key: scheduler-key\n")
	id := providerIDForTest(t)
	state.mu.Lock()
	state.cfg.Enabled = true
	state.cfg.CacheTTLSeconds = 1
	state.cfg.SyncIntervalSeconds = 1
	state.cfg.Monitors = map[string]monitorConfig{id: {AllowInternalHTTP: true}}
	state.mu.Unlock()
	if _, err := state.prefs.updateBatch([]providerPreferenceMutation{{ID: id, Monitored: boolPtr(true), Adapter: "zai-usage"}}); err != nil {
		t.Fatal(err)
	}

	startBackgroundScheduler()
	t.Cleanup(stopBackgroundScheduler)
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("background scheduler did not start an upstream refresh")
	}

	stopped := make(chan struct{})
	go func() {
		stopBackgroundScheduler()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("background scheduler did not stop")
	}
	select {
	case <-requestCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("background scheduler stop did not cancel the in-flight request")
	}
}

func waitForRefreshToFinish(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		refreshStatus.mu.RLock()
		running := refreshStatus.running
		refreshStatus.mu.RUnlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background refresh did not finish")
}

func backgroundSchedulerIsRunning() bool {
	backgroundScheduler.mu.Lock()
	defer backgroundScheduler.mu.Unlock()
	return backgroundScheduler.running
}

func waitForRefreshJobs(t *testing.T, jobs *refreshJobStore) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		jobs.mu.RLock()
		pending := false
		for _, job := range jobs.jobs {
			if job.Status == "queued" || job.Status == "running" {
				pending = true
				break
			}
		}
		jobs.mu.RUnlock()
		if !pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh jobs did not finish before test cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func int64Ptr(value int64) *int64 { return &value }

func stringPtr(value string) *string { return &value }

func TestManagementRegistrationUsesExactRoutes(t *testing.T) {
	raw, err := handleMethod("management.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	var envelopeValue envelope
	if err := json.Unmarshal(raw, &envelopeValue); err != nil {
		t.Fatal(err)
	}
	var registrationValue managementRegistration
	if err := json.Unmarshal(envelopeValue.Result, &registrationValue); err != nil {
		t.Fatal(err)
	}
	if len(registrationValue.Routes) != 9 {
		t.Fatalf("routes = %d, want 9", len(registrationValue.Routes))
	}
	for _, route := range registrationValue.Routes {
		if route.Path == "" || route.Path[0] != '/' {
			t.Fatalf("route is not absolute: %+v", route)
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/summary" {
			continue
		}
		if route.Method == http.MethodPost && route.Path == "/upstream-monitor/refresh" {
			continue
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/refresh" {
			continue
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/config" {
			continue
		}
		if route.Method == http.MethodPut && route.Path == "/upstream-monitor/config" {
			continue
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/state" {
			continue
		}
		if route.Method == http.MethodPut && route.Path == "/upstream-monitor/providers" {
			continue
		}
		if route.Method == http.MethodPost && route.Path == "/upstream-monitor/cleanup" {
			continue
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/history" {
			continue
		}
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestReadTokenRequiresBearerHeader(t *testing.T) {
	const envName = "CPA_UPSTREAM_MONITOR_TEST_TOKEN"
	t.Setenv(envName, "test-token")
	state.mu.Lock()
	previous := state.cfg.ReadTokenEnv
	state.cfg.ReadTokenEnv = envName
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.ReadTokenEnv = previous
		state.mu.Unlock()
	})

	if validReadToken(http.Header{}) {
		t.Fatal("empty headers unexpectedly authorized")
	}
	if validReadToken(http.Header{"Authorization": []string{"Bearer wrong"}}) {
		t.Fatal("wrong token unexpectedly authorized")
	}
	if !validReadToken(http.Header{"Authorization": []string{"Bearer test-token"}}) {
		t.Fatal("correct token was rejected")
	}
}
