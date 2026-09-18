package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errNewAPIAccountPATMissing = errors.New("management credential is not configured")

type monitorConfig struct {
	Adapter             string `yaml:"adapter"`
	BaseURL             string `yaml:"base_url"`
	ManagementBaseURL   string `yaml:"management_base_url"`
	AllowInternalHTTP   bool   `yaml:"allow_internal_http"`
	APIKeyEnv           string `yaml:"api_key_env"`
	ManagementAPIKeyEnv string `yaml:"management_api_key_env"`
	ManagementUserIDEnv string `yaml:"management_user_id_env"`
	ProxyMode           string `yaml:"proxy_mode"`
	ProxyURL            string `yaml:"proxy_url"`
}

type endpointOptions struct {
	AllowInternalHTTP    bool
	StripInferenceSuffix bool
}

func validateMonitorConfigs(monitors map[string]monitorConfig) error {
	keys := make([]string, 0, len(monitors))
	for key := range monitors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		config := monitors[key]
		if strings.TrimSpace(config.ProxyMode) == "" && strings.TrimSpace(config.ProxyURL) != "" {
			return fmt.Errorf("监控账户 %q 配置 proxy_url 时必须指定 proxy_mode", key)
		}
		if strings.TrimSpace(config.ProxyMode) != "" {
			proxyMode, err := normalizeProxyMode(config.ProxyMode)
			if err != nil {
				return fmt.Errorf("监控账户 %q 的代理配置无效: %w", key, err)
			}
			if proxyMode == proxyModeURL && strings.TrimSpace(config.ProxyURL) == "" {
				return fmt.Errorf("监控账户 %q 指定代理模式必须提供 proxy_url", key)
			}
			if proxyMode != proxyModeURL && strings.TrimSpace(config.ProxyURL) != "" {
				return fmt.Errorf("监控账户 %q 在 %s 模式不能配置 proxy_url", key, proxyMode)
			}
			if proxyMode == proxyModeURL {
				if err := validateProxyURL(config.ProxyURL); err != nil {
					return fmt.Errorf("监控账户 %q 的代理 URL 配置无效: %w", key, err)
				}
			}
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "base_url", value: config.BaseURL},
			{name: "management_base_url", value: config.ManagementBaseURL},
		} {
			if err := validateConfiguredEndpointURL(field.value, config.AllowInternalHTTP); err != nil {
				return fmt.Errorf("监控账户 %q 的 %s 配置无效: %w", key, field.name, err)
			}
		}
	}
	return nil
}

func validateConfiguredEndpointURL(raw string, allowInternalHTTP bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("必须是有效的绝对 URL")
	}
	if u.User != nil {
		return fmt.Errorf("URL 不能包含用户信息")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowInternalHTTP {
			return nil
		}
		return fmt.Errorf("必须使用 HTTPS；如需访问内部 HTTP 服务，请显式开启 allow_internal_http")
	default:
		return fmt.Errorf("仅支持 HTTP 或 HTTPS")
	}
}

type newAPIQuotaStatus struct {
	QuotaPerUnit               float64
	DisplayType                string
	USDExchangeRate            float64
	CustomCurrencySymbol       string
	CustomCurrencyExchangeRate float64
	Fallback                   bool
}

func defaultNewAPIQuotaStatus() newAPIQuotaStatus {
	return newAPIQuotaStatus{DisplayType: "quota", Fallback: true}
}

func monitorFor(candidate credentialCandidate) monitorConfig {
	state.mu.RLock()
	monitors := state.cfg.Monitors
	state.mu.RUnlock()
	config := monitorConfig{}
	for _, key := range []string{candidateAccountID(candidate), candidate.AuthIndex, candidate.AuthID, candidate.Provider} {
		if value, ok := monitors[key]; ok {
			config = value
			break
		}
	}
	if state.prefs != nil {
		if adapter := state.prefs.adapter(candidateAccountID(candidate), ""); adapter != "" {
			config.Adapter = adapter
		}
	}
	return config
}

func adapterFor(candidate credentialCandidate) (string, credentialCandidate, bool) {
	p := strings.ToLower(strings.TrimSpace(candidate.Provider))
	host := hostname(candidate.BaseURL)
	commandCode := matchesCommandCode(p, host)

	config := monitorFor(candidate)
	candidate.ManagementBaseURL = firstNonEmpty(config.ManagementBaseURL, candidate.ManagementBaseURL)
	candidate.AllowInternalHTTP = candidate.AllowInternalHTTP || config.AllowInternalHTTP
	if proxyMode, err := normalizeProxyMode(config.ProxyMode); err == nil {
		switch proxyMode {
		case proxyModeDirect:
			candidate.ProxyMode = proxyModeDirect
			candidate.ProxyURL = ""
		case proxyModeURL:
			candidate.ProxyMode = proxyModeURL
			candidate.ProxyURL = strings.TrimSpace(config.ProxyURL)
		}
	}
	if strings.TrimSpace(config.Adapter) != "" {
		candidate.BaseURL = firstNonEmpty(config.BaseURL, candidate.BaseURL)
		adapter := strings.ToLower(strings.TrimSpace(config.Adapter))
		// Before Command Code GOAT had a dedicated adapter, the configuration UI
		// offered OpenCode Go as the closest override. Migrate that stale choice
		// automatically without disturbing the saved monitoring preferences.
		if adapter == "opencode-go" && commandCode {
			adapter = "commandcode-goat"
		}
		if adapter == "commandcode-goat" {
			candidate = normalizeCommandCodeCandidate(candidate)
		}
		return adapter, candidate, true
	}
	switch {
	case commandCode:
		return "commandcode-goat", normalizeCommandCodeCandidate(candidate), true
	case matchesDeepSeek(candidate):
		return "deepseek-balance", candidate, true
	case p == "zai" || p == "zhipu" || p == "glm" || host == "open.bigmodel.cn" || host == "api.z.ai":
		return "zai-usage", candidate, true
	case p == "kimi-coding" || p == "kimi-for-coding" || host == "api.kimi.com":
		return "kimi-coding", candidate, true
	case p == "kimi" || p == "moonshot" || p == "moonshotai" || host == "api.moonshot.cn":
		return "moonshot-balance", candidate, true
	case p == "opencode-go" || host == "opencode.ai":
		return "opencode-go", candidate, true
	case strings.Contains(p, "new-api") || strings.Contains(p, "newapi"):
		return "newapi-usage", candidate, true
	case strings.Contains(p, "sub2api") || strings.Contains(p, "passion") || strings.HasSuffix(host, ".passionapi.com") || host == "passionapi.com":
		return "sub2api-usage", candidate, true
	case p == "codex-api-key":
		return "relay-usage", candidate, true
	default:
		return "", candidate, false
	}
}

func matchesCommandCode(provider, host string) bool {
	return strings.Contains(provider, "commandcode") ||
		strings.Contains(provider, "command-code") ||
		strings.Contains(provider, "goat") ||
		host == "api.commandcode.ai"
}

