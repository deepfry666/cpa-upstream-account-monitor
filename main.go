package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
    void* ptr;
    size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
    uint32_t abi_version;
    void* host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
    stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
    if (stored_host == NULL || stored_host->call == NULL) return 1;
    return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
    if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
        stored_host->free_buffer(ptr, len);
    }
}
*/
import "C"

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginName = "upstream-monitor"
const repositoryURL = "https://github.com/deepfry666/cpa-upstream-account-monitor"

var pluginVersion = "0.5.1"

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type pluginConfig struct {
	Enabled              bool                     `yaml:"enabled"`
	CacheTTLSeconds      int                      `yaml:"cache_ttl_seconds"`
	SyncIntervalSeconds  int                      `yaml:"sync_interval_seconds"`
	RequestTimeoutSecond int                      `yaml:"request_timeout_seconds"`
	AccountTimeoutSecond int                      `yaml:"account_timeout_seconds"`
	ReadTokenEnv         string                   `yaml:"read_token_env"`
	Monitors             map[string]monitorConfig `yaml:"monitors"`
	SourceConfigPath     string                   `yaml:"source_config_path"`
	CachePath            string                   `yaml:"cache_path"`
	PreferencesPath      string                   `yaml:"preferences_path"`
	Thresholds           thresholdConfig          `yaml:"thresholds"`
}

type pluginState struct {
	mu                 sync.RWMutex
	snapshotMu         sync.Mutex
	cfg                pluginConfig
	store              *snapshotStore
	prefs              *monitorPrefs
	jobs               *refreshJobStore
	schemaVersion      uint32
	directoryMu        sync.RWMutex
	directoryViews     []providerView
	directoryAttemptAt *time.Time
	directorySuccessAt *time.Time
	directoryError     string
}

var state = &pluginState{
	cfg: pluginConfig{
		Enabled:              true,
		CacheTTLSeconds:      300,
		SyncIntervalSeconds:  60,
		RequestTimeoutSecond: 8,
		AccountTimeoutSecond: 30,
		ReadTokenEnv:         "UPSTREAM_MONITOR_READ_TOKEN",
		SourceConfigPath:     "/CLIProxyAPI/config.yaml",
		CachePath:            "/CLIProxyAPI/data/upstream-monitor-snapshots.json",
		Thresholds: thresholdConfig{
			WarningPercent:  20,
			CriticalPercent: 10,
		},
	},
	store:         newSnapshotStore(),
	prefs:         newMonitorPrefs(),
	jobs:          newRefreshJobStore(),
	schemaVersion: 5,
}

var refreshMu sync.Mutex
var configureMu sync.Mutex
var lastRefresh time.Time

type accountRefreshCall struct {
	done     chan struct{}
	snapshot accountSnapshot
	err      error
}

type accountRefreshKey struct {
	accountID string
	revision  int64
	signature string
}

type accountRefreshGroup struct {
	mu    sync.Mutex
	calls map[accountRefreshKey]*accountRefreshCall
}

var candidateRefreshes = accountRefreshGroup{calls: map[accountRefreshKey]*accountRefreshCall{}}

func (g *accountRefreshGroup) do(ctx context.Context, key accountRefreshKey, query func() (accountSnapshot, error)) (accountSnapshot, error) {
	g.mu.Lock()
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		select {
		case <-call.done:
			return call.snapshot, call.err
		case <-ctx.Done():
			return accountSnapshot{}, ctx.Err()
		}
	}
	call := &accountRefreshCall{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	call.snapshot, call.err = query()
	g.mu.Lock()
	delete(g.calls, key)
	close(call.done)
	g.mu.Unlock()
	return call.snapshot, call.err
}

func newAccountRefreshKey(id string, revision int64, candidate credentialCandidate, storage []byte) accountRefreshKey {
	hasher := sha256.New()
	writePart := func(value string) {
		_, _ = hasher.Write([]byte(strconv.Itoa(len(value))))
		_, _ = hasher.Write([]byte{':'})
		_, _ = hasher.Write([]byte(value))
	}
	for _, value := range []string{
		id,
		strconv.FormatInt(revision, 10),
		candidate.AccountID,
		candidate.LegacyID,
		candidate.AuthIndex,
		candidate.AuthID,
		candidate.Provider,
		candidate.BaseURL,
		candidate.ManagementBaseURL,
		candidate.ProxyMode,
		candidate.ProxyURL,
		candidate.Source,
		candidate.SourceID,
		candidate.Fingerprint,
		strconv.FormatBool(candidate.AllowInternalHTTP),
	} {
		writePart(value)
	}
	_, _ = hasher.Write(storage)
	return accountRefreshKey{accountID: id, revision: revision, signature: hex.EncodeToString(hasher.Sum(nil))}
}

type refreshInfo struct {
	mu       sync.RWMutex
	running  bool
	started  *time.Time
	finished *time.Time
	lastErr  string
}

var refreshStatus refreshInfo

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ManagementAPI bool `json:"management_api"`
	QuotaProvider bool `json:"quota_provider"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRegistration struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementRequest struct {
	Method         string              `json:"Method"`
	Path           string              `json:"Path"`
	Headers        http.Header         `json:"Headers"`
	Query          map[string][]string `json:"Query"`
	Body           []byte              `json:"Body"`
	HostCallbackID string              `json:"host_callback_id,omitempty"`
}

type quotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostHTTPResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"`
}

type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type hostAuthGetRequest struct {
	AuthIndex string `json:"auth_index"`
}

type hostAuthGetResponse struct {
	JSON json.RawMessage `json:"json"`
}

