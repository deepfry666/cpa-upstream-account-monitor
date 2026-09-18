package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const commandCodeAPIBase = "https://api.commandcode.ai"
const commandCodeCLIVersion = "1.56.0"

var commandCodePlanMonthlyCredits = map[string]float64{
	"individual-go":       10,
	"individual-goat":     70,
	"individual-pro":      30,
	"individual-pro-v1":   80,
	"individual-provider": 15,
	"individual-max":      150,
	"individual-ultra":    300,
	"teams-pro":           40,
}

var commandCodePlanNames = map[string]string{
	"individual-go":       "GO",
	"individual-goat":     "GOAT",
	"individual-pro":      "PRO",
	"individual-pro-v1":   "PRO V1",
	"individual-provider": "PROVIDER",
	"individual-max":      "MAX",
	"individual-ultra":    "ULTRA",
	"teams-pro":           "TEAMS PRO",
}

func queryCommandCodeSnapshot(callbackID string, candidate credentialCandidate, token string) (accountSnapshot, error) {
	candidate = normalizeCommandCodeCandidate(candidate)
	whoamiEndpoint := commandCodeEndpoint(candidate.BaseURL, "/alpha/whoami", url.Values{"limits": {"1"}})
	whoamiResponse, err := queryCommandCodeEndpoint(callbackID, whoamiEndpoint, token, candidate.ProxyURL)
	if err != nil {
		return accountSnapshot{}, fmt.Errorf("查询 Command Code 账户信息失败: %w", err)
	}
	if whoamiResponse.StatusCode < 200 || whoamiResponse.StatusCode >= 300 {
		return accountSnapshot{}, fmt.Errorf("Command Code 账户接口返回 HTTP %d", whoamiResponse.StatusCode)
	}
	orgID, _, _, _, err := parseCommandCodeIdentity(whoamiResponse.Body)
	if err != nil {
		return accountSnapshot{}, err
	}

	query := url.Values{}
	if strings.TrimSpace(orgID) != "" {
		query.Set("orgId", orgID)
	}

	var wg sync.WaitGroup
	var creditsResponse, subscriptionResponse, usageResponse hostHTTPResponse
	var creditsErr, subscriptionErr, usageErr error
	wg.Add(3)
	go func() {
		defer wg.Done()
		creditsResponse, creditsErr = queryCommandCodeEndpoint(callbackID, commandCodeEndpoint(candidate.BaseURL, "/alpha/billing/credits", query), token, candidate.ProxyURL)
	}()
	go func() {
		defer wg.Done()
		subscriptionResponse, subscriptionErr = queryCommandCodeEndpoint(callbackID, commandCodeEndpoint(candidate.BaseURL, "/alpha/billing/subscriptions", query), token, candidate.ProxyURL)
	}()
	go func() {
		defer wg.Done()
		usageResponse, usageErr = queryCommandCodeEndpoint(callbackID, commandCodeEndpoint(candidate.BaseURL, "/alpha/usage/summary", query), token, candidate.ProxyURL)
	}()
	wg.Wait()

	if creditsErr != nil {
		return accountSnapshot{}, fmt.Errorf("查询 Command Code Credits 失败: %w", creditsErr)
	}
	if creditsResponse.StatusCode < 200 || creditsResponse.StatusCode >= 300 {
		return accountSnapshot{}, fmt.Errorf("Command Code Credits 接口返回 HTTP %d", creditsResponse.StatusCode)
	}
	var subscriptionBody, usageBody []byte
	if subscriptionErr == nil && subscriptionResponse.StatusCode >= 200 && subscriptionResponse.StatusCode < 300 {
		subscriptionBody = subscriptionResponse.Body
	}
	if usageErr == nil && usageResponse.StatusCode >= 200 && usageResponse.StatusCode < 300 {
		usageBody = usageResponse.Body
	}

	snapshot, err := parseCommandCodeSnapshot(candidate, whoamiResponse.Body, creditsResponse.Body, subscriptionBody, usageBody)
	if err != nil {
		return accountSnapshot{}, err
	}
	if subscriptionErr != nil || subscriptionResponse.StatusCode < 200 || subscriptionResponse.StatusCode >= 300 {
		snapshot.Warnings = append(snapshot.Warnings, "套餐信息暂时不可用；Credits 余额仍按已获取数据展示。")
	}
	if usageErr != nil || usageResponse.StatusCode < 200 || usageResponse.StatusCode >= 300 {
		snapshot.Warnings = append(snapshot.Warnings, "本周期用量统计暂时不可用；余额与窗口额度仍正常展示。")
	}
	return snapshot, nil
}