func hostname(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func queryCandidate(callbackID string, candidate credentialCandidate, storage []byte, attributes map[string]string) (accountSnapshot, error) {
	return queryCandidateContext(context.Background(), callbackID, candidate, storage, attributes)
}

func queryCandidateContext(parent context.Context, callbackID string, candidate credentialCandidate, storage []byte, attributes map[string]string) (accountSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, accountTimeout())
	defer cancel()
	accountID := candidateAccountID(candidate)
	candidate.Name = state.prefs.name(accountID, candidate.Name)
	applyCredentialProxy(&candidate, storage)
	adapter, candidate, ok := adapterFor(candidate)
	if !ok {
		return accountSnapshot{}, fmt.Errorf("no adapter matches provider %q", candidate.Provider)
	}
	if candidate.BaseURL == "" && attributes != nil {
		candidate.BaseURL = strings.TrimSpace(attributes["base_url"])
	}
	token := tokenForCandidate(candidate, storage, attributes)
	if token == "" {
		return accountSnapshot{}, fmt.Errorf("credential token is unavailable")
	}
	started := time.Now()
	var snapshot accountSnapshot
	var err error
	switch adapter {
	case "deepseek-balance":
		snapshot, err = queryDeepSeekSnapshot(ctx, callbackID, candidate, storage, attributes)
	case "zai-usage":
		snapshot, err = queryZaiSnapshot(ctx, callbackID, candidate, token)
	case "moonshot-balance":
		snapshot, err = queryMoonshotSnapshot(ctx, callbackID, candidate, token)
	case "kimi-coding":
		snapshot, err = queryKimiSnapshot(ctx, callbackID, candidate, token)
	case "opencode-go":
		snapshot, err = queryOpenCodeSnapshot(ctx, callbackID, candidate, token)
	case "newapi-usage":
		snapshot, err = queryNewAPISnapshot(ctx, callbackID, candidate, token)
	case "sub2api-usage":
		snapshot, err = querySub2APISnapshot(ctx, callbackID, candidate, token)
	case "relay-usage":
		snapshot, err = queryRelaySnapshot(ctx, callbackID, candidate, token)
	case "commandcode-goat":
		snapshot, err = queryCommandCodeSnapshot(ctx, callbackID, candidate, token)
	default:
		err = fmt.Errorf("adapter %q is not implemented", adapter)
	}
	if err != nil {
		return accountSnapshot{}, err
	}
	now := time.Now().UTC()
	snapshot.AccountID = accountID
	snapshot.AdapterID = adapter
	snapshot.CheckedAt = now
	snapshot = accountSnapshotWithSuccess(accountSnapshot{}, snapshot, now, time.Since(started))
	snapshot = applyThresholds(snapshot)
	return snapshot, nil
}

func tokenForCandidate(candidate credentialCandidate, storage []byte, attributes map[string]string) string {
	if token := tokenFromCredential(storage, attributes); token != "" {
		return token
	}
	config := monitorFor(candidate)
	if strings.TrimSpace(config.APIKeyEnv) == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(config.APIKeyEnv))
}

func queryZaiSnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpointWithOptions(candidate.BaseURL, "https://open.bigmodel.cn", "/api/monitor/usage/quota/limit", endpointOptions{AllowInternalHTTP: candidate.AllowInternalHTTP})
	if err != nil {
		return accountSnapshot{}, err
	}
	response, err := queryEndpointAuthContext(ctx, callbackID, endpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	if response.StatusCode == 404 {
		return unsupportedSnapshot(candidate, "zai-usage", "Z.ai usage quota endpoint is not available for this account"), nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return accountSnapshot{}, fmt.Errorf("Z.ai usage endpoint returned HTTP %d", response.StatusCode)
	}
	snapshot, err := parseZaiSnapshot(candidate, response.Body)
	if err != nil {
		return accountSnapshot{}, err
	}
	return snapshot, nil
}

func parseZaiSnapshot(candidate credentialCandidate, body []byte) (accountSnapshot, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return accountSnapshot{}, fmt.Errorf("decode Z.ai usage response: %w", err)
	}
	data := root
	if object, ok := root.(map[string]any); ok {
		if strings.EqualFold(stringValue(object, "success"), "false") || stringValue(object, "code") == "500" {
			return unsupportedSnapshot(candidate, "zai-usage", firstNonEmpty(stringValue(object, "msg", "message"), "Z.ai usage quota is not available for this account")), nil
		}
		if nested, ok := object["data"]; ok {
			data = nested
		}
	}
	limits := zaiLimitObjects(data)
	if len(limits) == 0 {
		return accountSnapshot{}, fmt.Errorf("Z.ai usage response contains no quota limits")
	}
	snapshot := accountSnapshot{
		AccountID:    candidateAccountID(candidate),
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "zai-usage",
		BaseURL:      candidate.BaseURL,
		Kind:         kindPeriodQuota,
		Status:       statusOK,
		Capabilities: []string{"rolling_quota"},
		Sections: map[string]sectionStatus{
			"cash_balance": zaiCashBalanceSection(candidate),
		},
	}
	for _, limit := range limits {
		kind := strings.ToUpper(stringValue(limit, "type", "kind"))
		name := zaiWindowName(limit, kind)
		window := quotaWindowFromZaiLimit(limit, name, kind)
		if window != nil {
			snapshot.Windows = append(snapshot.Windows, *window)
		}
	}
	if len(snapshot.Windows) == 0 {
		return accountSnapshot{}, fmt.Errorf("Z.ai usage response contains no readable quota limits")
	}
	return snapshot, nil
}

func zaiCashBalanceSection(candidate credentialCandidate) sectionStatus {
	actionURL := "https://bigmodel.cn/finance-center"
	if strings.Contains(strings.ToLower(candidate.BaseURL), "z.ai") {
		actionURL = "https://z.ai/manage-apikey/billing"
	}
	return sectionStatus{
		Status:      "unsupported",
		Message:     "智谱当前未提供可验证的现金余额查询接口，该账号暂不支持自动查询现金余额。",
		ActionURL:   actionURL,
		ActionLabel: "前往控制台查看",
	}
}

func zaiWindowName(limit map[string]any, kind string) string {
	if kind == "TIME_LIMIT" {
		return "month"
	}
	if kind != "TOKENS_LIMIT" && kind != "CREDIT_LIMIT" {
		raw := firstNonEmpty(stringValue(limit, "window", "period", "cycle", "name"), kind)
		return "其他周期（" + raw + "）"
	}
	unitRaw := stringValue(limit, "unit", "time_unit", "timeUnit")
	numberRaw := stringValue(limit, "number", "duration", "count")
	unit, unitOK := numericValue(unitRaw)
	number, numberOK := numericValue(numberRaw)
	if unitOK && numberOK {
		switch {
		case unit == 3 && number == 5:
			return "5h"
		case unit == 6 && number == 1:
			return "7d"
		}
	}
	raw := firstNonEmpty(stringValue(limit, "window", "period", "cycle", "name"))
	if raw == "" {
		raw = strings.Join([]string{kind, unitRaw, numberRaw}, ":")
	}
	return "其他周期（" + raw + "）"
}

func zaiLimitObjects(value any) []map[string]any {
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"limits", "quotas", "items"} {
			if limits, ok := object[key].([]any); ok {
				return objectSlice(limits)
			}
		}
	}
	if limits, ok := value.([]any); ok {
		return objectSlice(limits)
	}
	return nil
}