type hostHTTPRequest struct {
	HostCallbackID string              `json:"host_callback_id,omitempty"`
	Method         string              `json:"method"`
	URL            string              `json:"url"`
	Headers        map[string][]string `json:"headers,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	stopBackgroundScheduler()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if len(request) > 0 {
			if err := json.Unmarshal(request, &lifecycle); err != nil {
				return nil, err
			}
			if err := configure(lifecycle.ConfigYAML); err != nil {
				return nil, err
			}
		}
		setSchemaVersion(lifecycle.SchemaVersion)
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodQuotaIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginName})
	case pluginabi.MethodQuotaDescribe:
		return okEnvelope(pluginapi.QuotaDescribeResponse{
			DisplayName: "上游账户监控",
			SupportedProviders: []string{
				"deepseek", "zai", "zhipu", "glm", "kimi", "kimi-coding", "kimi-for-coding",
				"moonshot", "moonshotai", "openai-compatibility", "new-api", "newapi",
				"sub2api", "passion", "opencode-go", "commandcode", "command-code", "goat",
			},
			SupportsReset: false,
		})
	case pluginabi.MethodQuotaFetch:
		return handleQuotaFetch(request)
	case pluginabi.MethodQuotaReset:
		return okEnvelope(pluginapi.QuotaResetResponse{Success: false, Message: "quota reset is not supported"})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Routes: []managementRoute{
				{Method: http.MethodGet, Path: "/upstream-monitor/summary"},
				{Method: http.MethodPost, Path: "/upstream-monitor/refresh"},
				{Method: http.MethodGet, Path: "/upstream-monitor/refresh"},
				{Method: http.MethodGet, Path: "/upstream-monitor/config"},
				{Method: http.MethodPut, Path: "/upstream-monitor/config"},
				{Method: http.MethodGet, Path: "/upstream-monitor/state"},
				{Method: http.MethodPut, Path: "/upstream-monitor/providers"},
				{Method: http.MethodPost, Path: "/upstream-monitor/cleanup"},
				{Method: http.MethodGet, Path: "/upstream-monitor/history"},
			},
			Resources: []resourceRoute{
				{Path: "/ui", Menu: "上游账户监控", Description: "查看上游账户余额、额度与用量。"},
				{Path: "/api/v1/report", Description: "Read-only machine report for Hermes."},
				{Path: "/api/v1/health", Description: "Read-only health status for Hermes."},
			},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	configureMu.Lock()
	defer configureMu.Unlock()
	var cfg pluginConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.CacheTTLSeconds <= 0 {
		cfg.CacheTTLSeconds = 300
	}
	if cfg.SyncIntervalSeconds <= 0 {
		cfg.SyncIntervalSeconds = 60
	}
	if cfg.RequestTimeoutSecond <= 0 {
		cfg.RequestTimeoutSecond = 8
	}
	if cfg.AccountTimeoutSecond <= 0 {
		cfg.AccountTimeoutSecond = 30
	}
	if strings.TrimSpace(cfg.ReadTokenEnv) == "" {
		cfg.ReadTokenEnv = "UPSTREAM_MONITOR_READ_TOKEN"
	}
	if strings.TrimSpace(cfg.SourceConfigPath) == "" {
		cfg.SourceConfigPath = "/CLIProxyAPI/config.yaml"
	}
	if strings.TrimSpace(cfg.CachePath) == "" {
		cfg.CachePath = "/CLIProxyAPI/data/upstream-monitor-snapshots.json"
	}
	if strings.TrimSpace(cfg.PreferencesPath) == "" {
		cfg.PreferencesPath = "/CLIProxyAPI/data/upstream-monitor-preferences.json"
	}
	if cfg.Thresholds.WarningPercent <= 0 || cfg.Thresholds.WarningPercent > 100 {
		cfg.Thresholds.WarningPercent = 20
	}
	if cfg.Thresholds.CriticalPercent <= 0 || cfg.Thresholds.CriticalPercent >= cfg.Thresholds.WarningPercent {
		cfg.Thresholds.CriticalPercent = 10
	}
	cashByCurrency, err := normalizeCashThresholds(cfg.Thresholds.CashByCurrency)
	if err != nil {
		return fmt.Errorf("校验现金币种阈值失败: %w", err)
	}
	cashByAccount, err := normalizeAccountCashThresholds(cfg.Thresholds.CashByAccount)
	if err != nil {
		return fmt.Errorf("校验账户现金阈值失败: %w", err)
	}
	cfg.Thresholds.CashByCurrency = cashByCurrency
	cfg.Thresholds.CashByAccount = cashByAccount
	if err := validateMonitorConfigs(cfg.Monitors); err != nil {
		return err
	}
	if err := state.prefs.configure(cfg.PreferencesPath); err != nil {
		return err
	}
	_ = state.store.load(cfg.CachePath)
	state.mu.Lock()
	state.cfg = cfg
	state.mu.Unlock()
	stopBackgroundScheduler()
	startBackgroundScheduler()
	return nil
}

func setSchemaVersion(hostVersion uint32) {
	version := hostVersion
	if version == 0 || version > 5 {
		version = 5
	}
	state.mu.Lock()
	state.schemaVersion = version
	state.mu.Unlock()
}

func pluginRegistration() registration {
	state.mu.RLock()
	schemaVersion := state.schemaVersion
	state.mu.RUnlock()
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "deepfry666",
			GitHubRepository: repositoryURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable the monitor."},
				{Name: "cache_ttl_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Cache lifetime for successful snapshots."},
				{Name: "sync_interval_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "CPA discovery and background scheduling interval."},
				{Name: "request_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Timeout for one upstream query."},
				{Name: "account_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Total timeout for refreshing one account."},
				{Name: "read_token_env", Type: pluginapi.ConfigFieldTypeString, Description: "Environment variable containing the Hermes read-only token."},
				{Name: "source_config_path", Type: pluginapi.ConfigFieldTypeString, Description: "CPA config path used to discover static API-key providers."},
				{Name: "cache_path", Type: pluginapi.ConfigFieldTypeString, Description: "Path for the redacted snapshot cache."},
				{Name: "preferences_path", Type: pluginapi.ConfigFieldTypeString, Description: "Path for encrypted monitoring selections and NewAPI management PATs."},
				{Name: "thresholds", Type: pluginapi.ConfigFieldTypeObject, Description: "Warning and critical quota thresholds as percentages."},
			},
		},
		Capabilities: registrationCapability{ManagementAPI: true, QuotaProvider: true},
	}
}

func handleQuotaFetch(raw []byte) ([]byte, error) {
	var req quotaFetchRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	candidate := credentialCandidate{
		LegacyID:  req.AuthIndex,
		AuthIndex: req.AuthIndex,
		AuthID:    req.AuthID,
		Provider:  req.Provider,
		Source:    "host",
		SourceID:  req.AuthID,
	}
	if req.Attributes != nil {
		candidate.BaseURL = strings.TrimSpace(req.Attributes["base_url"])
	}
	key := state.prefs.preferenceKey()
	if len(key) == 32 && strings.TrimSpace(candidate.SourceID) == "" {
		if secret := tokenFromCredential(req.StorageJSON, req.Attributes); secret != "" {
			candidate.Fingerprint = credentialIdentityFingerprint(key, candidate.Source, candidate.SourceID, candidate.BaseURL, secret)
		}
	}
	ids, err := state.prefs.resolveIdentities([]credentialCandidate{candidate})
	if err != nil {
		return errorEnvelope("identity_resolution_failed", err.Error()), nil
	}
	candidate.AccountID = ids[0]
	if _, _, ok := adapterFor(candidate); !ok {
		return errorEnvelope("unsupported_provider", "no adapter is registered for this provider"), nil
	}
	snapshot, err := queryCandidate(req.HostCallbackID, candidate, req.StorageJSON, req.Attributes)
	if err != nil {
		return errorEnvelope("upstream_request_failed", err.Error()), nil
	}
	state.store.put(snapshot)
	return okEnvelope(quotaResponseFromSnapshot(snapshot))
}

func queryDeepSeekSnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, storage []byte, attributes map[string]string) (accountSnapshot, error) {
	token := tokenForCandidate(candidate, storage, attributes)
	if token == "" {
		return accountSnapshot{}, fmt.Errorf("credential token is unavailable")
	}
	endpoint, err := deepSeekURL(candidate.BaseURL)
	if err != nil {
		return accountSnapshot{}, err
	}
	started := time.Now()
	httpResponse, err := queryEndpointContext(ctx, callbackID, endpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return accountSnapshot{}, fmt.Errorf("DeepSeek returned HTTP %d", httpResponse.StatusCode)
	}
	snapshot, err := parseDeepSeekSnapshot(candidate, httpResponse.Body, time.Now().UTC())
	if err != nil {
		return accountSnapshot{}, err
	}
	snapshot = accountSnapshotWithSuccess(accountSnapshot{}, snapshot, time.Now().UTC(), time.Since(started))
	return snapshot, nil
}

func quotaResponseFromSnapshot(snapshot accountSnapshot) pluginapi.QuotaFetchResponse {
	groups := make([]pluginapi.QuotaGroup, 0, 1)
	if len(snapshot.Windows) > 0 || len(snapshot.Quantities) > 0 {
		group := pluginapi.QuotaGroup{DisplayName: "Quota"}
		for _, window := range snapshot.Windows {
			if window.RemainingFraction == nil {
				continue
			}
			bucket := pluginapi.QuotaBucket{Window: window.Name, RemainingFraction: *window.RemainingFraction}
			if window.ResetAt != nil {
				bucket.ResetTime = window.ResetAt.UTC().Format(time.RFC3339)
			}
			group.Buckets = append(group.Buckets, bucket)
		}
		for _, quantity := range snapshot.Quantities {
			remaining, okRemaining := numericValue(quantity.Remaining)
			total, okTotal := numericValue(quantity.Total)
			if !okRemaining || !okTotal || total <= 0 {
				continue
			}
			bucket := pluginapi.QuotaBucket{Window: quantity.Name, RemainingFraction: remaining / total}
			if quantity.ResetAt != nil {
				bucket.ResetTime = quantity.ResetAt.UTC().Format(time.RFC3339)
			}
			group.Buckets = append(group.Buckets, bucket)
		}
		if len(group.Buckets) > 0 {
			groups = append(groups, group)
		}
	}
	return pluginapi.QuotaFetchResponse{Groups: groups}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
	}
	switch {
	case req.Path == "/v0/resource/plugins/upstream-monitor/api/v1/health":
		if !validReadToken(req.Headers) {
			return jsonResponse(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"status":         "ok",
			"schema_version": 1,
			"version":        pluginVersion,
			"generated_at":   time.Now().UTC(),
		})
	case req.Path == "/v0/resource/plugins/upstream-monitor/api/v1/report":
		if !validReadToken(req.Headers) {
			return jsonResponse(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
		if queryValue(req.Query, "refresh") == "1" {
			if err := refreshDiscoveredAccounts(req.HostCallbackID, true); err != nil {
				return jsonResponse(http.StatusBadGateway, map[string]string{"error": err.Error()})
			}
		}
		return jsonResponse(http.StatusOK, buildReport())
	case req.Path == "/v0/management/upstream-monitor/summary":
		return jsonResponse(http.StatusOK, buildReport())
	case req.Path == "/v0/management/upstream-monitor/state":
		maybeStartBackgroundRefresh(req.HostCallbackID)
		value, err := buildMonitorUIState(req.HostCallbackID)
		if err != nil {
			return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return jsonResponse(http.StatusOK, value)
	case req.Path == "/v0/management/upstream-monitor/refresh":
		return handleRefreshRoute(req)
	case req.Path == "/v0/management/upstream-monitor/providers":
		return handleProviderConfigRoute(req)
	case req.Path == "/v0/management/upstream-monitor/cleanup":
		return handleCleanupRoute(req)
	case req.Path == "/v0/management/upstream-monitor/history":
		return handleHistoryRoute(req)
	case req.Path == "/v0/management/upstream-monitor/config":
		return handleConfigRoute(req)
	case req.Path == "/v0/resource/plugins/upstream-monitor/ui":
		return htmlResponse(http.StatusOK, uiHTML)
	default:
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

type providerMutation struct {
	ID                 string         `json:"id"`
	CustomName         *string        `json:"custom_name,omitempty"`
	Monitored          *bool          `json:"monitored,omitempty"`
	Adapter            string         `json:"adapter,omitempty"`
	ClearAdapter       bool           `json:"clear_adapter,omitempty"`
	PATAction          string         `json:"pat_action,omitempty"`
	ManagementPAT      string         `json:"management_pat,omitempty"`
	ClearManagementPAT bool           `json:"clear_management_pat,omitempty"`
	CashThreshold      *cashThreshold `json:"cash_threshold,omitempty"`
	ClearCashThreshold bool           `json:"clear_cash_threshold,omitempty"`
}

type providerMutationRequest struct {
	BaseRevision *int64             `json:"base_revision,omitempty"`
	Providers    []providerMutation `json:"providers"`
}

type accountRefreshRequest struct {
	AccountIDs []string `json:"account_ids,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	Force      bool     `json:"force,omitempty"`
	Async      bool     `json:"async,omitempty"`
}