func parseCommandCodeSnapshot(candidate credentialCandidate, whoamiBody, creditsBody, subscriptionBody, usageBody []byte) (accountSnapshot, error) {
	candidate = normalizeCommandCodeCandidate(candidate)
	orgID, org, user, orgLimits, err := parseCommandCodeIdentity(whoamiBody)
	if err != nil {
		return accountSnapshot{}, err
	}

	credits, err := parseCommandCodeCredits(creditsBody)
	if err != nil {
		return accountSnapshot{}, err
	}
	monthlyCredits, monthlyOK := commandCodeNumber(credits, "monthlyCredits")
	purchasedCredits, purchasedOK := commandCodeNumber(credits, "purchasedCredits")
	freeCredits, freeOK := commandCodeNumber(credits, "freeCredits")
	if !monthlyOK && !purchasedOK && !freeOK {
		return accountSnapshot{}, fmt.Errorf("Command Code credits response contains no credit values")
	}
	if monthlyCredits < 0 || purchasedCredits < 0 || freeCredits < 0 {
		return accountSnapshot{}, fmt.Errorf("Command Code credits values cannot be negative")
	}

	subscription, subscriptionOK := parseCommandCodeSubscription(subscriptionBody)
	usage, usageOK := parseCommandCodeObject(usageBody)
	planID := firstNonEmpty(stringValue(credits, "planId"), stringValue(subscription, "planId"))
	planMonthlyCredits := commandCodePlanMonthlyCredits[strings.ToLower(planID)]
	monthlyGranted := monthlyCredits
	monthlyGrantedExplicit := false
	if granted, ok := commandCodeNumber(credits, "monthlyCreditsGranted"); ok && granted > 0 {
		monthlyGranted = granted
		monthlyGrantedExplicit = true
	}
	if planMonthlyCredits > monthlyGranted {
		monthlyGranted = planMonthlyCredits
	}
	if monthlyGranted < monthlyCredits {
		monthlyGranted = monthlyCredits
	}

	active := commandCodeSubscriptionActive(subscription, subscriptionOK)
	totalRemaining := monthlyCredits + purchasedCredits + freeCredits
	totalPool := totalRemaining
	if active {
		activePlanPool := maxFloat(planMonthlyCredits, monthlyCredits)
		totalPool = activePlanPool + purchasedCredits + freeCredits
	} else if usageOK {
		if totalCost, ok := commandCodeNumber(usage, "totalCost", "totalCredits"); ok && totalCost >= 0 {
			totalPool = totalCost + totalRemaining
		}
	}
	if totalPool < totalRemaining {
		totalPool = totalRemaining
	}
	usedCredits := maxFloat(totalPool-totalRemaining, 0)

	snapshot := accountSnapshot{
		AccountID:    candidate.AuthIndex,
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "commandcode-goat",
		BaseURL:      commandCodeAPIBase,
		Kind:         kindQuota,
		Status:       statusOK,
		Capabilities: []string{"credits", "subscription_usage", "quota_windows", "usage_statistics"},
		Quantities: []quotaQuantity{
			{
				Name:      "availableCredits",
				Scope:     "account",
				Source:    "combined",
				Remaining: commandCodeQuantityNumber(totalRemaining),
				Total:     commandCodeQuantityNumber(totalPool),
				Used:      commandCodeQuantityNumber(usedCredits),
				Unit:      "credits",
			},
			{
				Name:      "monthlyCredits",
				Scope:     "account",
				Source:    "subscription",
				Remaining: commandCodeQuantityNumber(monthlyCredits),
				Total:     commandCodeQuantityNumber(maxFloat(monthlyGranted, monthlyCredits)),
				Used:      commandCodeQuantityNumber(maxFloat(maxFloat(monthlyGranted, monthlyCredits)-monthlyCredits, 0)),
				Unit:      "credits",
			},
			{
				Name:      "purchasedCredits",
				Scope:     "account",
				Source:    "purchased",
				Remaining: commandCodeQuantityNumber(purchasedCredits),
				Total:     commandCodeQuantityNumber(purchasedCredits),
				Unit:      "credits",
			},
			{
				Name:      "freeCredits",
				Scope:     "account",
				Source:    "granted",
				Remaining: commandCodeQuantityNumber(freeCredits),
				Total:     commandCodeQuantityNumber(freeCredits),
				Unit:      "credits",
			},
		},
	}

	parsedWindows := map[string]*quotaWindow{}
	if windows := commandCodeWindowLimits(creditsBody, credits); windows != nil && boolValue(windows, "limited") {
		if boolValue(windows, "exceeded") {
			snapshot.Warnings = append(snapshot.Warnings, "Command Code 当前额度窗口已超出限制。")
		}
		for _, definition := range []struct {
			keys []string
			name string
		}{
			{[]string{"fiveHour", "five_hour", "fiveHourWindow"}, "fiveHour"},
			{[]string{"weekly", "week", "weeklyWindow"}, "weekly"},
			{[]string{"month", "monthly", "monthlyWindow"}, "month"},
		} {
			var windowObject map[string]any
			for _, key := range definition.keys {
				if windowObject = objectValue(windows, key); windowObject != nil {
					break
				}
			}
			if windowObject == nil {
				continue
			}
			window, err := commandCodeWindow(definition.name, windowObject)
			if err != nil {
				return accountSnapshot{}, err
			}
			parsedWindows[definition.name] = window
		}
	}
	if parsedWindows["month"] == nil && monthlyGranted > 0 && (active || monthlyGrantedExplicit) {
		parsedWindows["month"] = commandCodeMonthlyWindow(monthlyCredits, monthlyGranted, timeValue(subscription, "currentPeriodEnd", "periodEnd", "endsAt"))
	}
	for _, name := range []string{"fiveHour", "weekly", "month"} {
		if window := parsedWindows[name]; window != nil {
			snapshot.Windows = append(snapshot.Windows, *window)
		}
	}

	planName := commandCodePlanNames[strings.ToLower(planID)]
	if planName == "" {
		planName = strings.ToUpper(strings.TrimSpace(planID))
	}
	accountScope := "personal"
	if strings.TrimSpace(orgID) != "" {
		accountScope = "organization"
	}
	snapshot.Details = map[string]any{
		"plan": map[string]any{
			"id":                    planID,
			"name":                  planName,
			"monthlyCredits":        planMonthlyCredits,
			"monthlyCreditsGranted": monthlyGranted,
			"active":                active,
		},
		"credits": credits,
		"account": map[string]any{
			"scope":    accountScope,
			"orgId":    orgID,
			"orgLogin": stringValue(org, "login", "name", "slug"),
			"userName": stringValue(user, "userName", "name", "login", "email"),
		},
	}
	if subscriptionOK {
		snapshot.Details["subscription"] = subscription
	}
	if usageOK {
		snapshot.Details["usage"] = usage
	}
	if len(orgLimits) > 0 {
		snapshot.Details["org_limits"] = orgLimits
	}
	if boolValue(credits, "belowThreshold") {
		snapshot.Warnings = append(snapshot.Warnings, "Command Code Credits 余额低于账户设置的提醒阈值。")
	}
	if !subscriptionOK {
		snapshot.Warnings = append(snapshot.Warnings, "套餐信息不可用；已根据 Credits 与本周期费用估算可用池。")
	}
	if !usageOK {
		snapshot.Warnings = append(snapshot.Warnings, "本周期用量统计暂不可用。")
	}
	if accountScope == "organization" && len(orgLimits) == 0 {
		snapshot.Warnings = append(snapshot.Warnings, "组织限制信息暂不可用。")
	}
	return snapshot, nil
}