func objectSlice(values []any) []map[string]any {
	objects := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

func quotaWindowFromZaiLimit(object map[string]any, name, kind string) *quotaWindow {
	remainingRaw := stringValue(object, "remaining_percent", "remainingPercent", "remaining_percentage")
	usedRaw := stringValue(object, "used_percent", "usedPercent", "usage_percent", "usagePercent", "percent_used", "percentage", "percent")
	totalAmount := stringValue(object, "usage", "total", "limit")
	usedAmount := stringValue(object, "currentValue", "used", "consumed")
	remainingAmount := stringValue(object, "remaining", "available")
	var remaining float64
	var used float64
	if raw, ok := numericValue(remainingRaw); ok {
		remaining = raw / 100
		used = 1 - remaining
	} else if raw, ok := numericValue(usedRaw); ok {
		used = raw / 100
		remaining = 1 - used
	} else if total, okTotal := numericValue(totalAmount); okTotal {
		if usedValue, okUsed := numericValue(usedAmount); okUsed && total > 0 {
			used = maxMinFraction(usedValue / total)
			remaining = 1 - used
		} else if remainingValue, okRemaining := numericValue(remainingAmount); okRemaining && total > 0 {
			remaining = maxMinFraction(remainingValue / total)
			used = 1 - remaining
		} else {
			return nil
		}
	} else {
		return nil
	}
	remaining = maxMinFraction(remaining)
	used = maxMinFraction(used)
	unit := stringValue(object, "metric")
	if unit == "" {
		switch kind {
		case "TOKENS_LIMIT":
			unit = "tokens"
		case "TIME_LIMIT":
			unit = "requests"
		default:
			unit = "quota"
		}
	}
	return &quotaWindow{
		Name:              name,
		RemainingFraction: &remaining,
		UsedFraction:      &used,
		RemainingAmount:   remainingAmount,
		TotalAmount:       totalAmount,
		Unit:              unit,
		ResetAt:           timeValue(object, "reset_time", "resetTime", "next_reset_time", "nextResetAt", "nextResetTime", "resetsAt"),
	}
}

func maxMinFraction(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func queryMoonshotSnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpointWithOptions(candidate.BaseURL, "https://api.moonshot.cn", "/v1/users/me/balance", endpointOptions{AllowInternalHTTP: candidate.AllowInternalHTTP})
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSONContext(ctx, callbackID, endpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode Moonshot response: %w", err)
	}
	data := objectValue(root, "data")
	total := stringValue(data, "available_balance", "balance")
	if total == "" {
		return accountSnapshot{}, fmt.Errorf("Moonshot response contains no balance")
	}
	snapshot := balanceSnapshot(candidate, "moonshot-balance", firstNonEmpty(stringValue(data, "currency"), "CNY"), total, "")
	if cash := stringValue(data, "cash_balance"); cash != "" {
		snapshot.Balances = append(snapshot.Balances, moneyBalance{Amount: cash, Currency: firstNonEmpty(stringValue(data, "currency"), "CNY"), BalanceType: "cash"})
	}
	if voucher := stringValue(data, "voucher_balance"); voucher != "" {
		snapshot.Balances = append(snapshot.Balances, moneyBalance{Amount: voucher, Currency: firstNonEmpty(stringValue(data, "currency"), "CNY"), BalanceType: "voucher"})
	}
	return snapshot, nil
}

func queryKimiSnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpointWithOptions(candidate.BaseURL, "https://api.kimi.com", "/coding/v1/usages", endpointOptions{AllowInternalHTTP: candidate.AllowInternalHTTP})
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSONContext(ctx, callbackID, endpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode Kimi response: %w", err)
	}
	data := objectValue(root, "data")
	snapshot := accountSnapshot{
		AccountID:    candidateAccountID(candidate),
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "kimi-coding",
		BaseURL:      candidate.BaseURL,
		Kind:         kindPeriodQuota,
		Status:       statusOK,
		Capabilities: []string{"rolling_quota"},
	}
	if usage := objectValue(data, "usage"); usage != nil {
		if window := quotaWindowFromLimit(usage, "weekly"); window != nil {
			snapshot.Windows = append(snapshot.Windows, *window)
		}
	}
	if limits := arrayValue(data, "limits"); len(limits) > 0 {
		for _, limit := range limits {
			if window := quotaWindowFromLimit(objectValue(limit, "detail"), "session"); window != nil {
				snapshot.Windows = append(snapshot.Windows, *window)
				break
			}
		}
	}
	if len(snapshot.Windows) == 0 {
		return accountSnapshot{}, fmt.Errorf("Kimi response contains no quota windows")
	}
	return snapshot, nil
}

func queryOpenCodeSnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpointWithOptions(candidate.BaseURL, "https://opencode.ai", "/zen/go/v1/usage", endpointOptions{AllowInternalHTTP: candidate.AllowInternalHTTP})
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSONContext(ctx, callbackID, endpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	return parseOpenCodeSnapshot(candidate, body)
}

func parseOpenCodeSnapshot(candidate credentialCandidate, body []byte) (accountSnapshot, error) {
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode OpenCode Go response: %w", err)
	}
	usage := objectValue(root, "usage")
	if usage == nil {
		usage = root
	}
	snapshot := accountSnapshot{
		AccountID:    candidateAccountID(candidate),
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "opencode-go",
		BaseURL:      candidate.BaseURL,
		Kind:         kindPeriodQuota,
		Status:       statusOK,
		Capabilities: []string{"rolling_quota"},
	}
	for _, definition := range []struct {
		key  string
		name string
	}{
		{key: "rolling", name: "session"},
		{key: "weekly", name: "weekly"},
		{key: "monthly", name: "monthly"},
	} {
		if window := quotaWindowFromPercent(objectValue(usage, definition.key), definition.name); window != nil {
			snapshot.Windows = append(snapshot.Windows, *window)
		}
	}
	if len(snapshot.Windows) < 2 {
		return accountSnapshot{}, fmt.Errorf("OpenCode Go response contains insufficient quota windows")
	}
	return snapshot, nil
}

func queryNewAPISnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := newAPIManagementEndpoint(candidate, "/api/usage/token")
	if err != nil {
		return accountSnapshot{}, err
	}
	response, keyErr := queryEndpointContext(ctx, callbackID, endpoint, token, candidateProxyURL(candidate))
	var keyBody []byte
	if keyErr == nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			keyErr = fmt.Errorf("NewAPI token endpoint returned HTTP %d", response.StatusCode)
		} else {
			keyBody = response.Body
		}
	}
	quotaStatus, errStatus := queryNewAPIQuotaStatus(ctx, callbackID, candidate)
	if errStatus != nil {
		quotaStatus = defaultNewAPIQuotaStatus()
	}
	monitor := monitorFor(candidate)
	var account *quotaQuantity
	var accountErr error
	if managementCredentialFor(candidate, monitor) == "" {
		accountErr = errNewAPIAccountPATMissing
	} else {
		account, accountErr = queryNewAPIAccountQuantity(ctx, callbackID, candidate, monitor, quotaStatus)
	}
	snapshot, err := newAPISnapshotFromParts(candidate, quotaStatus, keyBody, keyErr, account, accountErr)
	if err != nil {
		return accountSnapshot{}, err
	}
	snapshot.Details = newAPISnapshotDetails(candidate, quotaStatus, keyBody, account)
	if errStatus != nil {
		snapshot.Warnings = append(snapshot.Warnings, "NewAPI 显示配置暂时无法查询，已使用兼容默认值。")
	}
	if errors.Is(accountErr, errNewAPIAccountPATMissing) {
		snapshot.Warnings = append(snapshot.Warnings, "当前仅显示这个 API Key 的额度；要显示 NewAPI 账户总额度，请配置管理凭据")
	} else if accountErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "NewAPI 账户总额度暂时无法查询，当前显示 API Key 额度")
	}
	return snapshot, nil
}

