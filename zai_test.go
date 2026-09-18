package main

import (
	"testing"
	"time"
)

func TestParseZaiUsageLimits(t *testing.T) {
	snapshot, err := parseZaiSnapshot(credentialCandidate{
		AuthIndex: "zai-1",
		Name:      "智谱",
		Provider:  "智谱",
		BaseURL:   "https://open.bigmodel.cn/api/paas/v4",
	}, []byte(`{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","usage":100000,"currentValue":18000,"remaining":82000,"percentage":18,"nextResetTime":"2026-09-14T00:00:00+08:00"},{"type":"TOKENS_LIMIT","usage":500000,"currentValue":180000,"remaining":320000,"percentage":36,"nextResetTime":"2026-09-20T00:00:00+08:00"},{"type":"TIME_LIMIT","usage":100,"currentValue":5,"remaining":95,"percentage":5,"nextResetTime":1789344000000}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindPeriodQuota || len(snapshot.Windows) != 3 {
		t.Fatalf("unexpected Z.ai snapshot: %+v", snapshot)
	}
	if snapshot.Windows[0].Name != "5h" || snapshot.Windows[0].Unit != "tokens" {
		t.Fatalf("unexpected token window: %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].Name != "7d" || snapshot.Windows[2].Unit != "requests" {
		t.Fatalf("unexpected window names or units: %+v", snapshot.Windows)
	}
	if snapshot.Windows[0].RemainingFraction == nil || absFloat(*snapshot.Windows[0].RemainingFraction-0.82) > 0.000001 {
		t.Fatalf("remaining fraction = %+v", snapshot.Windows[0].RemainingFraction)
	}
	if snapshot.Windows[0].RemainingAmount != "82000" || snapshot.Windows[0].TotalAmount != "100000" {
		t.Fatalf("amount fields were not preserved: %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[0].ResetAt == nil || snapshot.Windows[0].ResetAt.Year() != 2026 {
		t.Fatalf("reset time was not parsed: %+v", snapshot.Windows[0].ResetAt)
	}
	if snapshot.Windows[2].ResetAt == nil || snapshot.Windows[2].ResetAt.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("numeric reset time was not parsed: %+v", snapshot.Windows[2].ResetAt)
	}
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func TestParseZaiUnsupportedResponse(t *testing.T) {
	snapshot, err := parseZaiSnapshot(credentialCandidate{AuthIndex: "zai-1", Name: "智谱"}, []byte(`{"success":false,"code":"500","msg":"not available"}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != kindUnsupported || snapshot.Status != statusUnknown || snapshot.Error == nil {
		t.Fatalf("unexpected unsupported snapshot: %+v", snapshot)
	}
}
