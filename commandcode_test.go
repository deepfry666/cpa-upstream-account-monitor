package main

import (
	"strings"
	"testing"
)

const commandCodeWhoamiFixture = `{
  "org": {"id": "org_123", "login": "acme"},
  "user": {"userName": "tester"},
  "orgLimits": [
    {"scope": "org", "spent": 12.5, "limit": 50, "resetInterval": "month", "resetAt": "2026-10-01T00:00:00Z"}
  ]
}`

const commandCodeCreditsFixture = `{
  "data": {
    "credits": {
      "monthlyCredits": 55,
      "purchasedCredits": 30,
      "freeCredits": 10,
      "monthlyCreditsGranted": 70,
      "planId": "individual-goat",
      "windowLimits": {
        "limited": true,
        "fiveHour": {"used": 8, "cap": 20, "resetAt": "2026-09-18T08:00:00Z"},
        "weekly": {"used": 25, "cap": 70, "resetAt": "2026-09-22T00:00:00Z"}
      }
    }
  }
}`

const commandCodeSubscriptionFixture = `{
  "data": {
    "planId": "individual-goat",
    "status": "active",
    "currentPeriodStart": "2026-09-01T00:00:00Z",
    "currentPeriodEnd": "2026-10-01T00:00:00Z",
    "quantity": 1
  }
}`

const commandCodeUsageFixture = `{
  "totalCount": 120,
  "completedCount": 117,
  "failedCount": 3,
  "totalTokensIn": 210000,
  "totalTokensOut": 48000,
  "totalTokens": 258000,
  "totalCredits": 20,
  "totalCost": 20,
  "periodBasis": "subscription"
}`

const commandCodePersonalWhoamiFixture = `{
  "success": true,
  "user": {"id": "user_123", "name": "Tester", "userName": "tester", "email": "tester@example.com"}
}`

const commandCodePersonalCreditsFixture = `{
  "credits": {
    "belowThreshold": false,
    "creditThreshold": 0,
    "monthlyCredits": 67.4724902145,
    "purchasedCredits": 0,
    "freeCredits": 0
  },
  "windowLimits": {
    "limited": true,
    "exceeded": null,
    "fiveHour": {"used": 2.5275097855, "cap": 14, "exceeded": false, "resetAt": 1789715590180},
    "weekly": {"used": 2.5275097855, "cap": 35, "exceeded": false, "resetAt": 1790302390180}
  }
}`

const commandCodePersonalSubscriptionFixture = `{
  "success": true,
  "data": {
    "status": "active",
    "planId": "individual-goat",
    "currentPeriodStart": "2026-09-18T01:54:31.000Z",
    "currentPeriodEnd": "2026-10-18T01:54:31.000Z",
    "quantity": 1
  }
}`

const commandCodePersonalUsageFixture = `{
  "totalCount": 490,
  "totalCost": 2.4161524415,
  "averageCost": 0.00493092335,
  "successRate": 100,
  "completedCount": 490,
  "failedCount": 0,
  "totalCredits": 2.4161524415,
  "totalFreeCredits": 0,
  "totalMonthlyCredits": 2.4161524415,
  "totalPurchasedCredits": 0,
  "periodBasis": "billing-period"
}`

