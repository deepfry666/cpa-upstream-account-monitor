package main

import (
	"strings"
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

func TestApplyThresholdsRecomputesExistingDerivedStatus(t *testing.T) {
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
			{RemainingFraction: floatPtr(0.15)},
		},
	}
	snapshot = applyThresholds(snapshot)
	if snapshot.Status != statusWarning {
		t.Fatalf("initial status = %q, want warning", snapshot.Status)
	}

	state.mu.Lock()
	state.cfg.Thresholds = thresholdConfig{WarningPercent: 10, CriticalPercent: 5}
	state.mu.Unlock()
	if got := applyThresholds(snapshot).Status; got != statusOK {
		t.Fatalf("recomputed status = %q, want ok", got)
	}
}

func TestApplyThresholdsPreservesNonThresholdWarnings(t *testing.T) {
	state.mu.Lock()
	previous := state.cfg.Thresholds
	state.cfg.Thresholds = thresholdConfig{WarningPercent: 10, CriticalPercent: 5}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.Thresholds = previous
		state.mu.Unlock()
	})

	snapshot := accountSnapshot{
		Kind:   kindPeriodQuota,
		Status: statusWarning,
		Windows: []quotaWindow{
			{RemainingFraction: floatPtr(0.75)},
		},
		Sections: map[string]sectionStatus{
			"billing": {Status: "error", ErrorCode: "UPSTREAM_HTTP_503"},
		},
	}
	if got := applyThresholds(snapshot).Status; got != statusWarning {
		t.Fatalf("status = %q, want retained warning", got)
	}
}

func TestApplyThresholdsTreatsOverageAsCritical(t *testing.T) {
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
			{Name: "daily", RemainingFraction: floatPtr(0), UsedFraction: floatPtr(1), ExcessAmount: "2"},
			{Name: "weekly", RemainingFraction: floatPtr(0.88), UsedFraction: floatPtr(0.12)},
		},
	}
	if got := applyThresholds(snapshot).Status; got != statusCritical {
		t.Fatalf("status = %q, want critical", got)
	}
}

