package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	clinePassAPIBase                 = "https://api.cline.bot"
	clinePassUsageUnitsPerUSD        = 100000000
	clinePassBalanceUnitsPerUSD      = 1000000
	clinePassBalanceUnitsPerCredit   = 10000
	clinePassUsagePageLimit          = 100
	clinePassUsageMaximumPages       = 10
	clinePassUsageLookbackWindowDays = 30
)

func matchesClinePass(provider, host string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return strings.Contains(provider, "cline pass") ||
		strings.Contains(provider, "clinepass") ||
		(host == "api.cline.bot" && strings.Contains(provider, "cline-pass"))
}

func queryClinePassSnapshot(ctx context.Context, callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	meBody, err := queryClinePassJSON(ctx, callbackID, candidate, token, "/api/v1/users/me", nil)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("查询 Cline Pass 账户失败: %w", err)
	}
	meData, err := clinePassEnvelopeData(meBody)
	if err != nil {
		return accountSnapshot{}, err
	}
	userID := strings.TrimSpace(stringValue(meData, "id", "uid", "userId"))
	if userID == "" {
		return accountSnapshot{}, fmt.Errorf("Cline Pass 账户响应缺少用户 ID")
	}
	balanceBody, err := queryClinePassJSON(ctx, callbackID, candidate, token, "/api/v1/users/"+url.PathEscape(userID)+"/balance", nil)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("查询 Cline Pass 余额失败: %w", err)
	}
	planBody, err := queryClinePassJSON(ctx, callbackID, candidate, token, "/api/v1/users/me/plan", nil)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("查询 Cline Pass 套餐失败: %w", err)
	}
	usageItems, err := queryClinePassUsageItems(ctx, callbackID, candidate, token, userID, time.Now().UTC())
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("查询 Cline Pass 用量失败: %w", err)
	}
	return parseClinePassSnapshot(candidate, meBody, balanceBody, planBody, usageItems, time.Now().UTC())
}

func queryClinePassJSON(ctx context.Context, callbackID string, candidate credentialCandidate, token, path string, query url.Values) ([]byte, error) {
	endpoint, err := fixedEndpointWithOptions(candidate.BaseURL, clinePassAPIBase, path, endpointOptions{
		AllowInternalHTTP:    candidate.AllowInternalHTTP,
		StripInferenceSuffix: true,
	})
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	response, err := queryEndpointWithHeadersContext(ctx, callbackID, endpoint, "Bearer "+token, candidateProxyURL(candidate), map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Cline Pass endpoint returned HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}

func queryClinePassUsageItems(ctx context.Context, callbackID string, candidate credentialCandidate, token, userID string, now time.Time) ([]map[string]any, error) {
	cursor := ""
	items := make([]map[string]any, 0, clinePassUsagePageLimit)
	for page := 0; page < clinePassUsageMaximumPages; page++ {
		query := url.Values{}
		query.Set("limit", fmt.Sprintf("%d", clinePassUsagePageLimit))
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		body, err := queryClinePassJSON(ctx, callbackID, candidate, token, "/api/v1/users/"+url.PathEscape(userID)+"/usages", query)
		if err != nil {
			return nil, err
		}
		data, err := clinePassEnvelopeData(body)
		if err != nil {
			return nil, err
		}
		pageItems := objectArray(data, "items")
		items = append(items, pageItems...)
		cursor = strings.TrimSpace(stringValue(data, "nextToken"))
		if cursor == "" || len(pageItems) == 0 {
			break
		}
		oldest := time.Time{}
		for _, item := range pageItems {
			if created := timeValue(item, "createdAt", "created_at"); created != nil && (oldest.IsZero() || created.Before(oldest)) {
				oldest = *created
			}
		}
		if !oldest.IsZero() && oldest.Before(now.Add(-clinePassUsageLookbackWindowDays*24*time.Hour)) {
			break
		}
	}
	return items, nil
}

func clinePassEnvelopeData(body []byte) (map[string]any, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode Cline Pass response: %w", err)
	}
	if errorText := strings.TrimSpace(stringValue(root, "error")); errorText != "" {
		return nil, fmt.Errorf("Cline Pass response error: %s", errorText)
	}
	data := objectValue(root, "data")
	if data == nil {
		return nil, fmt.Errorf("Cline Pass response contains no data")
	}
	return data, nil
}