func queryNewAPIQuotaStatus(ctx context.Context, callbackID string, candidate credentialCandidate) (newAPIQuotaStatus, error) {
	endpoint, err := newAPIManagementEndpoint(candidate, "/api/status")
	if err != nil {
		return newAPIQuotaStatus{}, err
	}
	response, err := queryEndpointAuthContext(ctx, callbackID, endpoint, "", candidateProxyURL(candidate))
	if err != nil {
		return newAPIQuotaStatus{}, err
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return defaultNewAPIQuotaStatus(), nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newAPIQuotaStatus{}, fmt.Errorf("NewAPI status endpoint returned HTTP %d", response.StatusCode)
	}
	return parseNewAPIQuotaStatus(response.Body)
}

func parseNewAPIQuotaStatus(body []byte) (newAPIQuotaStatus, error) {
	root, err := decodeObject(body)
	if err != nil {
		return newAPIQuotaStatus{}, fmt.Errorf("decode NewAPI status response: %w", err)
	}
	data := objectValue(root, "data")
	if data == nil {
		data = root
	}
	quotaPerUnit, ok := numericValue(stringValue(data, "quota_per_unit"))
	if !ok || quotaPerUnit <= 0 {
		return defaultNewAPIQuotaStatus(), nil
	}
	displayType := strings.ToUpper(firstNonEmpty(stringValue(data, "quota_display_type"), "USD"))
	exchangeRate := 1.0
	if raw := stringValue(data, "usd_exchange_rate"); raw != "" {
		if parsed, ok := numericValue(raw); ok && parsed > 0 {
			exchangeRate = parsed
		}
	}
	customExchangeRate := 0.0
	if raw := stringValue(data, "custom_currency_exchange_rate"); raw != "" {
		if parsed, ok := numericValue(raw); ok && parsed > 0 {
			customExchangeRate = parsed
		}
	}
	return newAPIQuotaStatus{
		QuotaPerUnit:               quotaPerUnit,
		DisplayType:                displayType,
		USDExchangeRate:            exchangeRate,
		CustomCurrencySymbol:       stringValue(data, "custom_currency_symbol"),
		CustomCurrencyExchangeRate: customExchangeRate,
	}, nil
}

func newAPIAmount(raw string, status newAPIQuotaStatus) string {
	value, ok := numericValue(raw)
	if !ok {
		return raw
	}
	if newAPIConversionIssue(status) != "" {
		return raw
	}
	if strings.EqualFold(status.DisplayType, "TOKENS") {
		return raw
	}
	amount := value / status.QuotaPerUnit
	switch {
	case strings.EqualFold(status.DisplayType, "CNY"):
		amount *= status.USDExchangeRate
	case strings.EqualFold(status.DisplayType, "CUSTOM"):
		amount *= status.CustomCurrencyExchangeRate
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(amount, 'f', 6, 64), "0"), ".")
}

func newAPIConversionIssue(status newAPIQuotaStatus) string {
	if status.QuotaPerUnit <= 0 {
		return "NewAPI 显示配置缺少有效的 quota_per_unit，已保留原始 quota，未换算。"
	}
	switch strings.ToUpper(strings.TrimSpace(status.DisplayType)) {
	case "USD", "TOKENS":
		return ""
	case "CNY":
		if status.USDExchangeRate <= 0 {
			return "NewAPI 人民币显示配置缺少有效的 usd_exchange_rate，已保留原始 quota，未换算。"
		}
	case "CUSTOM":
		if strings.TrimSpace(status.CustomCurrencySymbol) == "" || status.CustomCurrencyExchangeRate <= 0 {
			return "NewAPI 自定义币种配置不完整，已保留原始 quota，未换算。"
		}
	default:
		return fmt.Sprintf("NewAPI 显示类型 %q 暂不支持，已保留原始 quota，未换算。", status.DisplayType)
	}
	return ""
}

func newAPIUnit(status newAPIQuotaStatus) string {
	if strings.EqualFold(status.DisplayType, "TOKENS") {
		return "tokens"
	}
	if strings.EqualFold(status.DisplayType, "CUSTOM") && strings.TrimSpace(status.CustomCurrencySymbol) != "" {
		return status.CustomCurrencySymbol
	}
	return status.DisplayType
}

func parseNewAPISnapshotWithQuotaStatus(callbackID string, candidate credentialCandidate, body []byte, quotaStatus newAPIQuotaStatus) (accountSnapshot, error) {
	return parseNewAPISnapshotWithQuotaStatusContext(context.Background(), callbackID, candidate, body, quotaStatus)
}

func parseNewAPISnapshotWithQuotaStatusContext(ctx context.Context, callbackID string, candidate credentialCandidate, body []byte, quotaStatus newAPIQuotaStatus) (accountSnapshot, error) {
	monitor := monitorFor(candidate)
	var account *quotaQuantity
	var accountErr error
	if managementCredentialFor(candidate, monitor) == "" {
		accountErr = errNewAPIAccountPATMissing
	} else {
		account, accountErr = queryNewAPIAccountQuantity(ctx, callbackID, candidate, monitor, quotaStatus)
	}
	snapshot, err := newAPISnapshotFromParts(candidate, quotaStatus, body, nil, account, accountErr)
	if err != nil {
		return accountSnapshot{}, err
	}
	snapshot.Details = newAPISnapshotDetails(candidate, quotaStatus, body, account)
	if errors.Is(accountErr, errNewAPIAccountPATMissing) {
		snapshot.Warnings = append(snapshot.Warnings, "当前仅显示这个 API Key 的额度；要显示 NewAPI 账户总额度，请配置管理凭据")
	} else if accountErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "NewAPI 账户总额度暂时无法查询，当前显示 API Key 额度")
	}
	return snapshot, nil
}

func newAPISnapshotDetails(candidate credentialCandidate, quotaStatus newAPIQuotaStatus, keyBody []byte, account *quotaQuantity) map[string]any {
	unlimited := false
	if quantity, _, err := parseNewAPIKeyQuantity(keyBody, quotaStatus); err == nil {
		unlimited = quantity.Unlimited
	}
	return map[string]any{
		"source":             "/api/usage/token/",
		"quota_per_unit":     quotaStatus.QuotaPerUnit,
		"quota_display_type": quotaStatus.DisplayType,
		"usd_exchange_rate":  quotaStatus.USDExchangeRate,
		"unlimited_quota":    unlimited,
	}
}

func newAPISnapshotFromParts(candidate credentialCandidate, quotaStatus newAPIQuotaStatus, keyBody []byte, keyErr error, account *quotaQuantity, accountErr error) (accountSnapshot, error) {
	snapshot := accountSnapshot{
		AccountID:   candidateAccountID(candidate),
		AccountName: candidate.Name,
		Provider:    candidate.Provider,
		AdapterID:   "newapi-usage",
		BaseURL:     candidate.BaseURL,
		Kind:        kindQuota,
		Status:      statusOK,
		Capabilities: []string{
			"quota",
		},
		Sections: map[string]sectionStatus{},
	}
	if keyErr == nil {
		quantity, warnings, errKey := parseNewAPIKeyQuantity(keyBody, quotaStatus)
		if errKey != nil {
			keyErr = errKey
		} else {
			snapshot.Quantities = append(snapshot.Quantities, quantity)
			snapshot.Warnings = append(snapshot.Warnings, warnings...)
		}
	}
	if keyErr != nil {
		snapshot.Status = statusWarning
		snapshot.Sections["key_quota"] = sectionStatus{Status: "error", ErrorCode: "UPSTREAM_ERROR", Message: keyErr.Error()}
	}
	if accountErr == nil && account != nil {
		snapshot.Quantities = append(snapshot.Quantities, *account)
	} else if accountErr != nil {
		section := sectionStatus{Status: "error", ErrorCode: "UPSTREAM_ERROR", Message: accountErr.Error()}
		if errors.Is(accountErr, errNewAPIAccountPATMissing) {
			section.Status = "missing"
			section.ErrorCode = "PAT_NOT_CONFIGURED"
		} else {
			snapshot.Status = statusWarning
		}
		snapshot.Sections["account_quota"] = section
	}
	return snapshot, nil
}