type accountRefreshJob struct {
	ID         string                 `json:"job_id"`
	Status     string                 `json:"status"`
	AccountIDs []string               `json:"account_ids,omitempty"`
	Total      int                    `json:"total"`
	Completed  int                    `json:"completed"`
	Failed     int                    `json:"failed"`
	Skipped    int                    `json:"skipped"`
	Results    []accountRefreshResult `json:"results,omitempty"`
	Error      string                 `json:"error,omitempty"`
	StartedAt  *time.Time             `json:"started_at,omitempty"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
}

type accountRefreshResult struct {
	AccountID string `json:"account_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

type refreshRunResult struct {
	Results   []accountRefreshResult
	Succeeded int
	Failed    int
	Skipped   int
}

type refreshJobStore struct {
	mu       sync.RWMutex
	sequence uint64
	jobs     map[string]accountRefreshJob
}

func newRefreshJobStore() *refreshJobStore {
	return &refreshJobStore{jobs: map[string]accountRefreshJob{}}
}

func (s *refreshJobStore) create(accountIDs []string) accountRefreshJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	now := time.Now().UTC()
	job := accountRefreshJob{
		ID:         fmt.Sprintf("refresh_%d_%d", now.UnixNano(), s.sequence),
		Status:     "queued",
		AccountIDs: append([]string(nil), accountIDs...),
		Total:      len(accountIDs),
		StartedAt:  &now,
	}
	s.jobs[job.ID] = job
	return job
}

func (s *refreshJobStore) update(id string, mutate func(*accountRefreshJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	mutate(&job)
	s.jobs[id] = job
}

func (s *refreshJobStore) get(id string) (accountRefreshJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return accountRefreshJob{}, false
	}
	job.AccountIDs = append([]string(nil), job.AccountIDs...)
	job.Results = append([]accountRefreshResult(nil), job.Results...)
	return job, true
}

func (s *pluginState) refreshJobs() *refreshJobStore {
	if s == nil {
		return newRefreshJobStore()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = newRefreshJobStore()
	}
	return s.jobs
}

func (s *pluginState) startRefreshJob(callbackID string, request accountRefreshRequest) accountRefreshJob {
	jobs := s.refreshJobs()
	job := jobs.create(request.AccountIDs)
	go func() {
		started := time.Now().UTC()
		jobs.update(job.ID, func(value *accountRefreshJob) {
			value.Status = "running"
			value.StartedAt = &started
		})
		run, refreshErr := refreshAccountsDetailed(callbackID, request.Force, request.AccountIDs)
		finished := time.Now().UTC()
		jobs.update(job.ID, func(value *accountRefreshJob) {
			value.FinishedAt = &finished
			value.Total = len(run.Results)
			value.Completed = run.Succeeded
			value.Failed = run.Failed
			value.Skipped = run.Skipped
			value.Results = append([]accountRefreshResult(nil), run.Results...)
			if refreshErr != nil {
				value.Status = "failed"
				if value.Failed == 0 && value.Total == 0 {
					value.Failed = len(request.AccountIDs)
				}
				value.Error = refreshErr.Error()
				return
			}
			value.Status = "completed"
		})
	}()
	return job
}

func handleRefreshRoute(req managementRequest) ([]byte, error) {
	jobs := state.refreshJobs()
	if req.Method == http.MethodGet {
		id := queryValue(req.Query, "job_id")
		if id == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "job_id is required"})
		}
		job, ok := jobs.get(id)
		if !ok {
			return jsonResponse(http.StatusNotFound, map[string]string{"error": "refresh job was not found"})
		}
		return jsonResponse(http.StatusOK, job)
	}
	if req.Method != http.MethodPost {
		return jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
	request, err := decodeRefreshRequest(req.Body)
	if err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if len(request.AccountIDs) > 0 {
		if err := validateRefreshAccountIDs(req.HostCallbackID, request.AccountIDs); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}
	if request.Async {
		job := state.startRefreshJob(req.HostCallbackID, request)
		return jsonResponse(http.StatusAccepted, job)
	}
	if err := refreshAccounts(req.HostCallbackID, request.Force, request.AccountIDs); err != nil {
		return jsonResponse(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, buildReport())
}

func validateRefreshAccountIDs(callbackID string, accountIDs []string) error {
	accounts, err := discoverHostAccounts(callbackID, false)
	if err != nil && strings.TrimSpace(callbackID) != "" {
		return err
	}
	byID := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		byID[candidateAccountID(account.candidate)] = struct{}{}
	}
	staticAccounts, err := discoverStaticAccounts()
	if err != nil {
		return err
	}
	for _, account := range staticAccounts {
		byID[candidateAccountID(account.candidate)] = struct{}{}
	}
	for _, id := range accountIDs {
		if _, ok := byID[id]; !ok || !state.prefs.isMonitored(id, false) {
			return fmt.Errorf("monitored account %q was not found", id)
		}
	}
	return nil
}

func decodeRefreshRequest(body []byte) (accountRefreshRequest, error) {
	var request accountRefreshRequest
	if len(body) == 0 {
		return request, nil
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return accountRefreshRequest{}, fmt.Errorf("invalid refresh request")
	}
	request.Scope = strings.ToLower(strings.TrimSpace(request.Scope))
	switch request.Scope {
	case "":
	case "all":
		if len(request.AccountIDs) > 0 {
			return accountRefreshRequest{}, fmt.Errorf("scope all cannot include account_ids")
		}
	default:
		return accountRefreshRequest{}, fmt.Errorf("unsupported refresh scope %q", request.Scope)
	}
	seen := make(map[string]struct{}, len(request.AccountIDs))
	ids := request.AccountIDs[:0]
	for _, rawID := range request.AccountIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return accountRefreshRequest{}, fmt.Errorf("account_ids cannot contain an empty id")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	request.AccountIDs = ids
	return request, nil
}

