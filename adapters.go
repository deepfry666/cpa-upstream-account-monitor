package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type monitorConfig struct {
	Adapter             string `yaml:"adapter"`
	BaseURL             string `yaml:"base_url"`
	APIKeyEnv           string `yaml:"api_key_env"`
	ManagementAPIKeyEnv string `yaml:"management_api_key_env"`
	ManagementUserIDEnv string `yaml:"management_user_id_env"`
}

type newAPIQuotaStatus struct {
	QuotaPerUnit    float64
	DisplayType     string
	USDExchangeRate float64
	Fallback        bool
}

func defaultNewAPIQuotaStatus() newAPIQuotaStatus {
	return newAPIQuotaStatus{QuotaPerUnit: 500000, DisplayType: "USD", USDExchangeRate: 1, Fallback: true}
}

func monitorFor(candidate credentialCandidate) monitorConfig {
	state.mu.RLock()
	monitors := state.cfg.Monitors
	state.mu.RUnlock()
	config := monitorConfig{}
	for _, key := range []string{candidate.AuthIndex, candidate.AuthID, candidate.Provider} {
		if value, ok := monitors[key]; ok {
			config = value
			break
		}
	}
	if state.prefs != nil {
		if adapter := state.prefs.adapter(candidate.AuthIndex, ""); adapter != "" {
			config.Adapter = adapter
		}
	}
	return config
}

func adapterFor(candidate credentialCandidate) (string, credentialCandidate, bool) {
	p := strings.ToLower(strings.TrimSpace(candidate.Provider))
	name := strings.ToLower(strings.TrimSpace(candidate.Name))
	rawBase := strings.ToLower(strings.TrimSpace(candidate.BaseURL))
	host := hostname(candidate.BaseURL)
	commandCode := matchesCommandCode(p, name, rawBase, host)

	config := monitorFor(candidate)
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

func matchesCommandCode(provider, name, rawBase, host string) bool {
	combined := provider + " " + name + " " + rawBase
	return strings.Contains(combined, "commandcode") ||
		strings.Contains(combined, "command-code") ||
		strings.Contains(provider, "goat") ||
		strings.Contains(name, "goat") ||
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
	candidate.Name = state.prefs.name(candidate.AuthIndex, candidate.Name)
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
		snapshot, err = queryDeepSeekSnapshot(callbackID, candidate, storage, attributes)
	case "zai-usage":
		snapshot, err = queryZaiSnapshot(callbackID, candidate, token)
	case "moonshot-balance":
		snapshot, err = queryMoonshotSnapshot(callbackID, candidate, token)
	case "kimi-coding":
		snapshot, err = queryKimiSnapshot(callbackID, candidate, token)
	case "opencode-go":
		snapshot, err = queryOpenCodeSnapshot(callbackID, candidate, token)
	case "newapi-usage":
		snapshot, err = queryNewAPISnapshot(callbackID, candidate, token)
	case "sub2api-usage":
		snapshot, err = querySub2APISnapshot(callbackID, candidate, token)
	case "relay-usage":
		snapshot, err = queryRelaySnapshot(callbackID, candidate, token)
	case "commandcode-goat":
		snapshot, err = queryCommandCodeSnapshot(callbackID, candidate, token)
	default:
		err = fmt.Errorf("adapter %q is not implemented", adapter)
	}
	if err != nil {
		return accountSnapshot{}, err
	}
	now := time.Now().UTC()
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

func queryZaiSnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpoint(candidate.BaseURL, "https://open.bigmodel.cn", "/api/monitor/usage/quota/limit")
	if err != nil {
		return accountSnapshot{}, err
	}
	response, err := queryEndpointAuth(callbackID, endpoint, token, candidate.ProxyURL)
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
		AccountID:    candidate.AuthIndex,
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "zai-usage",
		BaseURL:      candidate.BaseURL,
		Kind:         kindPeriodQuota,
		Status:       statusOK,
		Capabilities: []string{"rolling_quota"},
	}
	tokenIndex := 0
	timeIndex := 0
	for _, limit := range limits {
		kind := strings.ToUpper(stringValue(limit, "type", "kind"))
		name := firstNonEmpty(stringValue(limit, "window", "period", "cycle", "name"), kind)
		if kind == "TOKENS_LIMIT" {
			tokenIndex++
			if name == kind {
				if tokenIndex == 1 {
					name = "5h"
				} else if tokenIndex == 2 {
					name = "7d"
				} else {
					name = fmt.Sprintf("Token quota #%d", tokenIndex)
				}
			}
		} else if kind == "TIME_LIMIT" {
			timeIndex++
			if name == kind {
				name = "Time quota"
				if timeIndex > 1 {
					name = fmt.Sprintf("Time quota #%d", timeIndex)
				}
			}
		}
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
		if raw > 1 {
			raw /= 100
		}
		remaining = raw
		used = 1 - remaining
	} else if raw, ok := numericValue(usedRaw); ok {
		if raw > 1 {
			raw /= 100
		}
		used = raw
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
	unit := firstNonEmpty(stringValue(object, "unit"), stringValue(object, "metric"))
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

func queryMoonshotSnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpoint(candidate.BaseURL, "https://api.moonshot.cn", "/v1/users/me/balance")
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSON(callbackID, endpoint, token, candidate.ProxyURL)
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

func queryKimiSnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpoint(candidate.BaseURL, "https://api.kimi.com", "/coding/v1/usages")
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSON(callbackID, endpoint, token, candidate.ProxyURL)
	if err != nil {
		return accountSnapshot{}, err
	}
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode Kimi response: %w", err)
	}
	data := objectValue(root, "data")
	snapshot := accountSnapshot{
		AccountID:    candidate.AuthIndex,
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

func queryOpenCodeSnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpoint(candidate.BaseURL, "https://opencode.ai", "/zen/go/v1/usage")
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSON(callbackID, endpoint, token, candidate.ProxyURL)
	if err != nil {
		return accountSnapshot{}, err
	}
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode OpenCode Go response: %w", err)
	}
	usage := objectValue(root, "usage")
	if usage == nil {
		usage = root
	}
	snapshot := accountSnapshot{
		AccountID:    candidate.AuthIndex,
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "opencode-go",
		BaseURL:      candidate.BaseURL,
		Kind:         kindPeriodQuota,
		Status:       statusOK,
		Capabilities: []string{"rolling_quota"},
	}
	for key, name := range map[string]string{"rolling": "session", "weekly": "weekly", "monthly": "monthly"} {
		if window := quotaWindowFromPercent(objectValue(usage, key), name); window != nil {
			snapshot.Windows = append(snapshot.Windows, *window)
		}
	}
	if len(snapshot.Windows) < 2 {
		return accountSnapshot{}, fmt.Errorf("OpenCode Go response contains insufficient quota windows")
	}
	return snapshot, nil
}

func queryNewAPISnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpoint(candidate.BaseURL, "", "/api/usage/token")
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSON(callbackID, endpoint, token, candidate.ProxyURL)
	if err != nil {
		return accountSnapshot{}, err
	}
	quotaStatus, errStatus := queryNewAPIQuotaStatus(callbackID, candidate)
	if errStatus != nil {
		return accountSnapshot{}, errStatus
	}
	return parseNewAPISnapshotWithQuotaStatus(callbackID, candidate, body, quotaStatus)
}

func queryNewAPIQuotaStatus(callbackID string, candidate credentialCandidate) (newAPIQuotaStatus, error) {
	endpoint, err := fixedEndpoint(candidate.BaseURL, "", "/api/status")
	if err != nil {
		return newAPIQuotaStatus{}, err
	}
	response, err := queryEndpointAuth(callbackID, endpoint, "", candidate.ProxyURL)
	if err != nil {
		return newAPIQuotaStatus{}, err
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return defaultNewAPIQuotaStatus(), nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newAPIQuotaStatus{}, fmt.Errorf("NewAPI status endpoint returned HTTP %d", response.StatusCode)
	}
	root, err := decodeObject(response.Body)
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
	if displayType != "USD" && displayType != "CNY" {
		return newAPIQuotaStatus{}, fmt.Errorf("NewAPI display currency %q is unsupported", displayType)
	}
	exchangeRate := 1.0
	if raw := stringValue(data, "usd_exchange_rate"); raw != "" {
		if parsed, ok := numericValue(raw); ok && parsed > 0 {
			exchangeRate = parsed
		}
	}
	return newAPIQuotaStatus{QuotaPerUnit: quotaPerUnit, DisplayType: displayType, USDExchangeRate: exchangeRate}, nil
}

