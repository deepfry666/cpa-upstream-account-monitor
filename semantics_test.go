package main

import (
	"testing"
	"time"
)

func TestApplyThresholdsUsesLowestQuotaFraction(t *testing.T) {
	state.mu.Lock()
	previous := state.cfg.Thresholds
	state.cfg.Thresholds = thresholdConfig{WarningPercent: 20, CriticalPercent: 10}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.Thresholds = previous
		state.mu.Unlock()
	})

	snapshot := accountSnapshot{
		Kind:   kindPeriodQuota,
		Status: statusOK,
		Windows: []quotaWindow{
			{RemainingFraction: floatPtr(0.75)},
			{RemainingFraction: floatPtr(0.15)},
		},
	}
	if got := applyThresholds(snapshot).Status; got != statusWarning {
		t.Fatalf("status = %q, want warning", got)
	}

	snapshot.Windows[1].RemainingFraction = floatPtr(0.05)
	if got := applyThresholds(snapshot).Status; got != statusCritical {
		t.Fatalf("status = %q, want critical", got)
	}
}

func TestBuildReportSummarizesKindsAndAlerts(t *testing.T) {
	previous := state.store
	state.store = newSnapshotStore()
	t.Cleanup(func() { state.store = previous })
	state.store.put(accountSnapshot{AccountID: "balance", Kind: kindBalance, Status: statusOK})
	snapshot := accountSnapshot{AccountID: "quota", Kind: kindQuota, Status: statusCritical, Quantities: []quotaQuantity{{Name: "account", Remaining: "5", Total: "100", Unit: "quota"}}}
	state.store.put(snapshot)

	report := buildReport()
	if report.Summary.Balances != 1 || report.Summary.Quotas != 1 || len(report.Alerts) != 1 {
		t.Fatalf("unexpected report summary: %+v", report)
	}
	if report.Alerts[0].Type != "quota_low" || report.Alerts[0].AccountID != "quota" {
		t.Fatalf("unexpected alert: %+v", report.Alerts[0])
	}
}

func TestSnapshotStorePrunesRemovedAccounts(t *testing.T) {
	store := newSnapshotStore()
	store.put(accountSnapshot{AccountID: "keep", CheckedAt: nowForTest()})
	store.put(accountSnapshot{AccountID: "remove", CheckedAt: nowForTest()})
	store.prune(map[string]struct{}{"keep": {}})
	if _, ok := store.get("remove"); ok {
		t.Fatal("removed account still present")
	}
}

func TestSnapshotStoreListHasStableAccountOrder(t *testing.T) {
	store := newSnapshotStore()
	store.put(accountSnapshot{AccountID: "b", AccountName: "Zeta", CheckedAt: nowForTest()})
	store.put(accountSnapshot{AccountID: "a", AccountName: "Alpha", CheckedAt: nowForTest()})
	store.put(accountSnapshot{AccountID: "c", AccountName: "Alpha", CheckedAt: nowForTest()})
	items := store.list()
	if got := []string{items[0].AccountID, items[1].AccountID, items[2].AccountID}; !equalStrings(got, []string{"a", "c", "b"}) {
		t.Fatalf("account order = %v", got)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func nowForTest() (value time.Time) {
	return time.Now().UTC()
}