func handleProviderConfigRoute(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPut {
		return jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
	var request providerMutationRequest
	if err := json.Unmarshal(req.Body, &request); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid provider configuration"})
	}
	views, err := discoverProviderViews(req.HostCallbackID)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	known := make(map[string]struct{}, len(views))
	defaults := make(map[string]string, len(views))
	for _, view := range views {
		known[view.ID] = struct{}{}
		defaults[view.ID] = view.DefaultName
	}
	preferenceMutations := make([]providerPreferenceMutation, len(request.Providers))
	wasMonitored := make([]bool, len(request.Providers))
	queryRevisions := make([]int64, len(request.Providers))
	thresholds := state.effectiveThresholds()
	thresholdsChanged := false
	for index, mutation := range request.Providers {
		mutation.ID = strings.TrimSpace(mutation.ID)
		if _, ok := known[mutation.ID]; !ok {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "provider is not present in CPA"})
		}
		if mutation.Adapter == "auto" {
			mutation.ClearAdapter = true
			mutation.Adapter = ""
		}
		if mutation.Adapter != "" && !isSupportedAdapter(mutation.Adapter) {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unsupported adapter"})
		}
		if mutation.CashThreshold != nil && mutation.ClearCashThreshold {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "cash threshold cannot be both set and cleared"})
		}
		if mutation.CashThreshold != nil {
			normalized, err := normalizeAccountCashThresholds(map[string]cashThreshold{mutation.ID: *mutation.CashThreshold})
			if err != nil {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			if thresholds.CashByAccount == nil {
				thresholds.CashByAccount = map[string]cashThreshold{}
			}
			thresholds.CashByAccount[mutation.ID] = normalized[mutation.ID]
			thresholdsChanged = true
		} else if mutation.ClearCashThreshold {
			if _, exists := thresholds.CashByAccount[mutation.ID]; exists {
				delete(thresholds.CashByAccount, mutation.ID)
				if len(thresholds.CashByAccount) == 0 {
					thresholds.CashByAccount = nil
				}
				thresholdsChanged = true
			}
		}
		action, err := normalizeProviderPATAction(mutation)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		preferenceMutations[index] = providerPreferenceMutation{
			ID:           mutation.ID,
			Monitored:    mutation.Monitored,
			Adapter:      mutation.Adapter,
			ClearAdapter: mutation.ClearAdapter,
			PATAction:    action,
			PAT:          mutation.ManagementPAT,
			CustomName:   mutation.CustomName,
		}
		wasMonitored[index] = state.prefs.isMonitored(mutation.ID, false)
		queryRevisions[index] = state.prefs.queryRevision(mutation.ID)
	}
	var thresholdsToSave *thresholdConfig
	if thresholdsChanged {
		thresholdsToSave = &thresholds
	}
	revision, err := state.prefs.updateBatchAndThresholdsAtRevision(request.BaseRevision, preferenceMutations, thresholdsToSave)
	if err != nil {
		var conflict *revisionConflictError
		if errors.As(err, &conflict) {
			return jsonResponse(http.StatusConflict, map[string]any{
				"error":         "provider configuration changed; reload and try again",
				"base_revision": conflict.Expected,
				"revision":      conflict.Actual,
			})
		}
		var persistErr *preferencePersistenceError
		if errors.As(err, &persistErr) {
			return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	_ = revision
	for index, mutation := range request.Providers {
		id := strings.TrimSpace(mutation.ID)
		if mutation.CustomName != nil {
			state.store.rename(id, state.prefs.name(id, defaults[id]))
		}
		if mutation.Monitored != nil && !*mutation.Monitored {
			state.store.removeActive(id)
		} else if mutation.Monitored != nil && *mutation.Monitored && (!wasMonitored[index] || state.storeMissing(id)) {
			invalidateRefresh()
		}
	}
	refreshIDs := make([]string, 0, len(request.Providers))
	for index, mutation := range request.Providers {
		id := strings.TrimSpace(mutation.ID)
		if state.prefs.queryRevision(id) != queryRevisions[index] {
			refreshIDs = append(refreshIDs, id)
		}
	}
	state.mu.RLock()
	cachePath := state.cfg.CachePath
	state.mu.RUnlock()
	value, err := buildMonitorUIState(req.HostCallbackID)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if err := state.store.save(cachePath); err != nil {
		value.Warnings = append(value.Warnings, "配置已保存，但快照持久化失败："+err.Error())
	}
	if len(refreshIDs) > 0 {
		job := state.startRefreshJob(req.HostCallbackID, accountRefreshRequest{
			AccountIDs: refreshIDs,
			Force:      true,
			Async:      true,
		})
		value.RefreshJob = &job
	}
	return jsonResponse(http.StatusOK, value)
}

func normalizeProviderPATAction(mutation providerMutation) (string, error) {
	action := strings.ToLower(strings.TrimSpace(mutation.PATAction))
	hasReplacement := strings.TrimSpace(mutation.ManagementPAT) != ""
	if action == "" {
		switch {
		case mutation.ClearManagementPAT && hasReplacement:
			return "", fmt.Errorf("management PAT cannot be both cleared and replaced")
		case mutation.ClearManagementPAT:
			return patActionClear, nil
		case hasReplacement:
			return patActionReplace, nil
		default:
			return patActionKeep, nil
		}
	}
	switch action {
	case patActionKeep:
		if mutation.ClearManagementPAT || hasReplacement {
			return "", fmt.Errorf("pat_action keep cannot include a replacement or clear flag")
		}
	case patActionReplace:
		if mutation.ClearManagementPAT {
			return "", fmt.Errorf("pat_action replace cannot include a clear flag")
		}
		if !hasReplacement {
			return "", fmt.Errorf("pat_action replace requires a management PAT")
		}
	case patActionClear:
		if hasReplacement {
			return "", fmt.Errorf("pat_action clear cannot include a management PAT")
		}
	default:
		return "", fmt.Errorf("unsupported pat_action %q", mutation.PATAction)
	}
	return action, nil
}

type cleanupRequest struct {
	IDs []string `json:"ids"`
}

func handleCleanupRoute(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
	var request cleanupRequest
	if err := json.Unmarshal(req.Body, &request); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid cleanup request"})
	}
	if err := state.prefs.invalidateQueryResults(request.IDs); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	for _, id := range request.IDs {
		state.store.delete(strings.TrimSpace(id))
	}
	state.mu.RLock()
	cachePath := state.cfg.CachePath
	state.mu.RUnlock()
	if err := state.store.save(cachePath); err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, buildReport())
}

func handleHistoryRoute(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodGet {
		return jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
	id := queryValue(req.Query, "account_id")
	if id == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account_id is required"})
	}
	limit := 50
	if raw := queryValue(req.Query, "limit"); raw != "" {
		parsed := 0
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed < 1 || parsed > 100 {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 100"})
		}
		limit = parsed
	}
	return jsonResponse(http.StatusOK, map[string]any{"history": state.store.historyFor(id, limit)})
}

func refreshDiscoveredAccounts(callbackID string, force bool) error {
	return refreshAccounts(callbackID, force, nil)
}

func refreshAccounts(callbackID string, force bool, accountIDs []string) error {
	return refreshAccountsContext(context.Background(), callbackID, force, accountIDs)
}

func refreshAccountsContext(ctx context.Context, callbackID string, force bool, accountIDs []string) error {
	_, err := refreshAccountsDetailedContext(ctx, callbackID, force, accountIDs)
	return err
}

func refreshAccountsDetailed(callbackID string, force bool, accountIDs []string) (refreshRunResult, error) {
	return refreshAccountsDetailedContext(context.Background(), callbackID, force, accountIDs)
}

func refreshAccountsDetailedContext(ctx context.Context, callbackID string, force bool, accountIDs []string) (run refreshRunResult, refreshErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	refreshMu.Lock()
	state.mu.RLock()
	cacheTTL := time.Duration(state.cfg.CacheTTLSeconds) * time.Second
	cachePath := state.cfg.CachePath
	state.mu.RUnlock()
	refreshMu.Unlock()
	started := time.Now().UTC()
	refreshStatus.mu.Lock()
	refreshStatus.running = true
	refreshStatus.started = &started
	refreshStatus.lastErr = ""
	refreshStatus.mu.Unlock()
	defer func() {
		finished := time.Now().UTC()
		refreshStatus.mu.Lock()
		refreshStatus.running = false
		refreshStatus.finished = &finished
		if refreshErr != nil {
			refreshStatus.lastErr = refreshErr.Error()
		}
		refreshStatus.mu.Unlock()
	}()
	accounts, err := discoverHostAccounts(callbackID, true)
	if err != nil && strings.TrimSpace(callbackID) != "" {
		refreshErr = err
		return run, refreshErr
	}
	byID := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		byID[candidateAccountID(account.candidate)] = struct{}{}
	}
	staticAccounts, err := discoverStaticAccounts()
	if err != nil {
		refreshErr = err
		return run, refreshErr
	}
	for _, account := range staticAccounts {
		id := candidateAccountID(account.candidate)
		if _, duplicate := byID[id]; duplicate {
			continue
		}
		byID[id] = struct{}{}
		accounts = append(accounts, account)
	}
	defaults := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		defaults[candidateAccountID(account.candidate)] = false
	}
	state.prefs.reconcileDefaults(defaults)
	keep := make(map[string]struct{}, len(accounts))
	requested := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		requested[id] = struct{}{}
	}
	matchedRequested := make(map[string]struct{}, len(accountIDs))
	selected := make([]staticAccount, 0, len(accounts))
	for _, account := range accounts {
		if _, _, matched := adapterFor(account.candidate); !matched {
			continue
		}
		id := candidateAccountID(account.candidate)
		if !state.prefs.isMonitored(id, true) {
			continue
		}
		keep[id] = struct{}{}
		if len(requested) > 0 {
			if _, ok := requested[id]; !ok {
				continue
			}
			matchedRequested[id] = struct{}{}
		}
		selected = append(selected, account)
	}
	for id := range requested {
		if _, ok := matchedRequested[id]; !ok {
			refreshErr = fmt.Errorf("monitored account %q was not found", id)
			return run, refreshErr
		}
	}
	run.Results = make([]accountRefreshResult, len(selected))
	var workers sync.WaitGroup
	sem := make(chan struct{}, 4)
	now := time.Now().UTC()
	for index, account := range selected {
		index := index
		account := account
		id := candidateAccountID(account.candidate)
		if !force && !snapshotNeedsRefresh(id, cacheTTL, now) {
			run.Results[index] = accountRefreshResult{AccountID: id, Status: "skipped"}
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			configRevision := state.prefs.queryRevision(id)
			refreshKey := newAccountRefreshKey(id, configRevision, account.candidate, account.storage)
			snapshot, err := candidateRefreshes.do(ctx, refreshKey, func() (accountSnapshot, error) {
				return queryCandidateContext(ctx, callbackID, account.candidate, account.storage, nil)
			})
			if ctx.Err() != nil {
				return
			}
			state.snapshotMu.Lock()
			if _, exists := keep[id]; !exists || !state.prefs.canCommitQueryResult(id, configRevision) {
				state.snapshotMu.Unlock()
				run.Results[index] = accountRefreshResult{AccountID: id, Status: "skipped", ErrorCode: "REFRESH_SKIPPED", Error: "monitor configuration changed"}
				return
			}
			if err != nil {
				failed := errorSnapshot(account.candidate, "UPSTREAM_REQUEST_FAILED", err.Error())
				failed.ConfigRevision = configRevision
				state.store.put(failed)
				state.snapshotMu.Unlock()
				run.Results[index] = accountRefreshResult{AccountID: id, Status: "failed", ErrorCode: "UPSTREAM_REQUEST_FAILED", Error: err.Error()}
				return
			}
			snapshot.ConfigRevision = configRevision
			state.store.put(snapshot)
			state.snapshotMu.Unlock()
			run.Results[index] = accountRefreshResult{AccountID: id, Status: "success"}
		}()
	}
	workers.Wait()
	for _, result := range run.Results {
		switch result.Status {
		case "success":
			run.Succeeded++
		case "failed":
			run.Failed++
		case "skipped":
			run.Skipped++
		}
	}
	if err := ctx.Err(); err != nil {
		refreshErr = err
		return run, refreshErr
	}
	state.store.prune(keep)
	refreshMu.Lock()
	lastRefresh = time.Now()
	refreshMu.Unlock()
	_ = state.store.save(cachePath)
	return run, nil
}