func newAPIAmount(raw string, status newAPIQuotaStatus) string {
	value, ok := numericValue(raw)
	if !ok {
		return raw
	}
	if status.QuotaPerUnit <= 0 {
		return raw
	}
	amount := value / status.QuotaPerUnit * status.USDExchangeRate
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(amount, 'f', 6, 64), "0"), ".")
}

func parseNewAPISnapshotWithQuotaStatus(callbackID string, candidate credentialCandidate, body []byte, quotaStatus newAPIQuotaStatus) (accountSnapshot, error) {
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode NewAPI response: %w", err)
	}
	data := objectValue(root, "data")
	if data == nil {
		data = root
	}
	if strings.EqualFold(stringValue(root, "code"), "false") {
		return accountSnapshot{}, fmt.Errorf("NewAPI token response reports failure")
	}
	rawTotal := stringValue(data, "total_granted", "total", "quota")
	rawUsed := stringValue(data, "total_used", "used", "usage")
	rawRemaining := stringValue(data, "total_available", "remaining", "available")
	if rawTotal == "" && rawRemaining == "" {
		return accountSnapshot{}, fmt.Errorf("NewAPI response contains no quota values")
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
		Unit:      quotaStatus.DisplayType,
		ResetAt:   timeValue(data, "expires_at", "reset_at"),
	}
	if unlimited {
		// New API may include historical quota counters alongside the unlimited
		// marker. Those counters are not a finite remaining/total balance.
		quantity.Remaining = ""
		quantity.Total = ""
		quantity.Used = ""
	}
	snapshot := accountSnapshot{
		AccountID:    candidate.AuthIndex,
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "newapi-usage",
		BaseURL:      candidate.BaseURL,
		Kind:         kindQuota,
		Status:       statusOK,
		Capabilities: []string{"quota"},
		Quantities:   []quotaQuantity{quantity},
		Details: map[string]any{
			"source":             "/api/usage/token/",
			"quota_per_unit":     quotaStatus.QuotaPerUnit,
			"quota_display_type": quotaStatus.DisplayType,
			"usd_exchange_rate":  quotaStatus.USDExchangeRate,
			"unlimited_quota":    unlimited,
		},
	}
	monitor := monitorFor(candidate)
	if managementCredentialFor(candidate, monitor) == "" {
		snapshot.Warnings = append(snapshot.Warnings, "当前仅显示这个 API Key 的额度；要显示 NewAPI 账户总额度，请配置管理凭据")
	} else if accountQuantity, errAccount := queryNewAPIAccountQuantity(callbackID, candidate, monitor, quotaStatus); errAccount == nil {
		snapshot.Quantities = append([]quotaQuantity{*accountQuantity}, snapshot.Quantities...)
	} else {
		snapshot.Warnings = append(snapshot.Warnings, "NewAPI 账户总额度暂时无法查询，当前显示 API Key 额度")
	}
	return snapshot, nil
}

func managementCredentialFor(candidate credentialCandidate, monitor monitorConfig) string {
	if state.prefs != nil {
		if value := state.prefs.pat(candidate.AuthIndex); value != "" {
			return value
		}
	}
	if monitor.ManagementAPIKeyEnv == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(monitor.ManagementAPIKeyEnv))
}

func queryNewAPIAccountQuantity(callbackID string, candidate credentialCandidate, monitor monitorConfig, quotaStatus newAPIQuotaStatus) (*quotaQuantity, error) {
	managementToken := managementCredentialFor(candidate, monitor)
	if managementToken == "" {
		return nil, fmt.Errorf("management credential is not configured")
	}
	endpoint, err := fixedEndpoint(candidate.BaseURL, "", "/api/user/self")
	if err != nil {
		return nil, err
	}
	response, err := queryEndpointAuth(callbackID, endpoint, "Bearer "+managementToken, candidate.ProxyURL)
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
		Unit:      quotaStatus.DisplayType,
	}, nil
}