func queryCommandCodeEndpoint(callbackID, endpoint, token, proxyURL string) (hostHTTPResponse, error) {
	return queryEndpointWithHeaders(callbackID, endpoint, "Bearer "+token, proxyURL, map[string]string{
		"User-Agent":             "commandcode-quota/" + commandCodeCLIVersion,
		"x-command-code-version": commandCodeCLIVersion,
		"x-cli-environment":      "production",
	})
}

func normalizeCommandCodeCandidate(candidate credentialCandidate) credentialCandidate {
	candidate.BaseURL = commandCodeAPIBase
	return candidate
}

func parseCommandCodeIdentity(body []byte) (string, map[string]any, map[string]any, []map[string]any, error) {
	root, err := decodeObject(body)
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("decode Command Code account response: %w", err)
	}
	scope := root
	if data := objectValue(root, "data"); data != nil {
		scope = data
	}
	org := objectValue(scope, "org")
	user := objectValue(scope, "user")
	orgID := stringValue(org, "id", "orgId", "_id")
	if orgID == "" {
		orgID = stringValue(scope, "orgId", "organizationId")
	}
	orgLimits := objectArray(scope, "orgLimits")
	if len(orgLimits) == 0 {
		orgLimits = objectArray(root, "orgLimits")
	}
	return orgID, org, user, orgLimits, nil
}

func commandCodeOrgID(body []byte) (string, error) {
	orgID, _, _, _, err := parseCommandCodeIdentity(body)
	return orgID, err
}

func parseCommandCodeCredits(body []byte) (map[string]any, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode Command Code credits response: %w", err)
	}
	data := objectValue(root, "data")
	credits := objectValue(data, "credits")
	if credits == nil {
		credits = objectValue(root, "credits")
	}
	if credits == nil {
		return nil, fmt.Errorf("Command Code credits response contains no credits object")
	}
	return credits, nil
}