func parseNewAPIKeyQuantity(body []byte, quotaStatus newAPIQuotaStatus) (quotaQuantity, []string, error) {
	root, err := decodeObject(body)
	if err != nil {
		return quotaQuantity{}, nil, fmt.Errorf("decode NewAPI response: %w", err)
	}
	data := objectValue(root, "data")
	if data == nil {
		data = root
	}
	if strings.EqualFold(stringValue(root, "code"), "false") {
		return quotaQuantity{}, nil, fmt.Errorf("NewAPI token response reports failure")
	}
	rawTotal := stringValue(data, "total_granted", "total", "quota")
	rawUsed := stringValue(data, "total_used", "used", "usage")
	rawRemaining := stringValue(data, "total_available", "remaining", "available")
	if rawTotal == "" && rawRemaining == "" {
		return quotaQuantity{}, nil, fmt.Errorf("NewAPI response contains no quota values")
	}
	unlimited := boolValue(data, "unlimited_quota", "unlimited")
	quantity := quotaQuantity{
		Name:      "当前 Key",
		Scope:     "token",
		Source:    "/api/usage/token/",
		Unlimited: unlimited,
		Remaining: newAPIAmount(rawRemaining, quotaStatus),
		Total:     newAPIAmount(rawTotal, quotaStatus),
		Used:      newAPIAmount(rawUsed, quotaStatus),
		Unit:      newAPIUnit(quotaStatus),
		ResetAt:   timeValue(data, "reset_at"),
		ExpiresAt: timeValue(data, "expires_at"),
	}
	if unlimited {
		quantity.Remaining = ""
		quantity.Total = ""
		quantity.Used = ""
	}
	var warnings []string
	if warning := newAPIConversionIssue(quotaStatus); warning != "" {
		warnings = append(warnings, warning)
	}
	return quantity, warnings, nil
}

func managementCredentialFor(candidate credentialCandidate, monitor monitorConfig) string {
	if state.prefs != nil {
		if value := state.prefs.pat(candidateAccountID(candidate)); value != "" {
			return value
		}
	}
	if monitor.ManagementAPIKeyEnv == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(monitor.ManagementAPIKeyEnv))
}

func queryNewAPIAccountQuantity(ctx context.Context, callbackID string, candidate credentialCandidate, monitor monitorConfig, quotaStatus newAPIQuotaStatus) (*quotaQuantity, error) {
	managementToken := managementCredentialFor(candidate, monitor)
	if managementToken == "" {
		return nil, fmt.Errorf("management credential is not configured")
	}
	endpoint, err := newAPIManagementEndpoint(candidate, "/api/user/self")
	if err != nil {
		return nil, err
	}
	response, err := queryEndpointAuthContext(ctx, callbackID, endpoint, "Bearer "+managementToken, candidateProxyURL(candidate))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("NewAPI account endpoint returned HTTP %d", response.StatusCode)
	}
	root, err := decodeObject(response.Body)
	if err != nil {
		return nil, err
	}
	data := objectValue(root, "data")
	if data == nil {
		return nil, fmt.Errorf("NewAPI account response contains no data")
	}
	rawRemaining := stringValue(data, "quota", "remaining", "available")
	rawUsed := stringValue(data, "used_quota", "used", "usage")
	if rawRemaining == "" {
		return nil, fmt.Errorf("NewAPI account response contains no quota")
	}
	remaining, remainingOK := numericValue(rawRemaining)
	used, usedOK := numericValue(rawUsed)
	total := ""
	if remainingOK && usedOK {
		total = newAPIAmount(strconv.FormatFloat(remaining+used, 'f', -1, 64), quotaStatus)
	}
	return &quotaQuantity{
		Name:      "NewAPI 账户总额",
		Scope:     "account",
		Source:    "/api/user/self",
		Remaining: newAPIAmount(rawRemaining, quotaStatus),
		Total:     total,
		Used:      newAPIAmount(rawUsed, quotaStatus),
		Unit:      newAPIUnit(quotaStatus),
	}, nil
}

func querySub2APISnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := sub2APIUsageEndpoint(candidate)
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSONContext(ctx, callbackID, endpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	snapshot, err := parseSub2APISnapshot(candidate, body)
	if err != nil {
		return accountSnapshot{}, err
	}
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode Sub2API response: %w", err)
	}
	return attachSub2APIDetails(ctx, callbackID, candidate, token, snapshot, root), nil
}

func attachSub2APIDetails(ctx context.Context, callbackID string, candidate credentialCandidate, token string, snapshot accountSnapshot, root map[string]any) accountSnapshot {
	snapshot.Details = curatedDetails(root)
	billingEndpoint, err := fixedEndpointWithOptions(candidate.BaseURL, "", "/v1/sub2api/billing", endpointOptions{AllowInternalHTTP: candidate.AllowInternalHTTP})
	if err != nil {
		markSectionError(&snapshot, "billing", "INVALID_ENDPOINT", err.Error())
		return snapshot
	}
	response, err := queryEndpointContext(ctx, callbackID, billingEndpoint, token, candidateProxyURL(candidate))
	return applySub2APIBillingResponse(snapshot, response, err)
}

func applySub2APIBillingResponse(snapshot accountSnapshot, response hostHTTPResponse, queryErr error) accountSnapshot {
	if queryErr != nil {
		markSectionError(&snapshot, "billing", "UPSTREAM_REQUEST_FAILED", queryErr.Error())
		return snapshot
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		markSectionError(&snapshot, "billing", fmt.Sprintf("UPSTREAM_HTTP_%d", response.StatusCode), fmt.Sprintf("Sub2API billing endpoint returned HTTP %d", response.StatusCode))
		return snapshot
	}
	billingRoot, err := decodeObject(response.Body)
	if err != nil {
		markSectionError(&snapshot, "billing", "INVALID_RESPONSE", "Sub2API billing response is not valid JSON")
		return snapshot
	}
	if snapshot.Details == nil {
		snapshot.Details = map[string]any{}
	}
	snapshot.Details["billing"] = sanitizeJSON(billingRoot, 0)
	if snapshot.Sections == nil {
		snapshot.Sections = map[string]sectionStatus{}
	}
	snapshot.Sections["billing"] = sectionStatus{Status: "available"}
	return snapshot
}

func curatedDetails(root map[string]any) map[string]any {
	data := root
	if nested := objectValue(root, "data"); nested != nil {
		data = nested
	}
	known := []string{"mode", "isValid", "is_active", "status", "planName", "plan_name", "remaining", "balance", "unit", "quota", "rate_limits", "expires_at", "days_until_expiry", "subscription", "usage", "daily_usage", "model_stats"}
	details := make(map[string]any)
	for _, key := range known {
		if value, ok := data[key]; ok {
			details[key] = sanitizeJSON(value, 0)
		}
	}
	return details
}