func snapshotNeedsRefresh(id string, ttl time.Duration, now time.Time) bool {
	snapshot, ok := state.store.get(id)
	if !ok || snapshot.LastSuccessAt == nil {
		return true
	}
	if snapshot.ConfigRevision != state.prefs.queryRevision(id) {
		return true
	}
	return now.Sub(*snapshot.LastSuccessAt) >= ttl
}

type staticAccount struct {
	candidate credentialCandidate
	storage   []byte
	loadError error
	errorCode string
}

func discoverAllAccounts(callbackID string) ([]staticAccount, error) {
	hostAccounts, hostErr := discoverHostAccounts(callbackID, true)
	if hostErr != nil && strings.TrimSpace(callbackID) != "" {
		return nil, hostErr
	}
	accounts := append([]staticAccount(nil), hostAccounts...)
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		seen[candidateAccountID(account.candidate)] = struct{}{}
	}
	staticAccounts, err := discoverStaticAccounts()
	if err != nil {
		return nil, err
	}
	for _, account := range staticAccounts {
		id := candidateAccountID(account.candidate)
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accountCandidateLess(accounts[i].candidate, accounts[j].candidate)
	})
	return accounts, nil
}

func discoverHostAccounts(callbackID string, loadStorage bool) ([]staticAccount, error) {
	raw, err := callHost(pluginabi.MethodHostAuthList, []byte(`{}`))
	if err != nil {
		return nil, err
	}
	var listed hostAuthListResponse
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, fmt.Errorf("decode host auth list: %w", err)
	}
	accounts := make([]staticAccount, 0, len(listed.Files))
	seen := make(map[string]struct{}, len(listed.Files))
	for _, entry := range listed.Files {
		if entry.Disabled || strings.TrimSpace(entry.AuthIndex) == "" {
			continue
		}
		candidate := credentialCandidate{
			LegacyID:  entry.AuthIndex,
			AuthIndex: entry.AuthIndex,
			AuthID:    entry.ID,
			Name:      firstNonEmpty(entry.Label, entry.Name, entry.ID),
			Provider:  firstNonEmpty(entry.Provider, entry.Type),
			BaseURL:   entry.BaseURL,
			Source:    "host",
			SourceID:  entry.ID,
		}
		if _, _, ok := adapterFor(candidate); !ok {
			continue
		}
		if _, duplicate := seen[candidate.AuthIndex]; duplicate {
			continue
		}
		seen[candidate.AuthIndex] = struct{}{}
		account := staticAccount{candidate: candidate}
		if loadStorage {
			rawAuth, err := callHost(pluginabi.MethodHostAuthGet, mustJSON(hostAuthGetRequest{AuthIndex: candidate.AuthIndex}))
			if err != nil {
				account.loadError = err
				account.errorCode = "CREDENTIAL_READ_ERROR"
			} else {
				var auth hostAuthGetResponse
				if err := json.Unmarshal(rawAuth, &auth); err != nil {
					account.loadError = err
					account.errorCode = "CREDENTIAL_DECODE_ERROR"
				} else {
					account.storage = auth.JSON
					applyCredentialProxy(&account.candidate, account.storage)
				}
			}
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accountCandidateLess(accounts[i].candidate, accounts[j].candidate)
	})
	accounts, err = assignStableAccountIDs(accounts)
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if account.loadError != nil {
			state.store.put(errorSnapshot(account.candidate, account.errorCode, account.loadError.Error()))
		}
	}
	return accounts, nil
}

type cpaConfig struct {
	OpenAICompatibility []cpaCompatibility `yaml:"openai-compatibility"`
	CodexAPIKeys        []cpaCodexAPIKey   `yaml:"codex-api-key"`
	ClaudeAPIKeys       []cpaGenericAPIKey `yaml:"claude-api-key"`
	GeminiAPIKeys       []cpaGenericAPIKey `yaml:"gemini-api-key"`
	InteractionsAPIKeys []cpaGenericAPIKey `yaml:"interactions-api-key"`
	XAIAPIKeys          []cpaGenericAPIKey `yaml:"xai-api-key"`
	VertexAPIKeys       []cpaGenericAPIKey `yaml:"vertex-api-key"`
}

type cpaCompatibility struct {
	Name          string           `yaml:"name"`
	BaseURL       string           `yaml:"base-url"`
	ProxyMode     string           `yaml:"proxy-mode"`
	ProxyURL      string           `yaml:"proxy-url"`
	Disabled      bool             `yaml:"disabled"`
	APIKeyEntries []cpaAPIKeyEntry `yaml:"api-key-entries"`
}

type cpaAPIKeyEntry struct {
	APIKey    string `yaml:"api-key"`
	ProxyMode string `yaml:"proxy-mode"`
	ProxyURL  string `yaml:"proxy-url"`
}

type cpaCodexAPIKey struct {
	APIKey    string `yaml:"api-key"`
	BaseURL   string `yaml:"base-url"`
	ProxyMode string `yaml:"proxy-mode"`
	ProxyURL  string `yaml:"proxy-url"`
	Disabled  bool   `yaml:"disabled"`
}

type cpaGenericAPIKey struct {
	APIKey    string `yaml:"api-key"`
	BaseURL   string `yaml:"base-url"`
	ProxyMode string `yaml:"proxy-mode"`
	ProxyURL  string `yaml:"proxy-url"`
	Disabled  bool   `yaml:"disabled"`
}

func discoverStaticAccounts() ([]staticAccount, error) {
	state.mu.RLock()
	path := state.cfg.SourceConfigPath
	state.mu.RUnlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CPA config: %w", err)
	}
	var config cpaConfig
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("decode CPA config: %w", err)
	}
	accounts := make([]staticAccount, 0)
	for providerIndex, provider := range config.OpenAICompatibility {
		if provider.Disabled || strings.TrimSpace(provider.BaseURL) == "" {
			continue
		}
		for keyIndex, entry := range provider.APIKeyEntries {
			key := strings.TrimSpace(entry.APIKey)
			if key == "" {
				continue
			}
			name := firstNonEmpty(provider.Name, hostname(provider.BaseURL), "OpenAI-compatible")
			if len(provider.APIKeyEntries) > 1 {
				name = fmt.Sprintf("%s #%d", name, keyIndex+1)
			}
			proxyMode, proxyURL, err := resolveStaticProxy(entry.ProxyMode, entry.ProxyURL, provider.ProxyMode, provider.ProxyURL)
			if err != nil {
				return nil, fmt.Errorf("OpenAI-compatible provider %q key %d proxy configuration: %w", provider.Name, keyIndex+1, err)
			}
			candidate := credentialCandidate{
				LegacyID:  fmt.Sprintf("config:openai-compatibility:%d:%d", providerIndex, keyIndex),
				AuthIndex: fmt.Sprintf("config:openai-compatibility:%d:%d", providerIndex, keyIndex),
				AuthID:    name,
				Name:      name,
				Provider:  provider.Name,
				BaseURL:   provider.BaseURL,
				ProxyMode: proxyMode,
				ProxyURL:  proxyURL,
				Source:    "static",
			}
			storage, _ := json.Marshal(map[string]string{"api_key": key})
			accounts = append(accounts, staticAccount{candidate: candidate, storage: storage})
		}
	}
	for keyIndex, entry := range config.CodexAPIKeys {
		key := strings.TrimSpace(entry.APIKey)
		if entry.Disabled || key == "" || strings.TrimSpace(entry.BaseURL) == "" {
			continue
		}
		name := fmt.Sprintf("Codex API #%d", keyIndex+1)
		proxyMode, proxyURL, err := resolveStaticProxy(entry.ProxyMode, entry.ProxyURL, "", "")
		if err != nil {
			return nil, fmt.Errorf("Codex API key %d proxy configuration: %w", keyIndex+1, err)
		}
		candidate := credentialCandidate{
			LegacyID:  fmt.Sprintf("config:codex-api-key:%d", keyIndex),
			AuthIndex: fmt.Sprintf("config:codex-api-key:%d", keyIndex),
			AuthID:    name,
			Name:      name,
			Provider:  "codex-api-key",
			BaseURL:   entry.BaseURL,
			ProxyMode: proxyMode,
			ProxyURL:  proxyURL,
			Source:    "static",
		}
		storage, _ := json.Marshal(map[string]string{"api_key": key})
		accounts = append(accounts, staticAccount{candidate: candidate, storage: storage})
	}
	generic := []struct {
		key   string
		label string
		items []cpaGenericAPIKey
	}{
		{key: "claude-api-key", label: "Claude API Key", items: config.ClaudeAPIKeys},
		{key: "gemini-api-key", label: "Gemini API Key", items: config.GeminiAPIKeys},
		{key: "interactions-api-key", label: "Interactions API Key", items: config.InteractionsAPIKeys},
		{key: "xai-api-key", label: "xAI API Key", items: config.XAIAPIKeys},
		{key: "vertex-api-key", label: "Vertex API Key", items: config.VertexAPIKeys},
	}
	for _, group := range generic {
		for index, entry := range group.items {
			key := strings.TrimSpace(entry.APIKey)
			if entry.Disabled || key == "" {
				continue
			}
			proxyMode, proxyURL, err := resolveStaticProxy(entry.ProxyMode, entry.ProxyURL, "", "")
			if err != nil {
				return nil, fmt.Errorf("%s %d proxy configuration: %w", group.label, index+1, err)
			}
			candidate := credentialCandidate{
				LegacyID:  fmt.Sprintf("config:%s:%d", group.key, index),
				AuthIndex: fmt.Sprintf("config:%s:%d", group.key, index),
				AuthID:    fmt.Sprintf("%s #%d", group.label, index+1),
				Name:      fmt.Sprintf("%s #%d", group.label, index+1),
				Provider:  group.key,
				BaseURL:   entry.BaseURL,
				ProxyMode: proxyMode,
				ProxyURL:  proxyURL,
				Source:    "static",
			}
			storage, _ := json.Marshal(map[string]string{"api_key": key})
			accounts = append(accounts, staticAccount{candidate: candidate, storage: storage})
		}
	}
	return assignStableAccountIDs(accounts)
}