func querySub2APISnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	endpoint, err := fixedEndpoint(candidate.BaseURL, "", "/v1/usage")
	if err != nil {
		return accountSnapshot{}, err
	}
	body, err := queryJSON(callbackID, endpoint, token, candidate.ProxyURL)
	if err != nil {
		return accountSnapshot{}, err
	}
	root, err := decodeObject(body)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("decode Sub2API response: %w", err)
	}
	data := objectValue(root, "data")
	if data == nil {
		data = root
	}
	quota := objectValue(data, "quota")
	total := firstNonEmpty(stringValue(data, "total", "limit"), stringValue(quota, "total", "limit"))
	used := firstNonEmpty(stringValue(data, "used", "usage"), stringValue(quota, "used", "usage"))
	remaining := firstNonEmpty(stringValue(data, "remaining", "balance", "available"), stringValue(quota, "remaining", "available"))
	unit := firstNonEmpty(stringValue(data, "unit"), stringValue(quota, "unit"), "USD")
	if mode := strings.ToLower(stringValue(data, "mode")); mode == "quota_limited" || objectValue(data, "subscription") != nil {
		if snapshot, ok := sub2APIPeriodSnapshot(candidate, data, quota, unit); ok {
			snapshot = attachSub2APIDetails(callbackID, candidate, token, snapshot, root)
			return snapshot, nil
		}
	}
	if remaining != "" {
		snapshot := accountSnapshot{
			AccountID:    candidate.AuthIndex,
			AccountName:  candidate.Name,
			Provider:     candidate.Provider,
			AdapterID:    "sub2api-usage",
			BaseURL:      candidate.BaseURL,
			Kind:         kindBalance,
			Status:       statusOK,
			Capabilities: []string{"balance"},
			Quantities:   []quotaQuantity{{Name: "remaining", Remaining: remaining, Total: total, Used: used, Unit: unit}},
		}
		if isCurrency(strings.ToUpper(unit)) {
			snapshot.Balances = append(snapshot.Balances, moneyBalance{Amount: remaining, Currency: strings.ToUpper(unit), BalanceType: "remaining"})
		}
		if !isCurrency(strings.ToUpper(unit)) {
			snapshot.Kind = kindQuota
			snapshot.Balances = nil
			snapshot.Capabilities = []string{"quota"}
		}
		if value, ok := numericValue(remaining); ok && value < 0 && value != -1 {
			return accountSnapshot{}, fmt.Errorf("Sub2API response contains an invalid remaining value")
		}
		snapshot = attachSub2APIDetails(callbackID, candidate, token, snapshot, root)
		return snapshot, nil
	}
	if snapshot, ok := sub2APIPeriodSnapshot(candidate, data, quota, unit); ok {
		snapshot = attachSub2APIDetails(callbackID, candidate, token, snapshot, root)
		return snapshot, nil
	}
	if balance := stringValue(data, "balance", "available_balance"); balance != "" {
		snapshot := balanceSnapshot(candidate, "sub2api-usage", firstNonEmpty(stringValue(data, "currency"), "USD"), balance, "")
		snapshot = attachSub2APIDetails(callbackID, candidate, token, snapshot, root)
		return snapshot, nil
	}
	return accountSnapshot{}, fmt.Errorf("Sub2API response contains no supported quota values")
}

func attachSub2APIDetails(callbackID string, candidate credentialCandidate, token string, snapshot accountSnapshot, root map[string]any) accountSnapshot {
	snapshot.Details = curatedDetails(root)
	billingEndpoint, err := fixedEndpoint(candidate.BaseURL, "", "/v1/sub2api/billing")
	if err != nil {
		return snapshot
	}
	response, err := queryEndpoint(callbackID, billingEndpoint, token, candidate.ProxyURL)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		if snapshot.Details != nil {
			snapshot.Details["billing_status"] = "unavailable"
		}
		return snapshot
	}
	billingRoot, err := decodeObject(response.Body)
	if err != nil {
		return snapshot
	}
	if snapshot.Details == nil {
		snapshot.Details = map[string]any{}
	}
	snapshot.Details["billing"] = sanitizeJSON(billingRoot, 0)
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

func queryRelaySnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	newAPIEndpoint, err := fixedEndpoint(candidate.BaseURL, "", "/api/usage/token")
	if err != nil {
		return accountSnapshot{}, err
	}
	response, err := queryEndpoint(callbackID, newAPIEndpoint, token, candidate.ProxyURL)
	if err != nil {
		return accountSnapshot{}, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		quotaStatus, errStatus := queryNewAPIQuotaStatus(callbackID, candidate)
		if errStatus != nil {
			return accountSnapshot{}, errStatus
		}
		return parseNewAPISnapshotWithQuotaStatus(callbackID, candidate, response.Body, quotaStatus)
	}
	if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusMethodNotAllowed {
		return accountSnapshot{}, fmt.Errorf("relay quota endpoint returned HTTP %d", response.StatusCode)
	}

	sub2APIEndpoint, err := fixedEndpoint(candidate.BaseURL, "", "/v1/usage")
	if err != nil {
		return accountSnapshot{}, err
	}
	sub2APIEndpoint = addSub2APIUsageQuery(sub2APIEndpoint)
	response, err = queryEndpoint(callbackID, sub2APIEndpoint, token, candidate.ProxyURL)
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
		snapshot = attachSub2APIDetails(callbackID, candidate, token, snapshot, root)
	}
	return snapshot, nil
}

func queryEndpoint(callbackID, endpoint, token, proxyURL string) (hostHTTPResponse, error) {
	return queryEndpointAuth(callbackID, endpoint, "Bearer "+token, proxyURL)
}

func queryEndpointAuth(callbackID, endpoint, authorization, proxyURL string) (hostHTTPResponse, error) {
	return queryEndpointWithHeaders(callbackID, endpoint, authorization, proxyURL, nil)
}

