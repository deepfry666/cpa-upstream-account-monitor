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
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
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

var pluginVersion = "0.4.1"

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
	RequestTimeoutSecond int                      `yaml:"request_timeout_seconds"`
	ReadTokenEnv         string                   `yaml:"read_token_env"`
	Monitors             map[string]monitorConfig `yaml:"monitors"`
	SourceConfigPath     string                   `yaml:"source_config_path"`
	CachePath            string                   `yaml:"cache_path"`
	PreferencesPath      string                   `yaml:"preferences_path"`
	Thresholds           thresholdConfig          `yaml:"thresholds"`
}

type pluginState struct {
	mu            sync.RWMutex
	cfg           pluginConfig
	store         *snapshotStore
	prefs         *monitorPrefs
	schemaVersion uint32
}

var state = &pluginState{
	cfg: pluginConfig{
		Enabled:              true,
		CacheTTLSeconds:      300,
		RequestTimeoutSecond: 8,
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
	schemaVersion: 5,
}

var refreshMu sync.Mutex
var lastRefresh time.Time

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
func cliproxyPluginShutdown() {}

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
	var cfg pluginConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.CacheTTLSeconds <= 0 {
		cfg.CacheTTLSeconds = 300
	}
	if cfg.RequestTimeoutSecond <= 0 {
		cfg.RequestTimeoutSecond = 8
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
	state.mu.Lock()
	state.cfg = cfg
	state.mu.Unlock()
	if err := state.prefs.configure(cfg.PreferencesPath); err != nil {
		return err
	}
	_ = state.store.load(cfg.CachePath)
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
				{Name: "request_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Timeout for one upstream query."},
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
		AuthIndex: req.AuthIndex,
		AuthID:    req.AuthID,
		Provider:  req.Provider,
	}
	if req.Attributes != nil {
		candidate.BaseURL = strings.TrimSpace(req.Attributes["base_url"])
	}
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

func queryDeepSeekSnapshot(callbackID string, candidate credentialCandidate, storage []byte, attributes map[string]string) (accountSnapshot, error) {
	token := tokenForCandidate(candidate, storage, attributes)
	if token == "" {
		return accountSnapshot{}, fmt.Errorf("credential token is unavailable")
	}
	endpoint, err := deepSeekURL(candidate.BaseURL)
	if err != nil {
		return accountSnapshot{}, err
	}
	started := time.Now()
	httpResponse, err := queryEndpoint(callbackID, endpoint, token, candidate.ProxyURL)
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
		if err := refreshDiscoveredAccounts(req.HostCallbackID, true); err != nil {
			return jsonResponse(http.StatusBadGateway, map[string]string{"error": err.Error()})
		}
		return jsonResponse(http.StatusOK, buildReport())
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
	ID                 string  `json:"id"`
	CustomName         *string `json:"custom_name,omitempty"`
	Monitored          *bool   `json:"monitored,omitempty"`
	Adapter            string  `json:"adapter,omitempty"`
	ClearAdapter       bool    `json:"clear_adapter,omitempty"`
	ManagementPAT      string  `json:"management_pat,omitempty"`
	ClearManagementPAT bool    `json:"clear_management_pat,omitempty"`
}

type providerMutationRequest struct {
	Providers []providerMutation `json:"providers"`
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
	for _, mutation := range request.Providers {
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
		wasMonitored := state.prefs.isMonitored(mutation.ID, false)
		if err := state.prefs.update(mutation.ID, mutation.Monitored, mutation.Adapter, mutation.ManagementPAT, mutation.ClearManagementPAT, mutation.ClearAdapter, mutation.CustomName); err != nil {
			return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		if mutation.CustomName != nil {
			state.store.rename(mutation.ID, state.prefs.name(mutation.ID, defaults[mutation.ID]))
		}
		if mutation.Monitored != nil && !*mutation.Monitored {
			state.store.delete(mutation.ID)
		} else if mutation.Monitored != nil && *mutation.Monitored && (!wasMonitored || state.storeMissing(mutation.ID)) {
			invalidateRefresh()
		}
	}
	state.mu.RLock()
	cachePath := state.cfg.CachePath
	state.mu.RUnlock()
	_ = state.store.save(cachePath)
	value, err := buildMonitorUIState(req.HostCallbackID)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, value)
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
	refreshMu.Lock()
	defer refreshMu.Unlock()
	state.mu.RLock()
	cacheTTL := time.Duration(state.cfg.CacheTTLSeconds) * time.Second
	cachePath := state.cfg.CachePath
	state.mu.RUnlock()
	if !force && !lastRefresh.IsZero() && time.Since(lastRefresh) < cacheTTL {
		return nil
	}
	started := time.Now().UTC()
	refreshStatus.mu.Lock()
	refreshStatus.running = true
	refreshStatus.started = &started
	refreshStatus.lastErr = ""
	refreshStatus.mu.Unlock()
	var refreshErr error
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
	if err != nil {
		refreshErr = err
		return refreshErr
	}
	byID := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		byID[account.candidate.AuthIndex] = struct{}{}
	}
	staticAccounts, err := discoverStaticAccounts()
	if err != nil {
		refreshErr = err
		return refreshErr
	}
	for _, account := range staticAccounts {
		if _, duplicate := byID[account.candidate.AuthIndex]; duplicate {
			continue
		}
		byID[account.candidate.AuthIndex] = struct{}{}
		accounts = append(accounts, account)
	}
	defaults := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		_, _, matched := adapterFor(account.candidate)
		defaults[account.candidate.AuthIndex] = matched
	}
	state.prefs.reconcileDefaults(defaults)
	keep := make(map[string]struct{}, len(accounts))
	selected := make([]staticAccount, 0, len(accounts))
	for _, account := range accounts {
		if _, _, matched := adapterFor(account.candidate); !matched {
			continue
		}
		if !state.prefs.isMonitored(account.candidate.AuthIndex, true) {
			continue
		}
		keep[account.candidate.AuthIndex] = struct{}{}
		selected = append(selected, account)
	}
	var workers sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, account := range selected {
		account := account
		workers.Add(1)
		go func() {
			defer workers.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			snapshot, err := queryCandidate(callbackID, account.candidate, account.storage, nil)
			if err != nil {
				state.store.put(errorSnapshot(account.candidate, "UPSTREAM_REQUEST_FAILED", err.Error()))
				return
			}
			state.store.put(snapshot)
		}()
	}
	workers.Wait()
	state.store.prune(keep)
	lastRefresh = time.Now()
	_ = state.store.save(cachePath)
	return nil
}