func assignStableAccountIDs(accounts []staticAccount) ([]staticAccount, error) {
	if len(accounts) == 0 {
		return accounts, nil
	}
	if state.prefs == nil {
		return nil, fmt.Errorf("monitor preferences are unavailable")
	}
	key := state.prefs.preferenceKey()
	candidates := make([]credentialCandidate, len(accounts))
	for index, account := range accounts {
		candidate := account.candidate
		if len(key) == 32 && !(candidate.Source == "host" && candidate.SourceID != "") {
			if secret := tokenFromCredential(account.storage, nil); secret != "" {
				candidate.Fingerprint = credentialIdentityFingerprint(key, candidate.Source, candidate.SourceID, candidate.BaseURL, secret)
			}
		}
		candidates[index] = candidate
	}
	ids, err := state.prefs.resolveIdentities(candidates)
	if err != nil {
		return nil, err
	}
	for index := range accounts {
		accounts[index].candidate.AccountID = ids[index]
		accounts[index].candidate.Fingerprint = candidates[index].Fingerprint
	}
	return accounts, nil
}

func discoverProviderViews(callbackID string) ([]providerView, error) {
	accounts, err := discoverAllAccounts(callbackID)
	if err != nil {
		return nil, err
	}
	views := make([]providerView, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	order := make(map[string]int)
	for index, id := range state.prefs.accountOrder() {
		order[id] = index
	}
	for _, account := range accounts {
		id := candidateAccountID(account.candidate)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		adapter, normalized, matched := adapterFor(account.candidate)
		if !matched {
			adapter = ""
		}
		latest, hasLatest := state.store.get(id)
		var latestPtr *accountSnapshot
		if hasLatest {
			latestPtr = &latest
		}
		monitored := state.prefs.isMonitored(id, matched)
		if !matched {
			monitored = false
		}
		defaultName := firstNonEmpty(normalized.Name, account.candidate.Name)
		customName := state.prefs.name(id, "")
		name := firstNonEmpty(customName, defaultName)
		views = append(views, providerView{
			ID:                      id,
			Name:                    name,
			DefaultName:             defaultName,
			CustomName:              customName,
			Provider:                firstNonEmpty(normalized.Provider, account.candidate.Provider),
			BaseURL:                 firstNonEmpty(normalized.BaseURL, account.candidate.BaseURL),
			ProxyMode:               normalizedProxyMode(account.candidate.ProxyMode, account.candidate.ProxyURL),
			ProxyConfigured:         proxyIsConfigured(account.candidate.ProxyMode, account.candidate.ProxyURL),
			Adapter:                 adapter,
			AdapterOverride:         state.prefs.adapter(id, ""),
			Monitored:               monitored,
			ManagementPATConfigured: state.prefs.patConfigured(id),
			FirstSeenAt:             state.prefs.firstSeen(id),
			KeyHint:                 maskCredential(tokenFromCredential(account.storage, nil)),
			Latest:                  latestPtr,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		leftOrder, leftKnown := order[views[i].ID]
		rightOrder, rightKnown := order[views[j].ID]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		leftName := strings.ToLower(strings.TrimSpace(views[i].Name))
		rightName := strings.ToLower(strings.TrimSpace(views[j].Name))
		if leftName != rightName {
			return leftName < rightName
		}
		return views[i].ID < views[j].ID
	})
	return views, nil
}

func maskCredential(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "****"
	}
	return "****" + value[len(value)-4:]
}

func buildMonitorUIState(callbackID string) (monitorUIState, error) {
	providers, err := discoverProviderViews(callbackID)
	attemptedAt := time.Now().UTC()
	state.directoryMu.Lock()
	state.directoryAttemptAt = timePointer(attemptedAt)
	if err != nil {
		state.directoryError = err.Error()
		providers = cloneProviderViews(state.directoryViews)
	} else {
		state.directoryViews = cloneProviderViews(providers)
		state.directorySuccessAt = timePointer(attemptedAt)
		state.directoryError = ""
	}
	syncStatus := directorySyncStatus{
		Status:        "ok",
		LastAttemptAt: cloneTimePointer(state.directoryAttemptAt),
		LastSuccessAt: cloneTimePointer(state.directorySuccessAt),
		Error:         state.directoryError,
	}
	cachedProviderCount := len(state.directoryViews)
	state.directoryMu.Unlock()
	if err != nil {
		if cachedProviderCount == 0 {
			syncStatus.Status = "error"
		} else {
			syncStatus.Status = "error"
		}
	}
	if monitorStateNeedsRefresh(providers) {
		invalidateRefresh()
		maybeStartBackgroundRefresh(callbackID)
	}
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	thresholds := state.effectiveThresholds()
	refreshStatus.mu.RLock()
	refreshing := refreshStatus.running
	lastError := refreshStatus.lastErr
	finished := refreshStatus.finished
	refreshStatus.mu.RUnlock()
	var warnings []string
	if err := state.store.loadError(); err != nil {
		warnings = append(warnings, "快照缓存恢复失败，已保留原文件并停止自动覆盖："+err.Error())
	}
	return monitorUIState{
		Version:       pluginVersion,
		GeneratedAt:   time.Now().UTC(),
		Revision:      state.prefs.revision(),
		Refreshing:    refreshing,
		LastRefresh:   finished,
		LastError:     lastError,
		Warnings:      warnings,
		DirectorySync: syncStatus,
		Providers:     providers,
		Report:        buildReport(),
		Config: monitorConfigView{
			CacheTTLSeconds:      cfg.CacheTTLSeconds,
			SyncIntervalSeconds:  cfg.SyncIntervalSeconds,
			RequestTimeoutSecond: cfg.RequestTimeoutSecond,
			AccountTimeoutSecond: cfg.AccountTimeoutSecond,
			WarningPercent:       thresholds.WarningPercent,
			CriticalPercent:      thresholds.CriticalPercent,
			CashByCurrency:       thresholds.CashByCurrency,
			CashByAccount:        thresholds.CashByAccount,
		},
	}, nil
}

func cloneProviderViews(values []providerView) []providerView {
	out := make([]providerView, len(values))
	for index, value := range values {
		out[index] = value
		if value.Latest != nil {
			latest := *value.Latest
			out[index].Latest = &latest
		}
	}
	return out
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	out := value.UTC()
	return &out
}

func monitorStateNeedsRefresh(providers []providerView) bool {
	known := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		known[provider.ID] = struct{}{}
		if provider.Monitored && provider.Latest == nil {
			return true
		}
	}
	for _, snapshot := range state.store.list() {
		if _, ok := known[snapshot.AccountID]; !ok {
			return true
		}
	}
	return false
}