func parseClinePassSnapshot(candidate credentialCandidate, meBody, balanceBody, planBody []byte, usageItems []map[string]any, now time.Time) (accountSnapshot, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	meData, err := clinePassEnvelopeData(meBody)
	if err != nil {
		return accountSnapshot{}, err
	}
	balanceData, err := clinePassEnvelopeData(balanceBody)
	if err != nil {
		return accountSnapshot{}, err
	}
	planData, err := clinePassEnvelopeData(planBody)
	if err != nil {
		return accountSnapshot{}, err
	}
	balanceRaw, ok := commandCodeNumber(balanceData, "balance")
	if !ok || balanceRaw < 0 {
		return accountSnapshot{}, fmt.Errorf("Cline Pass 余额值无效")
	}
	plan := objectValue(planData, "plan")
	entitlements := objectValue(plan, "entitlements")
	passEntitlement := objectValue(entitlements, "cline_pass")
	thresholds := objectValue(passEntitlement, "inferenceCapThreshold")

	fiveHourCap, _ := commandCodeNumber(thresholds, "last5HoursUsageCostUSDPerUser")
	weeklyCap, _ := commandCodeNumber(thresholds, "last7daysUsageCostUSDPerUser")
	monthlyCap, _ := commandCodeNumber(thresholds, "last30daysUsageCostUSDPerUser")
	fiveHourUsed, weeklyUsed, monthlyUsed := clinePassUsageTotals(usageItems, now)

	balanceUSD := balanceRaw / clinePassBalanceUnitsPerUSD
	balanceCredits := balanceRaw / clinePassBalanceUnitsPerCredit
	planName := firstNonEmpty(stringValue(plan, "displayName", "name"), "Cline Pass")

	snapshot := accountSnapshot{
		AccountID:    candidateAccountID(candidate),
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "clinepass-usage",
		BaseURL:      clinePassAPIBase,
		Kind:         kindQuota,
		Status:       statusOK,
		Capabilities: []string{"balance", "subscription_usage", "quota_windows", "usage_statistics"},
		Balances: []moneyBalance{{
			Amount:      decimalString(balanceUSD),
			Currency:    "USD",
			BalanceType: "remaining",
		}},
		Quantities: []quotaQuantity{{
			Name:      "Cline Pass Credits",
			Scope:     "account",
			Source:    "balance",
			Display:   "remaining",
			Remaining: decimalString(balanceCredits),
			Unit:      "credits",
		}},
	}

	for _, definition := range []struct {
		name string
		used float64
		cap  float64
	}{
		{name: "fiveHour", used: fiveHourUsed, cap: fiveHourCap},
		{name: "weekly", used: weeklyUsed, cap: weeklyCap},
		{name: "month", used: monthlyUsed, cap: monthlyCap},
	} {
		if definition.cap <= 0 {
			continue
		}
		capUSD := definition.cap / clinePassUsageUnitsPerUSD
		usedUSD := definition.used / clinePassUsageUnitsPerUSD
		remaining := maxFloat(capUSD-usedUSD, 0)
		window := quotaWindowFromValues(
			definition.name,
			decimalString(remaining),
			decimalString(capUSD),
			decimalString(usedUSD),
			"USD",
			nil,
		)
		if window != nil {
			window.Source = "derived"
			snapshot.Windows = append(snapshot.Windows, *window)
		}
	}

	usageSummary := clinePassUsageSummary(usageItems, now)
	snapshot.Details = map[string]any{
		"plan": map[string]any{
			"id":                 stringValue(plan, "id", "priceId"),
			"name":               planName,
			"displayName":        planName,
			"interval":           stringValue(plan, "interval"),
			"pricePerSeatCents":  plan["pricePerSeatCents"],
			"active":             boolValue(plan, "isActive"),
			"currentPeriodStart": timeValue(planData, "currentPeriodStart"),
			"currentPeriodEnd":   timeValue(planData, "currentPeriodEnd"),
			"limits": map[string]any{
				"fiveHourUSD": decimalString(fiveHourCap / clinePassUsageUnitsPerUSD),
				"weeklyUSD":   decimalString(weeklyCap / clinePassUsageUnitsPerUSD),
				"monthlyUSD":  decimalString(monthlyCap / clinePassUsageUnitsPerUSD),
			},
		},
		"usage": usageSummary,
		"account": map[string]any{
			"userName": stringValue(meData, "displayName", "email"),
		},
	}
	return snapshot, nil
}