type staticAccount struct {
	candidate credentialCandidate
	storage   []byte
}

func discoverAllAccounts(callbackID string) ([]staticAccount, error) {
	hostAccounts, hostErr := discoverHostAccounts(callbackID, true)
	if hostErr != nil && strings.TrimSpace(callbackID) != "" {
		return nil, hostErr
	}
	accounts := append([]staticAccount(nil), hostAccounts...)
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		seen[account.candidate.AuthIndex] = struct{}{}
	}
	staticAccounts, err := discoverStaticAccounts()
	if err != nil {
		return nil, err
	}
	for _, account := range staticAccounts {
		if _, duplicate := seen[account.candidate.AuthIndex]; duplicate {
			continue
		}
		seen[account.candidate.AuthIndex] = struct{}{}
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
			AuthIndex: entry.AuthIndex,
			AuthID:    entry.ID,
			Name:      firstNonEmpty(entry.Label, entry.Name, entry.ID),
			Provider:  firstNonEmpty(entry.Provider, entry.Type),
			BaseURL:   entry.BaseURL,
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
				state.store.put(errorSnapshot(candidate, "CREDENTIAL_READ_ERROR", err.Error()))
			} else {
				var auth hostAuthGetResponse
				if err := json.Unmarshal(rawAuth, &auth); err != nil {
					state.store.put(errorSnapshot(candidate, "CREDENTIAL_DECODE_ERROR", err.Error()))
				} else {
					account.storage = auth.JSON
				}
			}
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accountCandidateLess(accounts[i].candidate, accounts[j].candidate)
	})
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
	ProxyURL      string           `yaml:"proxy-url"`
	Disabled      bool             `yaml:"disabled"`
	APIKeyEntries []cpaAPIKeyEntry `yaml:"api-key-entries"`
}