func maybeStartBackgroundRefresh(callbackID string) {
	_ = callbackID
	startBackgroundScheduler()
	wakeBackgroundScheduler()
}

func isSupportedAdapter(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "deepseek-balance", "zai-usage", "moonshot-balance", "kimi-coding", "opencode-go", "newapi-usage", "sub2api-usage", "relay-usage", "commandcode-goat":
		return true
	default:
		return false
	}
}

func errorSnapshot(candidate credentialCandidate, code, message string) accountSnapshot {
	now := time.Now().UTC()
	accountID := candidateAccountID(candidate)
	candidate.Name = state.prefs.name(accountID, candidate.Name)
	adapter, normalized, _ := adapterFor(candidate)
	if previous, ok := state.store.get(accountID); ok {
		previous = markStale(previous, now)
		previous.LastAttemptAt = now
		previous.Status = statusError
		previous.Error = &snapshotError{Code: code, Message: message}
		return previous
	}
	return accountSnapshot{
		AccountID:     accountID,
		AccountName:   firstNonEmpty(candidate.Name, normalized.Name, candidate.AuthID),
		Provider:      firstNonEmpty(candidate.Provider, normalized.Provider),
		AdapterID:     firstNonEmpty(adapter, "unknown"),
		BaseURL:       firstNonEmpty(candidate.BaseURL, normalized.BaseURL),
		Kind:          kindUnsupported,
		Status:        statusError,
		CheckedAt:     now,
		LastAttemptAt: now,
		Error:         &snapshotError{Code: code, Message: message},
	}
}

func accountCandidateLess(left, right credentialCandidate) bool {
	leftName := strings.ToLower(strings.TrimSpace(left.Name))
	rightName := strings.ToLower(strings.TrimSpace(right.Name))
	if leftName != rightName {
		return leftName < rightName
	}
	return left.AuthIndex < right.AuthIndex
}

func (s *snapshotStore) missing(id string) bool {
	if s == nil || id == "" {
		return true
	}
	_, ok := s.get(id)
	return !ok
}

func (s *pluginState) storeMissing(id string) bool {
	if s == nil {
		return true
	}
	return s.store.missing(id)
}

func invalidateRefresh() {
	refreshMu.Lock()
	lastRefresh = time.Time{}
	refreshMu.Unlock()
}

