package main

import (
	"testing"
	"time"
)

const clinePassMeFixture = `{
  "success": true,
  "data": {
    "id": "user-test",
    "displayName": "Tester",
    "organizations": []
  }
}`

const clinePassBalanceFixture = `{
  "success": true,
  "data": {
    "balance": 316027
  }
}`

const clinePassPlanFixture = `{
  "success": true,
  "data": {
    "currentPeriodStart": "2026-09-22T01:09:30Z",
    "currentPeriodEnd": "2026-10-22T01:09:30Z",
    "plan": {
      "displayName": "Cline Pass (Monthly)",
      "interval": "Monthly",
      "pricePerSeatCents": 999,
      "isActive": true,
      "entitlements": {
        "cline_pass": {
          "enabled": true,
          "inferenceCapThreshold": {
            "last5HoursUsageCostUSDPerUser": 1000000000,
            "last7daysUsageCostUSDPerUser": 2500000000,
            "last30daysUsageCostUSDPerUser": 5000000000
          }
        }
      }
    }
  }
}`

func TestParseClinePassSnapshotDerivesWindowsAndBalance(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	items := []map[string]any{
		{"createdAt": now.Add(-time.Hour).Format(time.RFC3339Nano), "costUsd": float64(100000000), "creditsUsed": float64(0), "promptTokens": float64(100), "completionTokens": float64(20), "totalTokens": float64(120), "aiModelName": "cline-pass/deepseek-v4.1-flash"},
		{"createdAt": now.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano), "costUsd": float64(200000000), "creditsUsed": float64(0), "promptTokens": float64(200), "completionTokens": float64(30), "totalTokens": float64(230), "aiModelName": "cline-pass/glm-5.3"},
		{"createdAt": now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano), "costUsd": float64(300000000), "creditsUsed": float64(0), "promptTokens": float64(300), "completionTokens": float64(40), "totalTokens": float64(340), "aiModelName": "cline-pass/kimi-k3"},
	}
	snapshot, err := parseClinePassSnapshot(credentialCandidate{AuthIndex: "cline", Name: "Cline Pass", Provider: "cline pass", BaseURL: clinePassAPIBase}, []byte(clinePassMeFixture), []byte(clinePassBalanceFixture), []byte(clinePassPlanFixture), items, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AdapterID != "clinepass-usage" || snapshot.Kind != kindQuota || snapshot.Status != statusOK {
		t.Fatalf("snapshot identity/status = %+v", snapshot)
	}
	if len(snapshot.Balances) != 1 || snapshot.Balances[0].Amount != "0.316027" || snapshot.Balances[0].Currency != "USD" {
		t.Fatalf("balances = %+v", snapshot.Balances)
	}
	if len(snapshot.Quantities) != 1 || snapshot.Quantities[0].Remaining != "31.6027" || snapshot.Quantities[0].Display != "remaining" {
		t.Fatalf("quantities = %+v", snapshot.Quantities)
	}
	if len(snapshot.Windows) != 3 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	want := map[string][3]string{
		"fiveHour": {"9", "10", "1"},
		"weekly":   {"24", "25", "1"},
		"month":    {"47", "50", "3"},
	}
	for _, window := range snapshot.Windows {
		values, ok := want[window.Name]
		if !ok || window.RemainingAmount != values[0] || window.TotalAmount != values[1] || window.UsedAmount != values[2] || window.Source != "derived" {
			t.Fatalf("unexpected window: %+v", window)
		}
	}
	plan, ok := snapshot.Details["plan"].(map[string]any)
	if !ok || plan["name"] != "Cline Pass (Monthly)" {
		t.Fatalf("plan details = %+v", snapshot.Details["plan"])
	}
	usage, ok := snapshot.Details["usage"].(map[string]any)
	if !ok || usage["count"] != 2 || usage["costUSD"] != "3" {
		t.Fatalf("usage details = %+v", usage)
	}
}

func TestAdapterDetectsClinePass(t *testing.T) {
	adapter, normalized, ok := adapterFor(credentialCandidate{
		Provider: "cline pass",
		BaseURL:  "https://api.cline.bot/api/v1",
	})
	if !ok || adapter != "clinepass-usage" {
		t.Fatalf("adapter = %q, ok=%v", adapter, ok)
	}
	if normalized.BaseURL != "https://api.cline.bot/api/v1" {
		t.Fatalf("base URL = %q", normalized.BaseURL)
	}
}

func TestClinePassEndpointUsesAPIv1Root(t *testing.T) {
	endpoint, err := fixedEndpointWithOptions("https://api.cline.bot/api/v1", clinePassAPIBase, "/api/v1/users/me", endpointOptions{StripInferenceSuffix: true})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://api.cline.bot/api/v1/users/me" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}