type cpaAPIKeyEntry struct {
	APIKey   string `yaml:"api-key"`
	ProxyURL string `yaml:"proxy-url"`
}

type cpaCodexAPIKey struct {
	APIKey   string `yaml:"api-key"`
	BaseURL  string `yaml:"base-url"`
	ProxyURL string `yaml:"proxy-url"`
	Disabled bool   `yaml:"disabled"`
}

type cpaGenericAPIKey struct {
	APIKey   string `yaml:"api-key"`
	BaseURL  string `yaml:"base-url"`
	ProxyURL string `yaml:"proxy-url"`
	Disabled bool   `yaml:"disabled"`
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
			candidate := credentialCandidate{
				AuthIndex: fmt.Sprintf("config:openai-compatibility:%d:%d", providerIndex, keyIndex),
				AuthID:    name,
				Name:      name,
				Provider:  provider.Name,
				BaseURL:   provider.BaseURL,
				ProxyURL:  firstNonEmpty(entry.ProxyURL, provider.ProxyURL),
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
		candidate := credentialCandidate{
			AuthIndex: fmt.Sprintf("config:codex-api-key:%d", keyIndex),
			AuthID:    name,
			Name:      name,
			Provider:  "codex-api-key",
			BaseURL:   entry.BaseURL,
			ProxyURL:  entry.ProxyURL,
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
			candidate := credentialCandidate{
				AuthIndex: fmt.Sprintf("config:%s:%d", group.key, index),
				AuthID:    fmt.Sprintf("%s #%d", group.label, index+1),
				Name:      fmt.Sprintf("%s #%d", group.label, index+1),
				Provider:  group.key,
				BaseURL:   entry.BaseURL,
				ProxyURL:  entry.ProxyURL,
			}
			storage, _ := json.Marshal(map[string]string{"api_key": key})
			accounts = append(accounts, staticAccount{candidate: candidate, storage: storage})
		}
	}
	return accounts, nil
}