func queryValue(values map[string][]string, key string) string {
	if values == nil || len(values[key]) == 0 {
		return ""
	}
	return strings.TrimSpace(values[key][0])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func handleConfigRoute(req managementRequest) ([]byte, error) {
	if req.Method == http.MethodPut {
		var request struct {
			BaseRevision    *int64                   `json:"base_revision,omitempty"`
			WarningPercent  *float64                 `json:"warning_percent,omitempty"`
			CriticalPercent *float64                 `json:"critical_percent,omitempty"`
			CashByCurrency  map[string]cashThreshold `json:"cash_by_currency,omitempty"`
			CashByAccount   map[string]cashThreshold `json:"cash_by_account,omitempty"`
		}
		if err := json.Unmarshal(req.Body, &request); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid monitor settings"})
		}
		thresholds := state.effectiveThresholds()
		if request.WarningPercent != nil {
			thresholds.WarningPercent = *request.WarningPercent
		}
		if request.CriticalPercent != nil {
			thresholds.CriticalPercent = *request.CriticalPercent
		}
		if request.CashByCurrency != nil {
			normalized, err := normalizeCashThresholds(request.CashByCurrency)
			if err != nil {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			thresholds.CashByCurrency = normalized
		}
		if request.CashByAccount != nil {
			normalized, err := normalizeAccountCashThresholds(request.CashByAccount)
			if err != nil {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			thresholds.CashByAccount = normalized
		}
		if err := validateThresholdPercentages(thresholds); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		if _, err := state.prefs.updateThresholdsAtRevision(request.BaseRevision, thresholds); err != nil {
			var conflict *revisionConflictError
			if errors.As(err, &conflict) {
				return jsonResponse(http.StatusConflict, map[string]any{
					"error":         "monitor settings changed; reload and try again",
					"base_revision": conflict.Expected,
					"revision":      conflict.Actual,
				})
			}
			var persistErr *preferencePersistenceError
			if errors.As(err, &persistErr) {
				return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		state.store.reapplyActive(applyThresholds)
		value, err := buildMonitorUIState(req.HostCallbackID)
		if err != nil {
			return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		state.mu.RLock()
		cachePath := state.cfg.CachePath
		state.mu.RUnlock()
		if err := state.store.save(cachePath); err != nil {
			value.Warnings = append(value.Warnings, "监控设置已保存，但快照持久化失败："+err.Error())
		}
		return jsonResponse(http.StatusOK, value)
	}
	if req.Method != http.MethodGet {
		return jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
	value, err := buildMonitorUIState(req.HostCallbackID)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, value)
}

func validateThresholdPercentages(thresholds thresholdConfig) error {
	if thresholds.WarningPercent <= 0 || thresholds.WarningPercent > 100 {
		return fmt.Errorf("warning_percent must be greater than 0 and at most 100")
	}
	if thresholds.CriticalPercent <= 0 || thresholds.CriticalPercent >= thresholds.WarningPercent {
		return fmt.Errorf("critical_percent must be greater than 0 and less than warning_percent")
	}
	return nil
}

func (s *pluginState) effectiveThresholds() thresholdConfig {
	s.mu.RLock()
	fallback := s.cfg.Thresholds
	s.mu.RUnlock()
	if s.prefs == nil {
		return fallback
	}
	return s.prefs.thresholds(fallback)
}

func buildReport() report {
	accounts := state.store.list()
	if state.prefs != nil {
		filtered := accounts[:0]
		for _, account := range accounts {
			if state.prefs.isMonitored(account.AccountID, true) {
				filtered = append(filtered, account)
			}
		}
		accounts = filtered
	}
	return buildReportFromAccounts(accounts)
}

func buildReportFromAccounts(accounts []accountSnapshot) report {
	out := report{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Status: statusOK, Accounts: accounts}
	for _, account := range accounts {
		out.Summary.Total++
		if account.Stale {
			out.Summary.Stale++
		}
		switch account.Status {
		case statusOK:
			out.Summary.OK++
		case statusWarning:
			out.Summary.Warning++
		case statusCritical:
			out.Summary.Critical++
		case statusError:
			out.Summary.Error++
		case statusUnknown:
			out.Summary.Unknown++
		}
		switch account.Kind {
		case kindBalance:
			out.Summary.Balances++
		case kindPeriodQuota:
			out.Summary.PeriodQuotas++
		case kindQuota:
			out.Summary.Quotas++
		}
		out.Alerts = append(out.Alerts, alertsForAccount(account)...)
		effective := account.Status
		if effective == statusDisabled {
			effective = statusCritical
		}
		if account.Stale {
			effective = maxStatus(effective, statusWarning)
		}
		out.Status = maxStatus(out.Status, effective)
	}
	return out
}

func alertsForAccount(account accountSnapshot) []reportAlert {
	alerts := make([]reportAlert, 0, 2)
	if account.Stale {
		alerts = append(alerts, reportAlert{Level: statusWarning, AccountID: account.AccountID, Type: "stale", Code: "STALE", Message: "正在使用上次成功获取的数据，后台会继续重试。"})
	}
	if account.Error != nil && account.Error.Code != "" {
		level := account.Status
		if level == statusDisabled {
			level = statusCritical
		}
		alerts = append(alerts, reportAlert{
			Level:      level,
			AccountID:  account.AccountID,
			Type:       "availability",
			Code:       account.Error.Code,
			Message:    availabilityAlertMessage(account.Error.Code),
			Diagnostic: account.Error.Message,
		})
	}
	sectionNames := make([]string, 0, len(account.Sections))
	for name, section := range account.Sections {
		if section.Status == "error" {
			sectionNames = append(sectionNames, name)
		}
	}
	sort.Strings(sectionNames)
	for _, name := range sectionNames {
		section := account.Sections[name]
		alerts = append(alerts, reportAlert{
			Level:      statusWarning,
			AccountID:  account.AccountID,
			MetricID:   name,
			Type:       "section",
			Code:       section.ErrorCode,
			Message:    sectionAlertMessage(name),
			Diagnostic: section.Message,
			OccurredAt: section.UpdatedAt,
		})
	}
	if account.Kind == kindUnsupported {
		return alerts
	}
	if account.Status == statusCritical || account.Status == statusWarning {
		thresholds := state.effectiveThresholds()
		if account.Kind == kindBalance {
			if level := cashThresholdStatus(account, thresholds); level != statusOK {
				alerts = append(alerts, reportAlert{Level: level, AccountID: account.AccountID, Type: "balance_low", Code: "BALANCE_BELOW_THRESHOLD", Message: "余额低于配置阈值"})
			}
		} else {
			windowAlerts := 0
			for _, window := range account.Windows {
				if window.RemainingFraction == nil {
					continue
				}
				percent := *window.RemainingFraction * 100
				level := statusOK
				threshold := 0.0
				code := ""
				if percent <= thresholds.CriticalPercent {
					level = statusCritical
					threshold = thresholds.CriticalPercent
					code = "QUOTA_BELOW_CRITICAL"
				} else if percent <= thresholds.WarningPercent {
					level = statusWarning
					threshold = thresholds.WarningPercent
					code = "QUOTA_BELOW_WARNING"
				}
				if level == statusOK {
					continue
				}
				windowID := firstNonEmpty(window.Name, "unknown")
				alerts = append(alerts, reportAlert{
					Level:     level,
					AccountID: account.AccountID,
					WindowID:  windowID,
					Type:      "quota_low",
					Code:      code,
					Message:   "额度剩余低于配置阈值",
					Current:   decimalString(percent),
					Unit:      "%",
					Threshold: decimalString(threshold),
				})
				windowAlerts++
			}
			if windowAlerts == 0 && len(sectionNames) == 0 {
				alerts = append(alerts, reportAlert{Level: account.Status, AccountID: account.AccountID, Type: "quota_low", Code: "QUOTA_BELOW_THRESHOLD", Message: "额度剩余低于配置阈值"})
			}
		}
	}
	for index := range alerts {
		alerts[index].ID = stableAlertID(alerts[index])
	}
	return alerts
}

func availabilityAlertMessage(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "KEY_EXPIRED":
		return "上游 Key 已过期。"
	case "KEY_DISABLED":
		return "上游已禁用该 Key 或账户。"
	case "KEY_INVALID":
		return "上游返回该 Key 无效。"
	case "PAT_NOT_CONFIGURED":
		return "尚未配置管理凭据，无法查询账户额度。"
	case "STALE":
		return "正在使用上次成功获取的数据，后台会继续重试。"
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(code)), "UPSTREAM_HTTP_") {
		return "上游接口暂时无法确认当前状态，请稍后重试。"
	}
	return "上游账户暂时无法确认当前状态，请稍后重试。"
}

func sectionAlertMessage(section string) string {
	switch section {
	case "balance":
		return "余额数据查询失败，其他可用数据仍会保留。"
	case "billing":
		return "计费数据查询失败，其他可用数据仍会保留。"
	case "key_quota":
		return "当前 Key 额度查询失败，其他可用数据仍会保留。"
	case "account_quota":
		return "账户额度查询失败，其他可用数据仍会保留。"
	case "usage":
		return "用量统计数据查询失败，其他可用数据仍会保留。"
	case "subscription":
		return "套餐信息查询失败，其他可用数据仍会保留。"
	default:
		return "部分账户数据查询失败，其他可用数据仍会保留。"
	}
}

func applyThresholds(snapshot accountSnapshot) accountSnapshot {
	if snapshot.Kind == kindUnsupported {
		return snapshot
	}
	if snapshot.baseStatus == "" {
		snapshot.baseStatus = inferSnapshotBaseStatus(snapshot)
	}
	status := snapshot.baseStatus
	switch status {
	case statusError, statusUnknown, statusDisabled:
		snapshot.Status = status
		return snapshot
	}
	thresholds := state.effectiveThresholds()
	if thresholdStatus := cashThresholdStatus(snapshot, thresholds); thresholdStatus != statusOK {
		status = maxStatus(status, thresholdStatus)
	}
	minimum := 1.0
	found := false
	overage := false
	for _, window := range snapshot.Windows {
		if excess, ok := numericValue(window.ExcessAmount); ok && excess > 0 {
			overage = true
		}
		if window.RemainingFraction != nil {
			minimum = minFloat(minimum, *window.RemainingFraction)
			found = true
		}
	}
	for _, quantity := range snapshot.Quantities {
		if quantity.Unlimited {
			continue
		}
		remaining, okRemaining := numericValue(quantity.Remaining)
		total, okTotal := numericValue(quantity.Total)
		if okRemaining && okTotal && total > 0 {
			if used, okUsed := numericValue(quantity.Used); okUsed && used > total {
				overage = true
			}
			minimum = minFloat(minimum, remaining/total)
			found = true
		}
	}
	if overage {
		snapshot.Status = maxStatus(status, statusCritical)
		return snapshot
	}
	if !found {
		snapshot.Status = status
		return snapshot
	}
	if minimum*100 <= thresholds.CriticalPercent {
		status = maxStatus(status, statusCritical)
	} else if minimum*100 <= thresholds.WarningPercent {
		status = maxStatus(status, statusWarning)
	}
	snapshot.Status = status
	return snapshot
}

func cashThresholdStatus(snapshot accountSnapshot, thresholds thresholdConfig) accountStatus {
	status := statusOK
	for _, balance := range snapshot.Balances {
		amount, ok := numericValue(balance.Amount)
		if !ok {
			continue
		}
		if amount < 0 {
			status = statusCritical
			continue
		}
		rule, configured := thresholds.CashByAccount[strings.TrimSpace(snapshot.AccountID)]
		if !configured {
			rule, configured = thresholds.CashByCurrency[strings.ToUpper(strings.TrimSpace(balance.Currency))]
		}
		if !configured {
			continue
		}
		warning, warningOK := numericValue(rule.Warning)
		critical, criticalOK := numericValue(rule.Critical)
		if !warningOK || !criticalOK {
			continue
		}
		switch {
		case amount <= critical:
			status = statusCritical
		case amount <= warning && status != statusCritical:
			status = statusWarning
		}
	}
	return status
}

func maxStatus(current, candidate accountStatus) accountStatus {
	rank := func(status accountStatus) int {
		switch status {
		case statusError:
			return 6
		case statusCritical:
			return 5
		case statusWarning:
			return 4
		case statusUnknown:
			return 3
		case statusDisabled:
			return 2
		case statusOK:
			return 1
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return candidate
	}
	return current
}

func normalizeCashThresholds(values map[string]cashThreshold) (map[string]cashThreshold, error) {
	return normalizeCashThresholdMap(values, true)
}

func normalizeAccountCashThresholds(values map[string]cashThreshold) (map[string]cashThreshold, error) {
	return normalizeCashThresholdMap(values, false)
}

func normalizeCashThresholdMap(values map[string]cashThreshold, uppercaseKeys bool) (map[string]cashThreshold, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]cashThreshold, len(values))
	for rawKey, rawRule := range values {
		key := strings.TrimSpace(rawKey)
		if uppercaseKeys {
			key = strings.ToUpper(key)
		}
		if key == "" {
			return nil, fmt.Errorf("阈值键不能为空")
		}
		warning, err := parseThresholdAmount("warning", rawRule.Warning)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		critical, err := parseThresholdAmount("critical", rawRule.Critical)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if critical > warning {
			return nil, fmt.Errorf("%s: critical 不能大于 warning", key)
		}
		out[key] = cashThreshold{Warning: decimalString(warning), Critical: decimalString(critical)}
	}
	return out, nil
}

func parseThresholdAmount(name, raw string) (float64, error) {
	value, ok := numericValue(raw)
	if !ok {
		return 0, fmt.Errorf("%s 金额无效", name)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s 金额不能为负数", name)
	}
	return value, nil
}

func minFloat(left, right float64) float64 {
	if right < left {
		return right
	}
	return left
}

func validReadToken(headers http.Header) bool {
	state.mu.RLock()
	envName := state.cfg.ReadTokenEnv
	state.mu.RUnlock()
	want := strings.TrimSpace(os.Getenv(envName))
	if want == "" {
		return false
	}
	raw := strings.TrimSpace(headers.Get("Authorization"))
	if !strings.HasPrefix(raw, "Bearer ") {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	if got == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func tokenFromCredential(storage []byte, attributes map[string]string) string {
	for _, key := range []string{"api_key", "api-key", "access_token", "token", "key"} {
		if value := strings.TrimSpace(attributes[key]); value != "" {
			return value
		}
	}
	var object map[string]any
	if json.Unmarshal(storage, &object) != nil {
		return ""
	}
	for _, key := range []string{"api_key", "api-key", "access_token", "token", "key"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func hostHTTPDo(callbackID, endpoint string, headers map[string][]string) (hostHTTPResponse, error) {
	payload, err := json.Marshal(hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodGet,
		URL:            endpoint,
		Headers:        headers,
	})
	if err != nil {
		return hostHTTPResponse{}, err
	}
	raw, err := callHost(pluginabi.MethodHostHTTPDo, payload)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	var response hostHTTPResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return hostHTTPResponse{}, err
	}
	return response, nil
}

func callHost(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}
	var response C.cliproxy_buffer
	if C.call_host_api(cMethod, request, C.size_t(len(payload)), &response) != 0 || response.ptr == nil {
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	raw := C.GoBytes(response.ptr, C.int(response.len))
	C.free_host_buffer(response.ptr, response.len)
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return append([]byte(nil), env.Result...), nil
}

func jsonResponse(status int, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	})
}

func htmlResponse(status int, body string) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
	})
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

//go:embed ui.html
var uiHTML string
