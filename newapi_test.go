package main

import (
	"errors"
	"testing"
)

func TestNewAPISnapshotKeepsAccountQuotaWhenKeyQueryFails(t *testing.T) {
	account := &quotaQuantity{
		Name:      "NewAPI 账户总额",
		Scope:     "account",
		Source:    "/api/user/self",
		Remaining: "80",
		Total:     "100",
		Used:      "20",
		Unit:      "USD",
	}
	snapshot, err := newAPISnapshotFromParts(
		credentialCandidate{AuthIndex: "acct", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"},
		newAPIQuotaStatus{QuotaPerUnit: 1, DisplayType: "USD"},
		nil,
		errors.New("upstream returned HTTP 401"),
		account,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Quantities) != 1 || snapshot.Quantities[0].Scope != "account" {
		t.Fatalf("quantities = %+v, want valid account quota", snapshot.Quantities)
	}
	if section := snapshot.Sections["key_quota"]; section.Status != "error" || section.ErrorCode == "" {
		t.Fatalf("key quota section = %+v, want explicit error", section)
	}
}

func TestNewAPISnapshotKeepsKeyQuotaWhenAccountQueryFails(t *testing.T) {
	body := []byte(`{"data":{"total_granted":100,"total_used":40,"total_available":60}}`)
	snapshot, err := newAPISnapshotFromParts(
		credentialCandidate{AuthIndex: "acct", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"},
		newAPIQuotaStatus{QuotaPerUnit: 1, DisplayType: "USD"},
		body,
		nil,
		nil,
		errors.New("NewAPI account endpoint returned HTTP 401"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Quantities) != 1 || snapshot.Quantities[0].Scope != "token" {
		t.Fatalf("quantities = %+v, want valid key quota", snapshot.Quantities)
	}
	if section := snapshot.Sections["account_quota"]; section.Status != "error" || section.ErrorCode == "" {
		t.Fatalf("account quota section = %+v, want explicit error", section)
	}
}

func TestNewAPISnapshotMarksMissingManagementPAT(t *testing.T) {
	body := []byte(`{"data":{"total_granted":100,"total_used":40,"total_available":60}}`)
	snapshot, err := newAPISnapshotFromParts(
		credentialCandidate{AuthIndex: "acct", Name: "newapi", Provider: "new-api", BaseURL: "https://example.com"},
		newAPIQuotaStatus{QuotaPerUnit: 1, DisplayType: "USD"},
		body,
		nil,
		nil,
		errNewAPIAccountPATMissing,
	)
	if err != nil {
		t.Fatal(err)
	}
	section := snapshot.Sections["account_quota"]
	if section.Status != "missing" || section.ErrorCode != "PAT_NOT_CONFIGURED" {
		t.Fatalf("account quota section = %+v, want missing PAT status", section)
	}
}