func clinePassUsageTotals(items []map[string]any, now time.Time) (fiveHour, weekly, monthly float64) {
	fiveHourCutoff := now.Add(-5 * time.Hour)
	weeklyCutoff := now.Add(-7 * 24 * time.Hour)
	monthlyCutoff := now.Add(-clinePassUsageLookbackWindowDays * 24 * time.Hour)
	for _, item := range items {
		created := timeValue(item, "createdAt", "created_at")
		cost, ok := commandCodeNumber(item, "costUsd", "cost_usd")
		if created == nil || !ok || cost < 0 {
			continue
		}
		if created.After(fiveHourCutoff) {
			fiveHour += cost
		}
		if created.After(weeklyCutoff) {
			weekly += cost
		}
		if created.After(monthlyCutoff) {
			monthly += cost
		}
	}
	return fiveHour, weekly, monthly
}

func clinePassUsageSummary(items []map[string]any, now time.Time) map[string]any {
	cutoff := now.Add(-clinePassUsageLookbackWindowDays * 24 * time.Hour)
	models := map[string]map[string]any{}
	summary := map[string]any{
		"count":            0,
		"costUSD":          "0",
		"creditsUsed":      "0",
		"promptTokens":     0,
		"completionTokens": 0,
		"totalTokens":      0,
		"models":           []map[string]any{},
	}
	var cost, credits float64
	count := 0
	var promptTokens, completionTokens, totalTokens float64
	for _, item := range items {
		created := timeValue(item, "createdAt", "created_at")
		if created != nil && created.Before(cutoff) {
			continue
		}
		itemCost, _ := commandCodeNumber(item, "costUsd", "cost_usd")
		itemCredits, _ := commandCodeNumber(item, "creditsUsed", "credits_used")
		itemPrompt, _ := commandCodeNumber(item, "promptTokens", "prompt_tokens")
		itemCompletion, _ := commandCodeNumber(item, "completionTokens", "completion_tokens")
		itemTotal, _ := commandCodeNumber(item, "totalTokens", "total_tokens")
		cost += itemCost
		credits += itemCredits
		promptTokens += itemPrompt
		completionTokens += itemCompletion
		totalTokens += itemTotal
		count++
		modelName := firstNonEmpty(stringValue(item, "aiModelName", "ai_model_name"), "unknown")
		model := models[modelName]
		if model == nil {
			model = map[string]any{"model": modelName, "requests": 0, "costUSD": "0", "totalTokens": 0}
			models[modelName] = model
		}
		model["requests"] = model["requests"].(int) + 1
		modelCost, _ := numericValue(model["costUSD"].(string))
		model["costUSD"] = decimalString(modelCost + itemCost/clinePassUsageUnitsPerUSD)
		model["totalTokens"] = model["totalTokens"].(int) + int(itemTotal)
	}
	modelList := make([]map[string]any, 0, len(models))
	for _, model := range models {
		modelList = append(modelList, model)
	}
	sort.Slice(modelList, func(i, j int) bool {
		left, _ := modelList[i]["requests"].(int)
		right, _ := modelList[j]["requests"].(int)
		if left != right {
			return left > right
		}
		return fmt.Sprint(modelList[i]["model"]) < fmt.Sprint(modelList[j]["model"])
	})
	summary["count"] = count
	summary["costUSD"] = decimalString(cost / clinePassUsageUnitsPerUSD)
	summary["creditsUsed"] = decimalString(credits / clinePassBalanceUnitsPerCredit)
	summary["promptTokens"] = int(promptTokens)
	summary["completionTokens"] = int(completionTokens)
	summary["totalTokens"] = int(totalTokens)
	summary["models"] = modelList
	return summary
}