func discoverProviderViews(callbackID string) ([]providerView, error) {
	accounts, err := discoverAllAccounts(callbackID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.candidate.AuthIndex)
	}
	state.prefs.reconcile(ids)
	views := make([]providerView, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		id := account.candidate.AuthIndex
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
			ProxyConfigured:         strings.TrimSpace(account.candidate.ProxyURL) != "",
			Adapter:                 adapter,
			AdapterOverride:         state.prefs.adapter(id, ""),
			Monitored:               monitored,
			ManagementPATConfigured: state.prefs.patConfigured(id),
			KeyHint:                 maskCredential(tokenFromCredential(account.storage, nil)),
			Latest:                  latestPtr,
		})
	}
	sort.Slice(views, func(i, j int) bool {
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
	if err != nil {
		return monitorUIState{}, err
	}
	if monitorStateNeedsRefresh(providers) {
		invalidateRefresh()
		maybeStartBackgroundRefresh(callbackID)
	}
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	refreshStatus.mu.RLock()
	refreshing := refreshStatus.running
	lastError := refreshStatus.lastErr
	finished := refreshStatus.finished
	refreshStatus.mu.RUnlock()
	return monitorUIState{
		Version:     pluginVersion,
		GeneratedAt: time.Now().UTC(),
		Refreshing:  refreshing,
		LastRefresh: finished,
		LastError:   lastError,
		Providers:   providers,
		Report:      buildReport(),
		Config: monitorConfigView{
			CacheTTLSeconds:      cfg.CacheTTLSeconds,
			RequestTimeoutSecond: cfg.RequestTimeoutSecond,
			WarningPercent:       cfg.Thresholds.WarningPercent,
			CriticalPercent:      cfg.Thresholds.CriticalPercent,
		},
	}, nil
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
	refreshStatus.mu.RLock()
	running := refreshStatus.running
	refreshStatus.mu.RUnlock()
	if running {
		return
	}
	refreshMu.Lock()
	state.mu.RLock()
	ttl := time.Duration(state.cfg.CacheTTLSeconds) * time.Second
	state.mu.RUnlock()
	stale := lastRefresh.IsZero() || time.Since(lastRefresh) >= ttl
	refreshMu.Unlock()
	if stale {
		go func() { _ = refreshDiscoveredAccounts(callbackID, false) }()
	}
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
	candidate.Name = state.prefs.name(candidate.AuthIndex, candidate.Name)
	adapter, normalized, _ := adapterFor(candidate)
	if previous, ok := state.store.get(candidate.AuthIndex); ok {
		previous = markStale(previous, time.Now().UTC())
		previous.Status = statusError
		previous.Error = &snapshotError{Code: code, Message: message}
		return previous
	}
	now := time.Now().UTC()
	return accountSnapshot{
		AccountID:   candidate.AuthIndex,
		AccountName: firstNonEmpty(candidate.Name, normalized.Name, candidate.AuthID),
		Provider:    firstNonEmpty(candidate.Provider, normalized.Provider),
		AdapterID:   firstNonEmpty(adapter, "unknown"),
		BaseURL:     firstNonEmpty(candidate.BaseURL, normalized.BaseURL),
		Kind:        kindUnsupported,
		Status:      statusError,
		CheckedAt:   now,
		Error:       &snapshotError{Code: code, Message: message},
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
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	if req.Method == http.MethodPut && len(req.Body) > 0 {
		if err := configure(req.Body); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		state.mu.RLock()
		cfg = state.cfg
		state.mu.RUnlock()
	}
	return jsonResponse(http.StatusOK, cfg)
}

func buildReport() report {
	accounts := state.store.list()
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
		if account.Status == statusWarning || account.Status == statusCritical || account.Status == statusError {
			out.Status = account.Status
		} else if account.Status == statusUnknown && out.Status == statusOK {
			out.Status = statusUnknown
		}
	}
	return out
}

func alertsForAccount(account accountSnapshot) []reportAlert {
	alerts := make([]reportAlert, 0, 2)
	if account.Stale {
		alerts = append(alerts, reportAlert{Level: statusWarning, AccountID: account.AccountID, Type: "stale", Message: "using the last successful snapshot"})
	}
	if account.Kind == kindUnsupported {
		return alerts
	}
	if account.Status == statusCritical || account.Status == statusWarning {
		alertType := "quota_low"
		message := "quota remaining is below the configured threshold"
		if account.Kind == kindBalance {
			alertType = "balance_low"
			message = "balance is empty or below the configured threshold"
		}
		alerts = append(alerts, reportAlert{Level: account.Status, AccountID: account.AccountID, Type: alertType, Message: message})
	}
	return alerts
}

func applyThresholds(snapshot accountSnapshot) accountSnapshot {
	if snapshot.Kind == kindUnsupported || snapshot.Status != statusOK {
		return snapshot
	}
	state.mu.RLock()
	thresholds := state.cfg.Thresholds
	state.mu.RUnlock()
	minimum := 1.0
	found := false
	for _, window := range snapshot.Windows {
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
			minimum = minFloat(minimum, remaining/total)
			found = true
		}
	}
	if !found {
		return snapshot
	}
	if minimum*100 <= thresholds.CriticalPercent {
		snapshot.Status = statusCritical
	} else if minimum*100 <= thresholds.WarningPercent {
		snapshot.Status = statusWarning
	}
	return snapshot
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