func commandCodeWindowLimits(body []byte, credits map[string]any) map[string]any {
	if windows := objectValue(credits, "windowLimits"); windows != nil {
		return windows
	}
	root, err := decodeObject(body)
	if err != nil {
		return nil
	}
	if windows := objectValue(root, "windowLimits"); windows != nil {
		return windows
	}
	if data := objectValue(root, "data"); data != nil {
		return objectValue(data, "windowLimits")
	}
	return nil
}

func parseCommandCodeObject(body []byte) (map[string]any, bool) {
	if len(body) == 0 {
		return nil, false
	}
	root, err := decodeObject(body)
	if err != nil {
		return nil, false
	}
	if data := objectValue(root, "data"); data != nil {
		return data, true
	}
	return root, true
}

func parseCommandCodeSubscription(body []byte) (map[string]any, bool) {
	root, ok := parseCommandCodeObject(body)
	if !ok || root == nil {
		return nil, false
	}
	if subscription := objectValue(root, "subscription"); subscription != nil {
		return subscription, true
	}
	if subscriptions := objectArray(root, "subscriptions"); len(subscriptions) > 0 {
		for _, subscription := range subscriptions {
			if commandCodeSubscriptionActive(subscription, true) {
				return subscription, true
			}
		}
		return subscriptions[0], true
	}
	if stringValue(root, "planId", "status", "currentPeriodStart") != "" {
		return root, true
	}
	return nil, false
}

func commandCodeSubscriptionActive(subscription map[string]any, ok bool) bool {
	if !ok || subscription == nil {
		return false
	}
	status := strings.ToLower(stringValue(subscription, "status"))
	switch status {
	case "active", "trialing", "trial", "current":
		return true
	case "inactive", "canceled", "cancelled", "expired", "past_due", "unpaid":
		return false
	}
	return stringValue(subscription, "planId") != ""
}

func commandCodeSubscriptionPeriodStart(body []byte) string {
	subscription, ok := parseCommandCodeSubscription(body)
	if !ok {
		return ""
	}
	return firstNonEmpty(stringValue(subscription, "currentPeriodStart", "periodStart", "startedAt"))
}

func commandCodeWindow(name string, object map[string]any) (*quotaWindow, error) {
	used, usedOK := commandCodeNumber(object, "used", "usage")
	cap, capOK := commandCodeNumber(object, "cap", "limit", "total")
	if !usedOK || !capOK || cap <= 0 {
		return nil, fmt.Errorf("Command Code %s window cap is missing or invalid", name)
	}
	if used < 0 {
		return nil, fmt.Errorf("Command Code %s window used value is invalid", name)
	}
	remaining := maxFloat(cap-used, 0)
	fraction := remaining / cap
	usedFraction := 1 - fraction
	return &quotaWindow{
		Name:              name,
		RemainingFraction: &fraction,
		UsedFraction:      &usedFraction,
		RemainingAmount:   commandCodeQuantityNumber(remaining),
		TotalAmount:       commandCodeQuantityNumber(cap),
		Unit:              "credits",
		ResetAt:           timeValue(object, "resetAt", "reset_at", "resetsAt"),
	}, nil
}

func commandCodeMonthlyWindow(remaining, total float64, resetAt *time.Time) *quotaWindow {
	if total <= 0 {
		return nil
	}
	remaining = maxFloat(remaining, 0)
	if remaining > total {
		total = remaining
	}
	fraction := remaining / total
	usedFraction := 1 - fraction
	return &quotaWindow{
		Name:              "month",
		RemainingFraction: &fraction,
		UsedFraction:      &usedFraction,
		RemainingAmount:   commandCodeQuantityNumber(remaining),
		TotalAmount:       commandCodeQuantityNumber(total),
		Unit:              "credits",
		ResetAt:           resetAt,
	}
}

func commandCodeEndpoint(baseURL, path string, query url.Values) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = commandCodeAPIBase
	}
	endpoint := base + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

func commandCodeNumber(object map[string]any, keys ...string) (float64, bool) {
	if object == nil {
		return 0, false
	}
	for _, key := range keys {
		value, exists := object[key]
		if !exists || value == nil {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return typed, true
		case json.Number:
			parsed, err := typed.Float64()
			return parsed, err == nil
		case string:
			return numericValue(typed)
		}
	}
	return 0, false
}

func commandCodeQuantityNumber(value float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(value, 'f', 6, 64), "0"), ".")
}

func maxFloat(left, right float64) float64 {
	if right > left {
		return right
	}
	return left
}