func TestApplyThresholdsUsesCurrencyAndAccountCashRules(t *testing.T) {
	state.mu.Lock()
	previous := state.cfg.Thresholds
	state.cfg.Thresholds = thresholdConfig{
		WarningPercent:  20,
		CriticalPercent: 10,
		CashByCurrency: map[string]cashThreshold{
			"CNY": {Warning: "10", Critical: "1"},
		},
		CashByAccount: map[string]cashThreshold{
			"premium": {Warning: "100", Critical: "50"},
		},
	}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.Thresholds = previous
		state.mu.Unlock()
	})

	tests := []struct {
		name     string
		snapshot accountSnapshot
		want     accountStatus
	}{
		{
			name:     "currency critical",
			snapshot: accountSnapshot{AccountID: "basic", Kind: kindBalance, Status: statusOK, Balances: []moneyBalance{{Amount: "0.01", Currency: "CNY"}}},
			want:     statusCritical,
		},
		{
			name:     "currency warning",
			snapshot: accountSnapshot{AccountID: "basic", Kind: kindBalance, Status: statusOK, Balances: []moneyBalance{{Amount: "5", Currency: "CNY"}}},
			want:     statusWarning,
		},
		{
			name:     "other currency has no configured rule",
			snapshot: accountSnapshot{AccountID: "basic", Kind: kindBalance, Status: statusOK, Balances: []moneyBalance{{Amount: "5", Currency: "USD"}}},
			want:     statusOK,
		},
		{
			name:     "account override wins",
			snapshot: accountSnapshot{AccountID: "premium", Kind: kindBalance, Status: statusOK, Balances: []moneyBalance{{Amount: "60", Currency: "CNY"}}},
			want:     statusWarning,
		},
		{
			name:     "negative balance is critical without rule",
			snapshot: accountSnapshot{AccountID: "basic", Kind: kindBalance, Status: statusOK, Balances: []moneyBalance{{Amount: "-1", Currency: "USD"}}},
			want:     statusCritical,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := applyThresholds(test.snapshot).Status; got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeCashThresholdsRejectsInvalidRanges(t *testing.T) {
	for _, thresholds := range []map[string]cashThreshold{
		{"CNY": {Warning: "1", Critical: "2"}},
		{"CNY": {Warning: "-1", Critical: "0"}},
		{"CNY": {Warning: "bad", Critical: "1"}},
	} {
		if _, err := normalizeCashThresholds(thresholds); err == nil {
			t.Fatalf("thresholds %+v were accepted", thresholds)
		}
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

func TestBuildReportFromAccountsUsesSeverityOrderRegardlessOfOrder(t *testing.T) {
	accounts := []accountSnapshot{
		{AccountID: "warning", Status: statusWarning},
		{AccountID: "ok", Status: statusOK},
		{AccountID: "error", Status: statusError},
		{AccountID: "unknown", Status: statusUnknown},
		{AccountID: "critical", Status: statusCritical},
	}
	for _, ordered := range [][]accountSnapshot{accounts, {accounts[3], accounts[1], accounts[4], accounts[0], accounts[2]}} {
		report := buildReportFromAccounts(ordered)
		if report.Status != statusError {
			t.Fatalf("status = %q, want error", report.Status)
		}
		if report.Summary.Error != 1 || report.Summary.Critical != 1 || report.Summary.Warning != 1 || report.Summary.Unknown != 1 || report.Summary.OK != 1 {
			t.Fatalf("summary = %+v, want stable counts", report.Summary)
		}
	}
}

func TestBuildReportExcludesExplicitlyUnmonitoredAccounts(t *testing.T) {
	previousStore := state.store
	previousPrefs := state.prefs
	state.store = newSnapshotStore()
	state.prefs = newMonitorPrefs()
	t.Cleanup(func() {
		state.store = previousStore
		state.prefs = previousPrefs
	})
	if err := state.prefs.update("hidden", boolPtr(false), "", "", false, false, nil); err != nil {
		t.Fatal(err)
	}
	state.store.put(accountSnapshot{AccountID: "visible", Status: statusOK})
	state.store.put(accountSnapshot{AccountID: "hidden", Status: statusError})

	report := buildReport()
	if report.Status != statusOK || report.Summary.Total != 1 || len(report.Accounts) != 1 || report.Accounts[0].AccountID != "visible" {
		t.Fatalf("unmonitored account affected report: %+v", report)
	}
}

func TestBuildReportStaleWarningDoesNotMaskAccountErrors(t *testing.T) {
	staleOK := buildReportFromAccounts([]accountSnapshot{{AccountID: "stale", Status: statusOK, Stale: true}})
	if staleOK.Status != statusWarning || staleOK.Summary.Stale != 1 {
		t.Fatalf("stale report = %+v, want warning", staleOK)
	}
	staleError := buildReportFromAccounts([]accountSnapshot{{AccountID: "failed", Status: statusError, Stale: true}})
	if staleError.Status != statusError {
		t.Fatalf("stale error status = %q, want error", staleError.Status)
	}
}

func TestAlertsForAccountIncludesUnavailableKeyCodes(t *testing.T) {
	for _, status := range []accountStatus{statusDisabled, statusError} {
		alerts := alertsForAccount(accountSnapshot{AccountID: "acct", Status: status, Error: &snapshotError{Code: "KEY_EXPIRED", Message: "expired"}})
		if len(alerts) != 1 || alerts[0].Code != "KEY_EXPIRED" || alerts[0].AccountID != "acct" {
			t.Fatalf("status %q alerts = %+v", status, alerts)
		}
	}
}

func TestAlertsForAccountIncludesSectionFailure(t *testing.T) {
	updatedAt := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	alerts := alertsForAccount(accountSnapshot{
		AccountID: "acct",
		Status:    statusWarning,
		Balances:  []moneyBalance{{Amount: "10", Currency: "USD", BalanceType: "available"}},
		Sections: map[string]sectionStatus{
			"balance": {Status: "available", UpdatedAt: &updatedAt},
			"billing": {Status: "error", ErrorCode: "UPSTREAM_HTTP_503", Message: "billing unavailable"},
		},
	})
	if len(alerts) != 1 {
		t.Fatalf("alerts = %+v, want one section failure", alerts)
	}
	alert := alerts[0]
	if alert.Level != statusWarning || alert.AccountID != "acct" || alert.Type != "section" || alert.Code != "UPSTREAM_HTTP_503" || !strings.Contains(alert.Message, "计费") || alert.Diagnostic != "billing unavailable" {
		t.Fatalf("section alert = %+v", alert)
	}
}

func TestAlertsForAccountKeepsLowQuotaAlongsideSectionFailure(t *testing.T) {
	state.mu.Lock()
	previous := state.cfg.Thresholds
	state.cfg.Thresholds = thresholdConfig{WarningPercent: 20, CriticalPercent: 10}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.Thresholds = previous
		state.mu.Unlock()
	})

	alerts := alertsForAccount(accountSnapshot{
		AccountID: "acct",
		Kind:      kindQuota,
		Status:    statusWarning,
		Windows:   []quotaWindow{{Name: "weekly", RemainingFraction: floatPtr(0.08)}},
		Sections: map[string]sectionStatus{
			"billing": {Status: "error", ErrorCode: "UPSTREAM_HTTP_503", Message: "billing unavailable"},
		},
	})
	if len(alerts) != 2 {
		t.Fatalf("alerts = %+v, want section and low-quota alerts", alerts)
	}
	if alerts[0].Type != "section" || alerts[1].Type != "quota_low" {
		t.Fatalf("alert types = %q, %q, want section then quota_low", alerts[0].Type, alerts[1].Type)
	}
}

func TestErrorSnapshotRecordsFirstAttempt(t *testing.T) {
	previousStore := state.store
	previousPrefs := state.prefs
	state.store = newSnapshotStore()
	state.prefs = newMonitorPrefs()
	t.Cleanup(func() {
		state.store = previousStore
		state.prefs = previousPrefs
	})

	before := time.Now().UTC()
	snapshot := errorSnapshot(credentialCandidate{AccountID: "first-failure", Name: "First failure"}, "UPSTREAM_HTTP_401", "authentication failed")
	after := time.Now().UTC()
	if snapshot.LastAttemptAt.Before(before) || snapshot.LastAttemptAt.After(after) {
		t.Fatalf("last_attempt_at = %s, want between %s and %s", snapshot.LastAttemptAt, before, after)
	}
	if snapshot.LastSuccessAt != nil {
		t.Fatalf("first failure has last_success_at = %s", snapshot.LastSuccessAt)
	}
	if snapshot.Status != statusError || snapshot.Error == nil || snapshot.Error.Code != "UPSTREAM_HTTP_401" {
		t.Fatalf("first failure snapshot = %+v", snapshot)
	}
}

func TestErrorSnapshotPreservesPreviousSuccessData(t *testing.T) {
	previousStore := state.store
	previousPrefs := state.prefs
	state.store = newSnapshotStore()
	state.prefs = newMonitorPrefs()
	t.Cleanup(func() {
		state.store = previousStore
		state.prefs = previousPrefs
	})

	successAt := time.Date(2026, 9, 18, 1, 0, 0, 0, time.UTC)
	state.store.put(accountSnapshot{
		AccountID:     "acct",
		AccountName:   "Account",
		Status:        statusOK,
		Kind:          kindBalance,
		Balances:      []moneyBalance{{Amount: "46.76", Currency: "USD", BalanceType: "remaining"}},
		CheckedAt:     successAt,
		LastSuccessAt: &successAt,
	})
	before := time.Now().UTC()
	snapshot := errorSnapshot(credentialCandidate{AccountID: "acct", Name: "Account"}, "UPSTREAM_HTTP_401", "authentication failed")
	after := time.Now().UTC()
	if snapshot.LastSuccessAt == nil || !snapshot.LastSuccessAt.Equal(successAt) {
		t.Fatalf("last_success_at = %v, want %s", snapshot.LastSuccessAt, successAt)
	}
	if snapshot.LastAttemptAt.Before(before) || snapshot.LastAttemptAt.After(after) {
		t.Fatalf("last_attempt_at = %s, want this attempt", snapshot.LastAttemptAt)
	}
	if len(snapshot.Balances) != 1 || snapshot.Balances[0].Amount != "46.76" || !snapshot.Stale {
		t.Fatalf("previous success data was not preserved: %+v", snapshot)
	}
}

func TestAccountSnapshotWithSuccessRecordsAttemptAndSuccess(t *testing.T) {
	now := time.Date(2026, 9, 19, 1, 2, 3, 0, time.UTC)
	snapshot := accountSnapshotWithSuccess(accountSnapshot{}, accountSnapshot{AccountID: "acct", Status: statusOK, CheckedAt: now.Add(-time.Hour)}, now, 250*time.Millisecond)
	if !snapshot.LastAttemptAt.Equal(now) {
		t.Fatalf("last_attempt_at = %s, want %s", snapshot.LastAttemptAt, now)
	}
	if snapshot.LastSuccessAt == nil || !snapshot.LastSuccessAt.Equal(now) {
		t.Fatalf("last_success_at = %v, want %s", snapshot.LastSuccessAt, now)
	}
	if snapshot.LatencyMs != 250 {
		t.Fatalf("latency_ms = %d, want 250", snapshot.LatencyMs)
	}
}

func TestAccountSnapshotWithSuccessMarksOnlySuccessfulSectionsCurrent(t *testing.T) {
	now := time.Date(2026, 9, 19, 1, 2, 3, 0, time.UTC)
	snapshot := accountSnapshotWithSuccess(accountSnapshot{}, accountSnapshot{
		AccountID: "acct",
		Status:    statusWarning,
		Sections: map[string]sectionStatus{
			"balance": {Status: "available"},
			"billing": {Status: "error", ErrorCode: "UPSTREAM_HTTP_503"},
		},
	}, now, 0)
	if snapshot.Sections["balance"].UpdatedAt == nil || !snapshot.Sections["balance"].UpdatedAt.Equal(now) {
		t.Fatalf("balance updated_at = %v, want %s", snapshot.Sections["balance"].UpdatedAt, now)
	}
	if snapshot.Sections["billing"].UpdatedAt != nil {
		t.Fatalf("failed billing section was marked current: %+v", snapshot.Sections["billing"])
	}
}

func TestSectionErrorKeepsPrimaryBalanceAndCreatesAlert(t *testing.T) {
	snapshot := accountSnapshot{
		AccountID: "sub2api-account",
		Status:    statusOK,
		Balances:  []moneyBalance{{Amount: "46.76", Currency: "USD", BalanceType: "remaining"}},
	}
	markSectionError(&snapshot, "billing", "UPSTREAM_HTTP_503", "billing unavailable")
	if snapshot.Status != statusWarning {
		t.Fatalf("status = %q, want warning", snapshot.Status)
	}
	if len(snapshot.Balances) != 1 || snapshot.Balances[0].Amount != "46.76" {
		t.Fatalf("balance was discarded: %+v", snapshot.Balances)
	}
	alerts := alertsForAccount(snapshot)
	if len(alerts) != 1 || alerts[0].MetricID != "billing" || alerts[0].Code != "UPSTREAM_HTTP_503" {
		t.Fatalf("alerts = %+v, want billing failure", alerts)
	}
}

func TestAccountAlertsUseStableIdentityIDs(t *testing.T) {
	account := accountSnapshot{
		AccountID: "acct",
		Status:    statusWarning,
		Sections: map[string]sectionStatus{
			"billing": {Status: "error", ErrorCode: "UPSTREAM_HTTP_503", Message: "unavailable"},
		},
	}
	first := alertsForAccount(account)[0]
	account.Sections["billing"] = sectionStatus{Status: "error", ErrorCode: "UPSTREAM_HTTP_503", Message: "different diagnostic text"}
	second := alertsForAccount(account)[0]
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("alert IDs = %q and %q, want stable non-empty ID", first.ID, second.ID)
	}
	account.Sections["usage"] = sectionStatus{Status: "error", ErrorCode: "UPSTREAM_HTTP_503", Message: "unavailable"}
	alerts := alertsForAccount(account)
	if alerts[0].ID == alerts[1].ID {
		t.Fatalf("different sections share alert ID %q", alerts[0].ID)
	}
}

func TestQuotaAlertIdentifiesLowWindowAndThreshold(t *testing.T) {
	state.mu.Lock()
	previous := state.cfg.Thresholds
	state.cfg.Thresholds = thresholdConfig{WarningPercent: 20, CriticalPercent: 10}
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.Thresholds = previous
		state.mu.Unlock()
	})

	alerts := alertsForAccount(accountSnapshot{
		AccountID: "acct",
		Status:    statusCritical,
		Windows: []quotaWindow{
			{Name: "daily", RemainingFraction: floatPtr(0.50)},
			{Name: "weekly", RemainingFraction: floatPtr(0.08)},
		},
	})
	if len(alerts) != 1 {
		t.Fatalf("alerts = %+v, want one low-window alert", alerts)
	}
	alert := alerts[0]
	if alert.WindowID != "weekly" || alert.Current != "8" || alert.Unit != "%" || alert.Threshold != "10" || alert.Code != "QUOTA_BELOW_CRITICAL" {
		t.Fatalf("quota alert = %+v", alert)
	}
}

func TestAlertUsesChineseSummaryAndKeepsDiagnostic(t *testing.T) {
	const diagnostic = "upstream returned HTTP 503: service unavailable"
	alerts := alertsForAccount(accountSnapshot{
		AccountID: "acct",
		Status:    statusWarning,
		Sections: map[string]sectionStatus{
			"billing": {Status: "error", ErrorCode: "UPSTREAM_HTTP_503", Message: diagnostic},
		},
	})
	if len(alerts) != 1 {
		t.Fatalf("alerts = %+v", alerts)
	}
	alert := alerts[0]
	if alert.Message == diagnostic || !strings.Contains(alert.Message, "计费") {
		t.Fatalf("main message = %q, want Chinese billing summary", alert.Message)
	}
	if alert.Diagnostic != diagnostic {
		t.Fatalf("diagnostic = %q, want %q", alert.Diagnostic, diagnostic)
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