func queryEndpointWithHeaders(callbackID, endpoint, authorization, proxyURL string, extraHeaders map[string]string) (hostHTTPResponse, error) {
	if strings.TrimSpace(proxyURL) == "" {
		headers := map[string][]string{"Accept": {"application/json"}}
		if authorization != "" {
			headers["Authorization"] = []string{authorization}
		}
		for key, value := range extraHeaders {
			headers[key] = []string{value}
		}
		return hostHTTPDo(callbackID, endpoint, headers)
	}
	proxy, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || (proxy.Scheme != "http" && proxy.Scheme != "https") {
		headers := map[string][]string{"Accept": {"application/json"}}
		if authorization != "" {
			headers["Authorization"] = []string{authorization}
		}
		for key, value := range extraHeaders {
			headers[key] = []string{value}
		}
		return hostHTTPDo(callbackID, endpoint, headers)
	}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cpa-upstream-monitor/"+pluginVersion)
	for key, value := range extraHeaders {
		request.Header.Set(key, value)
	}
	state.mu.RLock()
	timeout := time.Duration(state.cfg.RequestTimeoutSecond) * time.Second
	state.mu.RUnlock()
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy)},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many upstream redirects")
			}
			previous := via[len(via)-1]
			if !strings.EqualFold(previous.URL.Scheme, request.URL.Scheme) || !strings.EqualFold(previous.URL.Host, request.URL.Host) {
				return fmt.Errorf("cross-host upstream redirect is not supported")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return hostHTTPResponse{}, err
	}
	return hostHTTPResponse{StatusCode: response.StatusCode, Headers: response.Header, Body: body}, nil
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
	quota := objectValue(data, "quota")
	if total := firstNonEmpty(stringValue(data, "total", "limit", "quota"), stringValue(quota, "total", "limit")); total != "" {
		remaining := stringValue(data, "remaining", "available", "balance")
		if remaining == "" {
			remaining = stringValue(quota, "remaining", "available")
		}
		used := firstNonEmpty(stringValue(data, "used", "usage"), stringValue(quota, "used", "usage"))
		if remaining != "" {
			unit := firstNonEmpty(stringValue(data, "unit"), stringValue(quota, "unit"), "quota")
			if strings.ToLower(stringValue(data, "mode")) == "quota_limited" || objectValue(data, "subscription") != nil {
				if snapshot, ok := sub2APIPeriodSnapshot(candidate, data, quota, unit); ok {
					return snapshot, nil
				}
			}
			snapshot := accountSnapshot{AccountID: candidate.AuthIndex, AccountName: candidate.Name, Provider: candidate.Provider, AdapterID: "sub2api-usage", BaseURL: candidate.BaseURL, Kind: kindQuota, Status: statusOK, Capabilities: []string{"quota"}, Quantities: []quotaQuantity{{Name: "account", Remaining: remaining, Total: total, Used: used, Unit: unit, ResetAt: timeValue(data, "expires_at", "reset_at")}}}
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

func sub2APIPeriodSnapshot(candidate credentialCandidate, data, quota map[string]any, unit string) (accountSnapshot, bool) {
	snapshot := accountSnapshot{
		AccountID:    candidate.AuthIndex,
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "sub2api-usage",
		BaseURL:      candidate.BaseURL,
		Kind:         kindPeriodQuota,
		Status:       statusOK,
		Capabilities: []string{"rolling_quota"},
	}
	if quota != nil {
		if window := quotaWindowFromValues("total", stringValue(quota, "remaining", "available"), stringValue(quota, "limit", "total"), stringValue(quota, "used", "usage"), unit, timeValue(data, "expires_at", "reset_at")); window != nil {
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
			if window := quotaWindowFromValues(period, stringValue(subscription, period+"_limit_usd"), stringValue(subscription, period+"_limit_usd"), stringValue(subscription, period+"_usage_usd"), firstNonEmpty(stringValue(subscription, "unit"), unit, "USD"), timeValue(subscription, "reset_at", "expires_at")); window != nil {
				// The helper receives remaining first. Subscription responses expose
				// usage and limit, so convert the usage into remaining here.
				used := stringValue(subscription, period+"_usage_usd")
				limit := stringValue(subscription, period+"_limit_usd")
				if usedValue, okUsed := numericValue(used); okUsed {
					if limitValue, okLimit := numericValue(limit); okLimit {
						window = quotaWindowFromValues(period, strconv.FormatFloat(limitValue-usedValue, 'f', -1, 64), limit, used, firstNonEmpty(stringValue(subscription, "unit"), unit, "USD"), timeValue(subscription, "reset_at", "expires_at"))
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
	if remainingValue < 0 {
		return nil
	}
	if remainingValue > limitValue {
		remainingValue = limitValue
	}
	remainingText := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(remainingValue, 'f', 6, 64), "0"), ".")
	frac := remainingValue / limitValue
	usedFraction := 1 - frac
	return &quotaWindow{Name: name, RemainingFraction: &frac, UsedFraction: &usedFraction, RemainingAmount: firstNonEmpty(remaining, remainingText), TotalAmount: total, Unit: unit, ResetAt: resetAt}
}

func fixedEndpoint(baseURL, defaultBase, path string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = defaultBase
	}
	if base == "" {
		return "", fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("upstream base URL must use HTTPS")
	}
	u.Path = path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
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

func quotaWindowFromPercent(object map[string]any, name string) *quotaWindow {
	if object == nil {
		return nil
	}
	usedRaw := stringValue(object, "usagePercent", "usedPercent", "percentUsed", "percentage", "percent")
	used, ok := numericValue(usedRaw)
	if !ok {
		usedRaw = stringValue(object, "used", "consumed")
		limitRaw := stringValue(object, "limit", "total", "quota")
		usedValue, usedOK := numericValue(usedRaw)
		limit, limitOK := numericValue(limitRaw)
		if !usedOK || !limitOK || limit <= 0 {
			return nil
		}
		used = usedValue / limit * 100
	}
	if used <= 1 && object["percent"] == nil {
		used *= 100
	}
	used = clampPercent(used)
	remaining := 1 - used/100
	return &quotaWindow{Name: name, UsedFraction: floatPtr(used / 100), RemainingFraction: &remaining, ResetAt: timeValue(object, "resetAt", "resetsAt", "nextReset", "resetTime")}
}

func quotaWindowFromLimit(object map[string]any, name string) *quotaWindow {
	if object == nil {
		return nil
	}
	limit := stringValue(object, "limit", "total")
	remaining := stringValue(object, "remaining")
	limitValue, limitOK := numericValue(limit)
	remainingValue, remainingOK := numericValue(remaining)
	if !limitOK || !remainingOK || limitValue <= 0 {
		return nil
	}
	frac := remainingValue / limitValue
	used := 1 - frac
	return &quotaWindow{Name: name, RemainingFraction: &frac, UsedFraction: &used, RemainingAmount: remaining, TotalAmount: limit, Unit: stringValue(object, "unit"), ResetAt: timeValue(object, "resetAt", "reset_at", "resetTime", "reset_time", "resetsAt")}
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
		AccountID:    candidate.AuthIndex,
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
		AccountID:    candidate.AuthIndex,
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
	response, err := queryEndpoint(callbackID, endpoint, token, proxyURL)
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