func sanitizeJSON(value any, depth int) any {
	if depth > 8 {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			lower := strings.ToLower(key)
			if lower == "api_key" || lower == "apikey" || lower == "token" || lower == "authorization" || lower == "cookie" || lower == "secret" {
				continue
			}
			out[key] = sanitizeJSON(child, depth+1)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, sanitizeJSON(child, depth+1))
		}
		return out
	default:
		return value
	}
}

func queryRelaySnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	newAPIEndpoint, err := newAPIManagementEndpoint(candidate, "/api/usage/token")
	if err != nil {
		return accountSnapshot{}, err
	}
	response, err := queryEndpointContext(ctx, callbackID, newAPIEndpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		quotaStatus, errStatus := queryNewAPIQuotaStatus(ctx, callbackID, candidate)
		if errStatus != nil {
			quotaStatus = defaultNewAPIQuotaStatus()
		}
		snapshot, errParse := parseNewAPISnapshotWithQuotaStatusContext(ctx, callbackID, candidate, response.Body, quotaStatus)
		if errParse != nil {
			return accountSnapshot{}, errParse
		}
		if errStatus != nil {
			snapshot.Warnings = append(snapshot.Warnings, "NewAPI 显示配置暂时无法查询，已使用兼容默认值。")
		}
		return snapshot, nil
	}
	if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusMethodNotAllowed {
		return accountSnapshot{}, fmt.Errorf("relay quota endpoint returned HTTP %d", response.StatusCode)
	}

	sub2APIEndpoint, err := sub2APIUsageEndpoint(candidate)
	if err != nil {
		return accountSnapshot{}, err
	}
	response, err = queryEndpointContext(ctx, callbackID, sub2APIEndpoint, token, candidateProxyURL(candidate))
	if err != nil {
		return accountSnapshot{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return accountSnapshot{}, fmt.Errorf("Sub2API usage endpoint returned HTTP %d", response.StatusCode)
	}
	snapshot, err := parseSub2APISnapshot(candidate, response.Body)
	if err != nil {
		return accountSnapshot{}, err
	}
	root, err := decodeObject(response.Body)
	if err == nil {
		snapshot = attachSub2APIDetails(ctx, callbackID, candidate, token, snapshot, root)
	}
	return snapshot, nil
}

func queryEndpoint(callbackID, endpoint, token, proxyURL string) (hostHTTPResponse, error) {
	return queryEndpointAuthContext(context.Background(), callbackID, endpoint, "Bearer "+token, proxyURL)
}

func queryEndpointContext(ctx context.Context, callbackID, endpoint, token, proxyURL string) (hostHTTPResponse, error) {
	return queryEndpointAuthContext(ctx, callbackID, endpoint, "Bearer "+token, proxyURL)
}

func queryEndpointAuth(callbackID, endpoint, authorization, proxyURL string) (hostHTTPResponse, error) {
	return queryEndpointAuthContext(context.Background(), callbackID, endpoint, authorization, proxyURL)
}

func queryEndpointAuthContext(ctx context.Context, callbackID, endpoint, authorization, proxyURL string) (hostHTTPResponse, error) {
	return queryEndpointWithHeadersContext(ctx, callbackID, endpoint, authorization, proxyURL, nil)
}

func queryEndpointWithHeaders(callbackID, endpoint, authorization, proxyURL string, extraHeaders map[string]string) (hostHTTPResponse, error) {
	timeout := requestTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return queryEndpointWithHeadersContext(ctx, callbackID, endpoint, authorization, proxyURL, extraHeaders)
}

func queryEndpointWithHeadersContext(ctx context.Context, callbackID, endpoint, authorization, proxyURL string, extraHeaders map[string]string) (hostHTTPResponse, error) {
	return queryEndpointWithProxyContext(ctx, callbackID, endpoint, authorization, proxySettings{Mode: proxyModeURL, URL: proxyURL}, extraHeaders)
}

func requestTimeout() time.Duration {
	state.mu.RLock()
	timeout := time.Duration(state.cfg.RequestTimeoutSecond) * time.Second
	state.mu.RUnlock()
	if timeout <= 0 {
		return 8 * time.Second
	}
	return timeout
}

func accountTimeout() time.Duration {
	state.mu.RLock()
	timeout := time.Duration(state.cfg.AccountTimeoutSecond) * time.Second
	state.mu.RUnlock()
	if timeout <= 0 {
		return 30 * time.Second
	}
	return timeout
}

func addSub2APIUsageQuery(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	query := u.Query()
	query.Set("days", "30")
	u.RawQuery = query.Encode()
	return u.String()
}

func sub2APIUsageEndpoint(candidate credentialCandidate) (string, error) {
	endpoint, err := fixedEndpointWithOptions(candidate.BaseURL, "", "/v1/usage", endpointOptions{AllowInternalHTTP: candidate.AllowInternalHTTP})
	if err != nil {
		return "", err
	}
	return addSub2APIUsageQuery(endpoint), nil
}

func parseNewAPISnapshot(candidate credentialCandidate, body []byte) (accountSnapshot, error) {
	return parseNewAPISnapshotWithQuotaStatus("", candidate, body, newAPIQuotaStatus{QuotaPerUnit: 1, DisplayType: "quota", USDExchangeRate: 1})
}

func parseSub2APISnapshot(candidate credentialCandidate, body []byte) (accountSnapshot, error) {
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode Sub2API response: %w", err)
	}
	data := objectValue(root, "data")
	if data == nil {
		data = root
	}
	snapshot, err := parseSub2APISnapshotData(candidate, data)
	if err != nil {
		return accountSnapshot{}, err
	}
	return applySub2APIKeyStatus(snapshot, data, time.Now().UTC()), nil
}

func parseSub2APISnapshotData(candidate credentialCandidate, data map[string]any) (accountSnapshot, error) {
	quota := objectValue(data, "quota")
	total := firstNonEmpty(stringValue(data, "total", "limit", "quota"), stringValue(quota, "total", "limit"))
	remaining := stringValue(data, "remaining", "available", "balance")
	if remaining == "" {
		remaining = stringValue(quota, "remaining", "available")
	}
	if total != "" || remaining != "" {
		used := firstNonEmpty(stringValue(data, "used", "usage"), stringValue(quota, "used", "usage"))
		if remaining != "" {
			unit := firstNonEmpty(stringValue(data, "unit"), stringValue(quota, "unit"), "quota")
			if strings.ToLower(stringValue(data, "mode")) == "quota_limited" || objectValue(data, "subscription") != nil {
				if snapshot, ok := sub2APIPeriodSnapshot(candidate, data, quota, unit); ok {
					return snapshot, nil
				}
			}
			snapshot := accountSnapshot{AccountID: candidateAccountID(candidate), AccountName: candidate.Name, Provider: candidate.Provider, AdapterID: "sub2api-usage", BaseURL: candidate.BaseURL, Kind: kindQuota, Status: statusOK, Capabilities: []string{"quota"}, Quantities: []quotaQuantity{{Name: "account", Remaining: remaining, Total: total, Used: used, Unit: unit, ResetAt: timeValue(data, "reset_at"), ExpiresAt: timeValue(data, "expires_at")}}}
			if isCurrency(strings.ToUpper(unit)) {
				snapshot.Kind = kindBalance
				snapshot.Capabilities = []string{"balance"}
				snapshot.Balances = []moneyBalance{{Amount: remaining, Currency: strings.ToUpper(unit), BalanceType: "remaining"}}
			}
			return snapshot, nil
		}
	}
	if snapshot, ok := sub2APIPeriodSnapshot(candidate, data, quota, firstNonEmpty(stringValue(data, "unit"), stringValue(quota, "unit"), "quota")); ok {
		return snapshot, nil
	}
	if balance := stringValue(data, "balance", "available_balance"); balance != "" {
		return balanceSnapshot(candidate, "sub2api-usage", firstNonEmpty(stringValue(data, "currency"), "USD"), balance, ""), nil
	}
	return accountSnapshot{}, fmt.Errorf("Sub2API response contains no supported quota values")
}

