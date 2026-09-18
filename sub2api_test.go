package main

import "testing"

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