func TestParseCommandCodeGOATSnapshot(t *testing.T) {
	candidate := credentialCandidate{AuthIndex: "code-1", Name: "Command Code GOAT", Provider: "command-code-goat", BaseURL: "https://api.commandcode.ai"}
	snapshot, err := parseCommandCodeSnapshot(candidate, []byte(commandCodeWhoamiFixture), []byte(commandCodeCreditsFixture), []byte(commandCodeSubscriptionFixture), []byte(commandCodeUsageFixture))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindQuota || snapshot.AdapterID != "commandcode-goat" {
		t.Fatalf("unexpected snapshot kind/adapter: %+v", snapshot)
	}
	if len(snapshot.Quantities) != 4 {
		t.Fatalf("quantities = %d, want 4: %+v", len(snapshot.Quantities), snapshot.Quantities)
	}
	available := snapshot.Quantities[0]
	if available.Name != "availableCredits" || available.Remaining != "95" || available.Total != "110" || available.Used != "15" || available.Unit != "credits" {
		t.Fatalf("unexpected available credits: %+v", available)
	}
	if len(snapshot.Windows) != 3 {
		t.Fatalf("windows = %d, want 3: %+v", len(snapshot.Windows), snapshot.Windows)
	}
	if snapshot.Windows[0].Name != "fiveHour" || snapshot.Windows[0].RemainingAmount != "12" || snapshot.Windows[0].TotalAmount != "20" {
		t.Fatalf("unexpected five-hour window: %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[2].Name != "month" || snapshot.Windows[2].RemainingAmount != "55" || snapshot.Windows[2].TotalAmount != "70" {
		t.Fatalf("unexpected monthly window: %+v", snapshot.Windows[2])
	}
	if snapshot.Details["plan"] == nil || snapshot.Details["usage"] == nil || snapshot.Details["subscription"] == nil || snapshot.Details["org_limits"] == nil {
		t.Fatalf("missing structured details: %+v", snapshot.Details)
	}
	plan, ok := snapshot.Details["plan"].(map[string]any)
	if !ok || plan["name"] != "GOAT" || plan["monthlyCredits"] != float64(70) {
		t.Fatalf("unexpected plan details: %+v", plan)
	}
}

func TestParseCommandCodeProviderModeDoesNotFakeWindows(t *testing.T) {
	candidate := credentialCandidate{AuthIndex: "code-2", Name: "Command Code", Provider: "commandcode", BaseURL: "https://api.commandcode.ai"}
	credits := `{"data":{"credits":{"monthlyCredits":15,"purchasedCredits":0,"freeCredits":0,"planId":"individual-provider","windowLimits":{"limited":false,"fiveHour":{"used":1,"cap":5},"weekly":{"used":2,"cap":10}}}}}`
	snapshot, err := parseCommandCodeSnapshot(candidate, []byte(commandCodeWhoamiFixture), []byte(credits), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Windows) != 0 {
		t.Fatalf("provider mode generated fake windows: %+v", snapshot.Windows)
	}
	if len(snapshot.Warnings) == 0 {
		t.Fatal("missing subscription warning")
	}
}

func TestParseCommandCodeWithoutSubscriptionKeepsCredits(t *testing.T) {
	candidate := credentialCandidate{AuthIndex: "code-3", Provider: "command-code", BaseURL: "https://api.commandcode.ai"}
	credits := `{"data":{"credits":{"monthlyCredits":10,"purchasedCredits":5,"freeCredits":0,"planId":"individual-goat","windowLimits":{"limited":false}}}}`
	usage := `{"totalCredits":5,"totalCost":5,"totalCount":10}`
	snapshot, err := parseCommandCodeSnapshot(candidate, []byte(commandCodeWhoamiFixture), []byte(credits), nil, []byte(usage))
	if err != nil {
		t.Fatal(err)
	}
	available := snapshot.Quantities[0]
	if available.Remaining != "15" || available.Total != "20" || available.Used != "5" {
		t.Fatalf("unexpected inferred pool: %+v", available)
	}
	if len(snapshot.Warnings) == 0 || !strings.Contains(snapshot.Warnings[0], "套餐") {
		t.Fatalf("missing subscription warning: %+v", snapshot.Warnings)
	}
}

func TestParseCommandCodePersonalAccount(t *testing.T) {
	candidate := credentialCandidate{AuthIndex: "code-4", Provider: "commandcode", BaseURL: "https://api.commandcode.ai"}
	snapshot, err := parseCommandCodeSnapshot(candidate, []byte(commandCodePersonalWhoamiFixture), []byte(commandCodePersonalCreditsFixture), []byte(commandCodePersonalSubscriptionFixture), []byte(commandCodePersonalUsageFixture))
	if err != nil {
		t.Fatal(err)
	}
	account, ok := snapshot.Details["account"].(map[string]any)
	if !ok || account["scope"] != "personal" || account["orgId"] != "" {
		t.Fatalf("unexpected personal account details: %+v", account)
	}
	available := snapshot.Quantities[0]
	if available.Remaining != "67.47249" || available.Total != "70" || available.Used != "2.52751" {
		t.Fatalf("unexpected personal available credits: %+v", available)
	}
	monthly := snapshot.Quantities[1]
	if monthly.Remaining != "67.47249" || monthly.Total != "70" || monthly.Used != "2.52751" {
		t.Fatalf("unexpected personal monthly credits: %+v", monthly)
	}
	if len(snapshot.Windows) != 3 {
		t.Fatalf("personal account windows = %d, want 3: %+v", len(snapshot.Windows), snapshot.Windows)
	}
	if snapshot.Windows[0].Name != "fiveHour" || snapshot.Windows[0].RemainingAmount != "11.47249" || snapshot.Windows[0].TotalAmount != "14" {
		t.Fatalf("unexpected personal five-hour window: %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].Name != "weekly" || snapshot.Windows[1].RemainingAmount != "32.47249" || snapshot.Windows[1].TotalAmount != "35" {
		t.Fatalf("unexpected personal weekly window: %+v", snapshot.Windows[1])
	}
	if snapshot.Windows[2].Name != "month" || snapshot.Windows[2].RemainingAmount != "67.47249" || snapshot.Windows[2].TotalAmount != "70" {
		t.Fatalf("unexpected personal monthly window: %+v", snapshot.Windows[2])
	}
	for _, warning := range snapshot.Warnings {
		if strings.Contains(warning, "组织限制") {
			t.Fatalf("personal account should not warn about org limits: %v", snapshot.Warnings)
		}
	}
}

func TestParseCommandCodeRejectsEmptyCredits(t *testing.T) {
	candidate := credentialCandidate{AuthIndex: "code-5", Provider: "commandcode", BaseURL: "https://api.commandcode.ai"}
	_, err := parseCommandCodeSnapshot(candidate, []byte(commandCodeWhoamiFixture), []byte(`{"data":{"credits":{}}}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "credits") {
		t.Fatalf("error = %v, want empty credits error", err)
	}
}

func TestParseCommandCodeRejectsInvalidWindowCap(t *testing.T) {
	candidate := credentialCandidate{AuthIndex: "code-6", Provider: "commandcode", BaseURL: "https://api.commandcode.ai"}
	credits := `{"data":{"credits":{"monthlyCredits":10,"planId":"individual-goat","windowLimits":{"limited":true,"fiveHour":{"used":1,"cap":0}}}}}`
	_, err := parseCommandCodeSnapshot(candidate, []byte(commandCodeWhoamiFixture), []byte(credits), []byte(commandCodeSubscriptionFixture), nil)
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("error = %v, want invalid cap error", err)
	}
}

func TestAdapterDetectsCommandCode(t *testing.T) {
	tests := []credentialCandidate{
		{Provider: "commandcode-goat"},
		{Provider: "command-code", BaseURL: "https://api.commandcode.ai/provider/v1"},
		{Provider: "goat"},
		{Provider: "custom", BaseURL: "https://api.commandcode.ai/v1"},
	}
	for _, candidate := range tests {
		adapter, normalized, ok := adapterFor(candidate)
		if !ok || adapter != "commandcode-goat" {
			t.Fatalf("candidate %+v => adapter %q, ok %v", candidate, adapter, ok)
		}
		if normalized.BaseURL != "https://api.commandcode.ai" {
			t.Fatalf("candidate %+v was not normalized to Command Code API: %q", candidate, normalized.BaseURL)
		}
	}
}

func TestAdapterMigratesStaleOpenCodeOverride(t *testing.T) {
	previous := state.prefs
	prefs := newMonitorPrefs()
	prefs.data.Adapters["config:openai-compatibility:3:0"] = "opencode-go"
	state.prefs = prefs
	t.Cleanup(func() { state.prefs = previous })

	candidate := credentialCandidate{
		AuthIndex: "config:openai-compatibility:3:0",
		Name:      "command code goat",
		Provider:  "command code goat",
		BaseURL:   "https://api.commandcode.ai/provider/v1",
	}
	adapter, normalized, ok := adapterFor(candidate)
	if !ok || adapter != "commandcode-goat" {
		t.Fatalf("candidate %+v => adapter %q, ok %v", candidate, adapter, ok)
	}
	if normalized.BaseURL != commandCodeAPIBase {
		t.Fatalf("normalized base URL = %q, want %q", normalized.BaseURL, commandCodeAPIBase)
	}
}