func applySub2APIKeyStatus(snapshot accountSnapshot, data map[string]any, now time.Time) accountSnapshot {
	status := strings.ToLower(strings.TrimSpace(stringValue(data, "status", "key_status", "keyStatus", "account_status", "accountStatus")))
	valid, validKnown := objectBoolValue(data, "isValid", "is_valid", "valid")
	expiresAt := timeValue(data, "expires_at", "expiresAt")
	code := ""
	message := ""
	switch status {
	case "expired":
		code = "KEY_EXPIRED"
		message = "上游 Key 已过期。"
	case "disabled", "banned", "revoked", "suspended", "inactive":
		code = "KEY_DISABLED"
		message = "上游已禁用该 Key 或账户。"
	}
	if code == "" && expiresAt != nil && !expiresAt.After(now) {
		code = "KEY_EXPIRED"
		message = "上游 Key 已过期。"
	}
	if code == "" && validKnown && !valid {
		code = "KEY_INVALID"
		message = "上游返回该 Key 无效。"
	}
	if code == "" {
		return snapshot
	}
	snapshot.Status = statusDisabled
	snapshot.Error = &snapshotError{Code: code, Message: message}
	return snapshot
}

func objectBoolValue(object map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
			if err == nil {
				return parsed, true
			}
		}
	}
	return false, false
}

func sub2APIPeriodSnapshot(candidate credentialCandidate, data, quota map[string]any, unit string) (accountSnapshot, bool) {
	snapshot := accountSnapshot{
		AccountID:    candidateAccountID(candidate),
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "sub2api-usage",
		BaseURL:      candidate.BaseURL,
		Kind:         kindPeriodQuota,
		Status:       statusOK,
		Capabilities: []string{"rolling_quota"},
	}
	if quota != nil {
		if window := quotaWindowFromValues("total", stringValue(quota, "remaining", "available"), stringValue(quota, "limit", "total"), stringValue(quota, "used", "usage"), unit, firstTimeValue([]map[string]any{data, quota}, "reset_at", "resetAt")); window != nil {
			window.ExpiresAt = firstTimeValue([]map[string]any{data, quota}, "expires_at", "expiresAt")
			snapshot.Windows = append(snapshot.Windows, *window)
		}
	}
	if limits := objectArray(data, "rate_limits"); len(limits) > 0 {
		for _, limit := range limits {
			window := quotaWindowFromLimit(limit, stringValue(limit, "window"))
			if window != nil {
				window.Unit = firstNonEmpty(window.Unit, stringValue(data, "unit"), unit, "quota")
				snapshot.Windows = append(snapshot.Windows, *window)
			}
		}
	}
	if subscription := objectValue(data, "subscription"); subscription != nil {
		for _, period := range []string{"daily", "weekly", "monthly"} {
			if window := quotaWindowFromValues(period, stringValue(subscription, period+"_limit_usd"), stringValue(subscription, period+"_limit_usd"), stringValue(subscription, period+"_usage_usd"), "USD", timeValue(subscription, "reset_at")); window != nil {
				window.ExpiresAt = timeValue(subscription, "expires_at")
				// The helper receives remaining first. Subscription responses expose
				// usage and limit, so convert the usage into remaining here.
				used := stringValue(subscription, period+"_usage_usd")
				limit := stringValue(subscription, period+"_limit_usd")
				if usedValue, okUsed := numericValue(used); okUsed {
					if limitValue, okLimit := numericValue(limit); okLimit {
						window = quotaWindowFromValues(period, strconv.FormatFloat(limitValue-usedValue, 'f', -1, 64), limit, used, "USD", timeValue(subscription, "reset_at"))
						window.ExpiresAt = timeValue(subscription, "expires_at")
					}
				}
				if window != nil {
					snapshot.Windows = append(snapshot.Windows, *window)
				}
			}
		}
	}
	return snapshot, len(snapshot.Windows) > 0
}

func quotaWindowFromValues(name, remaining, total, used, unit string, resetAt *time.Time) *quotaWindow {
	limitValue, okLimit := numericValue(total)
	remainingValue, okRemaining := numericValue(remaining)
	if !okLimit || !okRemaining || limitValue <= 0 {
		return nil
	}
	usedValue, okUsed := numericValue(used)
	if !okUsed {
		usedValue = limitValue - remainingValue
	}
	usedText := firstNonEmpty(used, decimalString(usedValue))
	if remainingValue < 0 || usedValue > limitValue {
		excess := maxFloat(usedValue-limitValue, -remainingValue)
		zero := 0.0
		one := 1.0
		return &quotaWindow{
			Name:              name,
			RemainingFraction: &zero,
			UsedFraction:      &one,
			RemainingAmount:   "0",
			TotalAmount:       total,
			UsedAmount:        usedText,
			ExcessAmount:      decimalString(excess),
			Unit:              unit,
			ResetAt:           resetAt,
		}
	}
	if remainingValue > limitValue {
		remainingValue = limitValue
	}
	remainingText := decimalString(remainingValue)
	frac := remainingValue / limitValue
	usedFraction := 1 - frac
	return &quotaWindow{Name: name, RemainingFraction: &frac, UsedFraction: &usedFraction, RemainingAmount: firstNonEmpty(remaining, remainingText), TotalAmount: total, UsedAmount: usedText, Unit: unit, ResetAt: resetAt}
}

func decimalString(value float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(value, 'f', 6, 64), "0"), ".")
}

func fixedEndpoint(baseURL, defaultBase, path string) (string, error) {
	return fixedEndpointWithOptions(baseURL, defaultBase, path, endpointOptions{})
}

func fixedEndpointWithOptions(baseURL, defaultBase, path string, options endpointOptions) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = defaultBase
	}
	if base == "" {
		return "", fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("upstream base URL is invalid")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && options.AllowInternalHTTP) {
		return "", fmt.Errorf("upstream base URL must use HTTPS unless internal HTTP is explicitly enabled")
	}
	endpointURL, err := url.Parse(strings.TrimSpace(path))
	if err != nil || endpointURL.Path == "" {
		return "", fmt.Errorf("upstream endpoint path is invalid")
	}
	basePath := u.EscapedPath()
	if options.StripInferenceSuffix {
		baseSegments := splitEndpointPath(basePath)
		if len(baseSegments) > 0 && baseSegments[len(baseSegments)-1] == "v1" {
			baseSegments = baseSegments[:len(baseSegments)-1]
		}
		basePath = "/"
		if len(baseSegments) > 0 {
			basePath = "/" + strings.Join(baseSegments, "/")
		}
	}
	mergedPath := mergeEndpointPath(basePath, endpointURL.EscapedPath())
	decodedPath, err := url.PathUnescape(mergedPath)
	if err != nil {
		return "", fmt.Errorf("decode upstream endpoint path: %w", err)
	}
	u.Path = decodedPath
	u.RawPath = mergedPath
	u.RawQuery = endpointURL.RawQuery
	u.Fragment = ""
	return u.String(), nil
}

