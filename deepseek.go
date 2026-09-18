package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type deepSeekBalanceResponse struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency     string `json:"currency"`
		TotalBalance string `json:"total_balance"`
		Granted      string `json:"granted_balance"`
		ToppedUp     string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

func matchesDeepSeek(candidate credentialCandidate) bool {
	provider := strings.ToLower(strings.TrimSpace(candidate.Provider))
	if provider == "deepseek" || provider == "deepseek-apikey" {
		return true
	}
	if candidate.BaseURL == "" {
		return false
	}
	u, err := url.Parse(candidate.BaseURL)
	return err == nil && strings.EqualFold(u.Hostname(), "api.deepseek.com")
}

func parseDeepSeekSnapshot(candidate credentialCandidate, raw []byte, now time.Time) (accountSnapshot, error) {
	var payload deepSeekBalanceResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return accountSnapshot{}, fmt.Errorf("decode DeepSeek balance response: %w", err)
	}
	if len(payload.BalanceInfos) == 0 {
		return accountSnapshot{}, fmt.Errorf("DeepSeek response contains no balance_infos")
	}

	snapshot := accountSnapshot{
		AccountID:    candidate.AuthIndex,
		AccountName:  candidate.Name,
		Provider:     candidate.Provider,
		AdapterID:    "deepseek-balance",
		BaseURL:      candidate.BaseURL,
		Kind:         kindBalance,
		Status:       statusOK,
		Capabilities: []string{"balance"},
		CheckedAt:    now,
	}
	if !payload.IsAvailable {
		snapshot.Status = statusWarning
		snapshot.Error = &snapshotError{Code: "UNAVAILABLE", Message: "DeepSeek reports this account is unavailable"}
	}
	for _, balance := range payload.BalanceInfos {
		if strings.TrimSpace(balance.Currency) == "" || strings.TrimSpace(balance.TotalBalance) == "" {
			continue
		}
		snapshot.Balances = append(snapshot.Balances,
			moneyBalance{Amount: balance.TotalBalance, Currency: balance.Currency, BalanceType: "total"},
			moneyBalance{Amount: balance.Granted, Currency: balance.Currency, BalanceType: "granted"},
			moneyBalance{Amount: balance.ToppedUp, Currency: balance.Currency, BalanceType: "topped_up"},
		)
	}
	if len(snapshot.Balances) == 0 {
		return accountSnapshot{}, fmt.Errorf("DeepSeek response contains no usable balances")
	}
	return snapshot, nil
}

func deepSeekURL(baseURL string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = "https://api.deepseek.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "api.deepseek.com") {
		return "", fmt.Errorf("DeepSeek adapter only permits https://api.deepseek.com")
	}
	u.Path = "/user/balance"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func accountSnapshotWithSuccess(previous accountSnapshot, current accountSnapshot, now time.Time, latency time.Duration) accountSnapshot {
	if current.Kind == "" {
		current.Kind = inferAccountKind(current)
	}
	current.LastAttemptAt = now
	current.LastSuccessAt = &now
	current.LatencyMs = latency.Milliseconds()
	current.Stale = false
	for name, section := range current.Sections {
		if section.Status == "available" && section.UpdatedAt == nil {
			updatedAt := now
			section.UpdatedAt = &updatedAt
			current.Sections[name] = section
		}
	}
	return current
}

func inferAccountKind(snapshot accountSnapshot) accountKind {
	if len(snapshot.Balances) > 0 {
		return kindBalance
	}
	if len(snapshot.Windows) > 0 {
		return kindPeriodQuota
	}
	if len(snapshot.Quantities) > 0 {
		return kindQuota
	}
	return kindUnsupported
}
