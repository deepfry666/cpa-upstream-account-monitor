package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseSub2APIRateLimits(t *testing.T) {
	body := []byte(`{"mode":"quota_limited","isValid":true,"unit":"USD","rate_limits":[{"limit":500,"remaining":388.36261712,"used":111.63738288,"unit":"USD","reset_at":"2026-09-20T00:00:00+08:00","window":"7d"}]}`)
	root, decodeErr := decodeObject(body)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	limits := objectArray(root, "rate_limits")
	if len(limits) != 1 {
		t.Fatalf("limits = %#v", root)
	}
	window := quotaWindowFromLimit(limits[0], stringValue(limits[0], "window"))
	if window == nil {
		t.Fatalf("window did not parse: %#v", limits[0])
	}
	if window.Unit != "USD" {
		t.Fatalf("window unit = %q, want USD", window.Unit)
	}
	if window.ResetAt == nil || window.ResetAt.Year() != 2026 {
		t.Fatalf("window reset_at was not parsed: %+v", window)
	}
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "relay", Provider: "codex-api-key", BaseURL: "https://example.com"}, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Windows) != 1 || snapshot.Windows[0].RemainingFraction == nil || *snapshot.Windows[0].RemainingFraction < 0.77 || *snapshot.Windows[0].RemainingFraction > 0.78 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Kind != kindPeriodQuota || snapshot.Windows[0].RemainingAmount != "388.36261712" || snapshot.Windows[0].TotalAmount != "500" {
		t.Fatalf("unexpected period quota semantics: %+v", snapshot)
	}
}

func TestParseSub2APIBalanceIsNotPeriodPercent(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "wallet", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"total":46.76,"remaining":46.76,"unit":"USD"}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindBalance || len(snapshot.Balances) != 1 || len(snapshot.Windows) != 0 {
		t.Fatalf("balance was classified as quota: %+v", snapshot)
	}
}

func TestParseSub2APIAllowsRemainingOnlyBalance(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "wallet", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"remaining":46.76,"unit":"USD"}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindBalance || len(snapshot.Balances) != 1 || snapshot.Balances[0].Amount != "46.76" {
		t.Fatalf("remaining-only balance was not preserved: %+v", snapshot)
	}
	if len(snapshot.Quantities) != 1 || snapshot.Quantities[0].Remaining != "46.76" {
		t.Fatalf("remaining-only quantity was not preserved: %+v", snapshot.Quantities)
	}
}