func newAPIManagementEndpoint(candidate credentialCandidate, path string) (string, error) {
	base := firstNonEmpty(candidate.ManagementBaseURL, candidate.BaseURL)
	return fixedEndpointWithOptions(base, "", path, endpointOptions{AllowInternalHTTP: candidate.AllowInternalHTTP, StripInferenceSuffix: true})
}

func mergeEndpointPath(basePath, endpointPath string) string {
	baseSegments := splitEndpointPath(basePath)
	endpointSegments := splitEndpointPath(endpointPath)
	maxOverlap := min(len(baseSegments), len(endpointSegments)-1)
	overlap := 0
	for count := maxOverlap; count > 0; count-- {
		if slices.Equal(baseSegments[len(baseSegments)-count:], endpointSegments[:count]) {
			overlap = count
			break
		}
	}
	segments := append(append([]string(nil), baseSegments...), endpointSegments[overlap:]...)
	if len(segments) == 0 {
		return "/"
	}
	return "/" + strings.Join(segments, "/")
}

func splitEndpointPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			segments = append(segments, part)
		}
	}
	return segments
}

func decodeObject(raw []byte) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func objectValue(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	value, ok := object[key].(map[string]any)
	if !ok {
		return nil
	}
	return value
}

func arrayValue(object map[string]any, key string) []map[string]any {
	values, ok := object[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func stringValue(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			switch typed := value.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return strings.TrimSpace(typed)
				}
			case float64:
				return strconv.FormatFloat(typed, 'f', -1, 64)
			case json.Number:
				return typed.String()
			}
		}
	}
	return ""
}

func boolValue(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			if typed, ok := value.(bool); ok {
				return typed
			}
			if typed, ok := value.(string); ok {
				parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
				if err == nil {
					return parsed
				}
			}
		}
	}
	return false
}

func objectArray(object map[string]any, key string) []map[string]any {
	values, ok := object[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

func numericValue(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, err == nil
}

func timeValue(object map[string]any, keys ...string) *time.Time {
	raw := stringValue(object, keys...)
	if raw == "" {
		return nil
	}
	if number, ok := numericValue(raw); ok {
		if number <= 0 {
			return nil
		}
		if number < 20000000000 {
			number *= 1000
		}
		value := time.UnixMilli(int64(number)).UTC()
		return &value
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &value
}

func firstTimeValue(objects []map[string]any, keys ...string) *time.Time {
	for _, object := range objects {
		if value := timeValue(object, keys...); value != nil {
			return value
		}
	}
	return nil
}

func quotaWindowFromPercent(object map[string]any, name string) *quotaWindow {
	if object == nil {
		return nil
	}
	var used float64
	if raw, ok := numericValue(stringValue(object, "usedFraction")); ok {
		used = raw
	} else if raw, ok := numericValue(stringValue(object, "remainingFraction")); ok {
		used = 1 - raw
	} else if raw, ok := numericValue(stringValue(object, "usagePercent", "usedPercent", "percentUsed", "percentage", "percent")); ok {
		used = raw / 100
	} else {
		usedRaw := stringValue(object, "used", "consumed")
		limitRaw := stringValue(object, "limit", "total", "quota")
		usedValue, usedOK := numericValue(usedRaw)
		limit, limitOK := numericValue(limitRaw)
		if !usedOK || !limitOK || limit <= 0 {
			return nil
		}
		used = usedValue / limit
	}
	used = maxMinFraction(used)
	remaining := 1 - used
	return &quotaWindow{Name: name, UsedFraction: floatPtr(used), RemainingFraction: &remaining, ResetAt: timeValue(object, "resetAt", "resetsAt", "nextReset", "resetTime")}
}

func quotaWindowFromLimit(object map[string]any, name string) *quotaWindow {
	if object == nil {
		return nil
	}
	limit := stringValue(object, "limit", "total")
	remaining := stringValue(object, "remaining")
	usedText := stringValue(object, "used", "usage")
	limitValue, limitOK := numericValue(limit)
	remainingValue, remainingOK := numericValue(remaining)
	if !limitOK || !remainingOK || limitValue <= 0 {
		return nil
	}
	usedValue, usedOK := numericValue(usedText)
	if !usedOK {
		usedValue = limitValue - remainingValue
		usedText = decimalString(usedValue)
	}
	if remainingValue < 0 || usedValue > limitValue {
		excess := maxFloat(usedValue-limitValue, -remainingValue)
		zero := 0.0
		one := 1.0
		return &quotaWindow{Name: name, RemainingFraction: &zero, UsedFraction: &one, RemainingAmount: "0", TotalAmount: limit, UsedAmount: usedText, ExcessAmount: decimalString(excess), Unit: stringValue(object, "unit"), ResetAt: timeValue(object, "resetAt", "reset_at", "resetTime", "reset_time", "resetsAt"), ExpiresAt: timeValue(object, "expires_at", "expiresAt")}
	}
	frac := remainingValue / limitValue
	used := 1 - frac
	return &quotaWindow{Name: name, RemainingFraction: &frac, UsedFraction: &used, RemainingAmount: remaining, TotalAmount: limit, UsedAmount: usedText, Unit: stringValue(object, "unit"), ResetAt: timeValue(object, "resetAt", "reset_at", "resetTime", "reset_time", "resetsAt"), ExpiresAt: timeValue(object, "expires_at", "expiresAt")}
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func floatPtr(value float64) *float64 { return &value }

func balanceSnapshot(candidate credentialCandidate, adapter, currency, total, available string) accountSnapshot {
	snapshot := accountSnapshot{
		AccountID:    candidateAccountID(candidate),
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    adapter,
		BaseURL:      candidate.BaseURL,
		Kind:         kindBalance,
		Status:       statusOK,
		Capabilities: []string{"balance"},
		Balances:     []moneyBalance{{Amount: total, Currency: currency, BalanceType: "total"}},
	}
	if available != "" && available != total {
		snapshot.Balances = append(snapshot.Balances, moneyBalance{Amount: available, Currency: currency, BalanceType: "available"})
	}
	if value, ok := numericValue(total); ok && value <= 0 {
		snapshot.Status = statusCritical
	}
	return snapshot
}

func isCurrency(value string) bool {
	switch value {
	case "CNY", "USD", "EUR", "JPY", "GBP", "HKD":
		return true
	default:
		return false
	}
}

func unsupportedSnapshot(candidate credentialCandidate, adapter, message string) accountSnapshot {
	return accountSnapshot{
		AccountID:    candidateAccountID(candidate),
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    adapter,
		BaseURL:      candidate.BaseURL,
		Kind:         kindUnsupported,
		Status:       statusUnknown,
		Capabilities: []string{"unsupported"},
		CheckedAt:    time.Now().UTC(),
		Error:        &snapshotError{Code: "UNSUPPORTED", Message: message},
	}
}

func queryJSON(callbackID, endpoint, token, proxyURL string) ([]byte, error) {
	return queryJSONContext(context.Background(), callbackID, endpoint, token, proxyURL)
}

func queryJSONContext(ctx context.Context, callbackID, endpoint, token, proxyURL string) ([]byte, error) {
	response, err := queryEndpointContext(ctx, callbackID, endpoint, token, proxyURL)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	if len(response.Body) > 1<<20 {
		return nil, fmt.Errorf("upstream response exceeds 1 MiB")
	}
	return response.Body, nil
}
