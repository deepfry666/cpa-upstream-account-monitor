package main

import (
	"testing"
	"time"
)

func TestParseDeepSeekSnapshot(t *testing.T) {
	snapshot, err := parseDeepSeekSnapshot(credentialCandidate{
		AuthIndex: "auth-1",
		Name:      "DeepSeek",
		Provider:  "deepseek",
	}, []byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"37.82","granted_balance":"5.00","topped_up_balance":"32.82"}]}`), time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != statusOK || len(snapshot.Balances) != 3 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Balances[0].Amount != "37.82" || snapshot.Balances[0].Currency != "CNY" {
		t.Fatalf("unexpected total balance: %+v", snapshot.Balances[0])
	}
}

func TestDeepSeekURLRejectsCustomHost(t *testing.T) {
	if _, err := deepSeekURL("https://example.com"); err == nil {
		t.Fatal("expected custom host to be rejected")
	}
}

func TestTokenFromCredential(t *testing.T) {
	got := tokenFromCredential([]byte(`{"api_key":"from-json"}`), map[string]string{})
	if got != "from-json" {
		t.Fatalf("token = %q", got)
	}
	got = tokenFromCredential(nil, map[string]string{"api_key": "from-attrs"})
	if got != "from-attrs" {
		t.Fatalf("token = %q", got)
	}
}