func TestSub2APIUsageEndpointUsesSharedStatisticsParameters(t *testing.T) {
	endpoint, err := sub2APIUsageEndpoint(credentialCandidate{BaseURL: "https://relay.example/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://relay.example/v1/usage?days=30" {
		t.Fatalf("endpoint = %q, want shared days=30 usage endpoint", endpoint)
	}
}

func TestApplySub2APIBillingResponseMarksPartialFailure(t *testing.T) {
	snapshot := accountSnapshot{
		AccountID: "sub2api-account",
		Status:    statusOK,
		Balances:  []moneyBalance{{Amount: "46.76", Currency: "USD", BalanceType: "remaining"}},
	}
	snapshot = applySub2APIBillingResponse(snapshot, hostHTTPResponse{StatusCode: 503, Body: []byte(`{"error":"unavailable"}`)}, nil)
	if snapshot.Status != statusWarning || len(snapshot.Balances) != 1 {
		t.Fatalf("snapshot = %+v, want warning with balance preserved", snapshot)
	}
	section := snapshot.Sections["billing"]
	if section.Status != "error" || section.ErrorCode != "UPSTREAM_HTTP_503" {
		t.Fatalf("billing section = %+v", section)
	}
}

func TestParseSub2APIQuotaLimitedIsPeriodQuota(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"mode":"quota_limited","unit":"USD","quota":{"limit":100,"used":20,"remaining":80},"rate_limits":[{"window":"5h","limit":40,"used":10,"remaining":30,"reset_at":"2026-09-14T00:00:00+08:00"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindPeriodQuota || len(snapshot.Balances) != 0 || len(snapshot.Windows) != 2 {
		t.Fatalf("quota_limited was not treated as period quota: %+v", snapshot)
	}
	if snapshot.Windows[0].Name != "total" || snapshot.Windows[0].RemainingAmount != "80" {
		t.Fatalf("unexpected total window: %+v", snapshot.Windows[0])
	}
}

func TestParseSub2APISeparatesExpiryFromWindowReset(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"mode":"quota_limited","unit":"USD","expires_at":"2026-12-31T00:00:00Z","quota":{"limit":100,"used":20,"remaining":80,"reset_at":"2026-09-26T00:00:00Z"},"rate_limits":[{"window":"5h","limit":40,"used":10,"remaining":30}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Windows) != 2 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	total := snapshot.Windows[0]
	if total.ExpiresAt == nil || total.ExpiresAt.Month() != time.December {
		t.Fatalf("total expiry = %+v, want December", total.ExpiresAt)
	}
	if total.ResetAt == nil || total.ResetAt.Month() != time.September {
		t.Fatalf("total reset = %+v, want September", total.ResetAt)
	}
	if snapshot.Windows[1].ResetAt != nil {
		t.Fatalf("missing reset_at was copied into window: %+v", snapshot.Windows[1])
	}
}

func TestParseNewAPIExpiryDoesNotBecomeReset(t *testing.T) {
	snapshot, err := parseNewAPISnapshot(credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, []byte(`{"data":{"total_granted":100,"total_used":40,"total_available":60,"expires_at":"2026-12-31T00:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	quantity := snapshot.Quantities[0]
	if quantity.ExpiresAt == nil || quantity.ExpiresAt.Month() != time.December {
		t.Fatalf("expiry = %+v, want December", quantity.ExpiresAt)
	}
	if quantity.ResetAt != nil {
		t.Fatalf("expiry was copied into reset_at: %+v", quantity.ResetAt)
	}
}

func TestParseSub2APISubscriptionWindows(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"mode":"unrestricted","subscription":{"daily_usage_usd":2.5,"daily_limit_usd":10,"weekly_usage_usd":12,"weekly_limit_usd":50,"monthly_usage_usd":30,"monthly_limit_usd":100}}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindPeriodQuota || len(snapshot.Windows) != 3 {
		t.Fatalf("subscription was not parsed as period quota: %+v", snapshot)
	}
	if snapshot.Windows[0].RemainingAmount != "7.5" || snapshot.Windows[0].TotalAmount != "10" {
		t.Fatalf("unexpected daily subscription window: %+v", snapshot.Windows[0])
	}
}

func TestParseSub2APISubscriptionUSDFieldsIgnoreTopLevelCurrency(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"mode":"unrestricted","unit":"CNY","subscription":{"daily_usage_usd":2.5,"daily_limit_usd":10}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Windows) != 1 || snapshot.Windows[0].Unit != "USD" {
		t.Fatalf("subscription _usd windows = %+v, want fixed USD unit", snapshot.Windows)
	}
}

func TestParseSub2APISubscriptionKeepsDailyOverageDespiteHealthyWeekly(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"mode":"unrestricted","subscription":{"daily_usage_usd":12,"daily_limit_usd":10,"weekly_usage_usd":12,"weekly_limit_usd":100}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Windows) != 2 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	daily := snapshot.Windows[0]
	if daily.RemainingAmount != "0" || daily.UsedAmount != "12" || daily.ExcessAmount != "2" {
		t.Fatalf("daily overage = %+v", daily)
	}
	state.mu.Lock()
	previous := state.cfg.Thresholds
	state.cfg.Thresholds = thresholdConfig{WarningPercent: 20, CriticalPercent: 10}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.Thresholds = previous
		state.mu.Unlock()
	})
	if got := applyThresholds(snapshot).Status; got != statusCritical {
		t.Fatalf("status = %q, want critical", got)
	}
}

func TestParseSub2APIExpiredKeyOverridesValidFlagAndKeepsQuota(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"mode":"quota_limited","isValid":true,"status":"expired","expires_at":"2020-01-01T00:00:00Z","unit":"USD","quota":{"limit":100,"used":10,"remaining":90}}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != statusDisabled {
		t.Fatalf("status = %q, want disabled", snapshot.Status)
	}
	if snapshot.Error == nil || snapshot.Error.Code != "KEY_EXPIRED" {
		t.Fatalf("error = %+v, want KEY_EXPIRED", snapshot.Error)
	}
	if len(snapshot.Windows) != 1 || snapshot.Windows[0].RemainingAmount != "90" {
		t.Fatalf("quota was discarded: %+v", snapshot.Windows)
	}
}

func TestParseSub2APIExpiredTimestampOverridesValidFlag(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"isValid":true,"expires_at":"2020-01-01T00:00:00Z","total":100,"remaining":90,"unit":"USD"}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != statusDisabled || snapshot.Error == nil || snapshot.Error.Code != "KEY_EXPIRED" {
		t.Fatalf("snapshot = %+v, want expired disabled snapshot", snapshot)
	}
	if len(snapshot.Quantities) != 1 || snapshot.Quantities[0].Remaining != "90" {
		t.Fatalf("quota was discarded: %+v", snapshot.Quantities)
	}
}

func TestParseSub2APIInvalidKeyIsNotHealthy(t *testing.T) {
	snapshot, err := parseSub2APISnapshot(credentialCandidate{AuthIndex: "a", Name: "panel", Provider: "sub2api", BaseURL: "https://example.com"}, []byte(`{"isValid":false,"status":"active","total":100,"remaining":90,"unit":"USD"}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != statusDisabled || snapshot.Error == nil || snapshot.Error.Code != "KEY_INVALID" {
		t.Fatalf("snapshot = %+v, want invalid disabled snapshot", snapshot)
	}
}

func TestParseNewAPIQuotaDoesNotCreateFakeWindow(t *testing.T) {
	snapshot, err := parseNewAPISnapshot(credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, []byte(`{"data":{"total_granted":100,"total_used":40,"total_available":60,"unit":"quota","expires_at":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindQuota || len(snapshot.Quantities) != 1 || len(snapshot.Windows) != 0 {
		t.Fatalf("account quota was represented as a period window: %+v", snapshot)
	}
	if snapshot.Quantities[0].ResetAt != nil {
		t.Fatalf("zero reset time should be omitted: %+v", snapshot.Quantities[0])
	}
}

func TestNewAPIAmountUsesInstanceDisplaySettings(t *testing.T) {
	status := newAPIQuotaStatus{QuotaPerUnit: 500000, DisplayType: "CNY", USDExchangeRate: 1}
	if got := newAPIAmount("277857662", status); got != "555.715324" {
		t.Fatalf("converted remaining = %q, want 555.715324", got)
	}
	if got := newAPIAmount("500000000", status); got != "1000" {
		t.Fatalf("converted total = %q, want 1000", got)
	}
}

func TestParseNewAPIQuotaUsesDisplayCurrencyRules(t *testing.T) {
	body := []byte(`{"data":{"total_granted":1000000,"total_used":0,"total_available":1000000}}`)
	for _, test := range []struct {
		name   string
		status newAPIQuotaStatus
		want   string
		unit   string
	}{
		{name: "USD", status: newAPIQuotaStatus{QuotaPerUnit: 500000, DisplayType: "USD", USDExchangeRate: 6.73}, want: "2", unit: "USD"},
		{name: "CNY", status: newAPIQuotaStatus{QuotaPerUnit: 500000, DisplayType: "CNY", USDExchangeRate: 6.73}, want: "13.46", unit: "CNY"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := parseNewAPISnapshotWithQuotaStatus("", credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, body, test.status)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Quantities) != 1 {
				t.Fatalf("quantities = %+v", snapshot.Quantities)
			}
			quantity := snapshot.Quantities[0]
			if quantity.Remaining != test.want || quantity.Total != test.want || quantity.Used != "0" || quantity.Unit != test.unit {
				t.Fatalf("quantity = %+v, want remaining/total %s %s", quantity, test.want, test.unit)
			}
		})
	}
}

func TestParseNewAPIQuotaShowsTokensWithoutCurrencyConversion(t *testing.T) {
	body := []byte(`{"data":{"total_granted":1000000,"total_used":250000,"total_available":750000}}`)
	status := newAPIQuotaStatus{QuotaPerUnit: 500000, DisplayType: "TOKENS"}
	snapshot, err := parseNewAPISnapshotWithQuotaStatus("", credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, body, status)
	if err != nil {
		t.Fatal(err)
	}
	quantity := snapshot.Quantities[0]
	if quantity.Remaining != "750000" || quantity.Total != "1000000" || quantity.Used != "250000" || quantity.Unit != "tokens" {
		t.Fatalf("quantity = %+v, want raw token counters", quantity)
	}
}

func TestParseNewAPIQuotaUsesCustomCurrencySettings(t *testing.T) {
	body := []byte(`{"data":{"total_granted":1000000,"total_used":0,"total_available":1000000}}`)
	status := newAPIQuotaStatus{QuotaPerUnit: 500000, DisplayType: "CUSTOM", CustomCurrencySymbol: "EUR", CustomCurrencyExchangeRate: 0.92}
	snapshot, err := parseNewAPISnapshotWithQuotaStatus("", credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, body, status)
	if err != nil {
		t.Fatal(err)
	}
	quantity := snapshot.Quantities[0]
	if quantity.Remaining != "1.84" || quantity.Total != "1.84" || quantity.Unit != "EUR" {
		t.Fatalf("quantity = %+v, want custom currency conversion", quantity)
	}
}

func TestParseNewAPIQuotaPreservesRawValueWhenDisplaySettingsAreIncomplete(t *testing.T) {
	body := []byte(`{"data":{"total_granted":1000000,"total_used":0,"total_available":1000000}}`)
	for _, status := range []newAPIQuotaStatus{
		{QuotaPerUnit: 500000, DisplayType: "CUSTOM", CustomCurrencySymbol: "EUR"},
		{QuotaPerUnit: 500000, DisplayType: "UNKNOWN"},
		{DisplayType: "USD"},
	} {
		snapshot, err := parseNewAPISnapshotWithQuotaStatus("", credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, body, status)
		if err != nil {
			t.Fatal(err)
		}
		quantity := snapshot.Quantities[0]
		if quantity.Remaining != "1000000" || quantity.Total != "1000000" {
			t.Fatalf("status %+v quantity = %+v, want raw quota preserved", status, quantity)
		}
		if len(snapshot.Warnings) == 0 || !strings.Contains(strings.Join(snapshot.Warnings, " "), "未换算") {
			t.Fatalf("status %+v warnings = %v, want unconverted warning", status, snapshot.Warnings)
		}
	}
}

func TestNewAPIAmountHandlesZeroDecimalAndNegativeQuota(t *testing.T) {
	status := newAPIQuotaStatus{QuotaPerUnit: 500000, DisplayType: "USD"}
	for raw, want := range map[string]string{
		"0":        "0",
		"3":        "0.000006",
		"-1000000": "-2",
	} {
		if got := newAPIAmount(raw, status); got != want {
			t.Fatalf("newAPIAmount(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseNewAPIQuotaStatusSupportsCustomAndKeepsUnknownTypes(t *testing.T) {
	status, err := parseNewAPIQuotaStatus([]byte(`{"data":{"quota_per_unit":500000,"quota_display_type":"CUSTOM","custom_currency_symbol":"EUR","custom_currency_exchange_rate":0.92}}`))
	if err != nil {
		t.Fatal(err)
	}
	if status.DisplayType != "CUSTOM" || status.CustomCurrencySymbol != "EUR" || status.CustomCurrencyExchangeRate != 0.92 {
		t.Fatalf("custom status = %+v", status)
	}

	status, err = parseNewAPIQuotaStatus([]byte(`{"data":{"quota_per_unit":500000,"quota_display_type":"CREDITS"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if status.DisplayType != "CREDITS" || status.Fallback {
		t.Fatalf("unknown status = %+v, want type preserved without fallback", status)
	}
}

func TestParseNewAPIQuotaMarksTokenScope(t *testing.T) {
	snapshot, err := parseNewAPISnapshot(credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, []byte(`{"data":{"total_granted":100,"total_used":40,"total_available":60,"unit":"quota"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Quantities) != 1 || snapshot.Quantities[0].Scope != "token" || snapshot.Quantities[0].Source != "/api/usage/token/" {
		t.Fatalf("unexpected NewAPI scope: %+v", snapshot.Quantities)
	}
}

func TestParseNewAPIUnlimitedQuotaDoesNotCreateZeroAlertValue(t *testing.T) {
	snapshot, err := parseNewAPISnapshot(credentialCandidate{AuthIndex: "a", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"}, []byte(`{"data":{"total_granted":222142338,"total_used":222142338,"total_available":0,"unlimited_quota":true,"expires_at":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	quantity := snapshot.Quantities[0]
	if !quantity.Unlimited || quantity.Remaining != "" || quantity.Total != "" || quantity.Used != "" {
		t.Fatalf("unlimited quota retained misleading finite values: %+v", quantity)
	}
	if got := applyThresholds(snapshot).Status; got != statusOK {
		t.Fatalf("unlimited quota status = %q, want ok", got)
	}
}
