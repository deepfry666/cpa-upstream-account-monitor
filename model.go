package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

type accountStatus string

const (
	statusOK       accountStatus = "ok"
	statusWarning  accountStatus = "warning"
	statusCritical accountStatus = "critical"
	statusError    accountStatus = "error"
	statusUnknown  accountStatus = "unknown"
	statusDisabled accountStatus = "disabled"
)

type accountKind string

const (
	kindBalance     accountKind = "balance"
	kindPeriodQuota accountKind = "period_quota"
	kindQuota       accountKind = "quota"
	kindUnsupported accountKind = "unsupported"
)

type moneyBalance struct {
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	BalanceType string `json:"balance_type"`
}

type quotaWindow struct {
	Name              string     `json:"name"`
	RemainingFraction *float64   `json:"remaining_fraction,omitempty"`
	UsedFraction      *float64   `json:"used_fraction,omitempty"`
	RemainingAmount   string     `json:"remaining_amount,omitempty"`
	TotalAmount       string     `json:"total_amount,omitempty"`
	UsedAmount        string     `json:"used_amount,omitempty"`
	ExcessAmount      string     `json:"excess_amount,omitempty"`
	Unit              string     `json:"unit,omitempty"`
	ResetAt           *time.Time `json:"reset_at,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
}

type quotaQuantity struct {
	Name      string     `json:"name"`
	Scope     string     `json:"scope,omitempty"`
	Source    string     `json:"source,omitempty"`
	Unlimited bool       `json:"unlimited,omitempty"`
	Remaining string     `json:"remaining,omitempty"`
	Total     string     `json:"total,omitempty"`
	Used      string     `json:"used,omitempty"`
	Unit      string     `json:"unit"`
	ResetAt   *time.Time `json:"reset_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type thresholdConfig struct {
	WarningPercent  float64                  `yaml:"warning_percent"`
	CriticalPercent float64                  `yaml:"critical_percent"`
	CashByCurrency  map[string]cashThreshold `yaml:"cash_by_currency,omitempty"`
	CashByAccount   map[string]cashThreshold `yaml:"cash_by_account,omitempty"`
}

type cashThreshold struct {
	Warning  string `yaml:"warning" json:"warning"`
	Critical string `yaml:"critical" json:"critical"`
}

type snapshotError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type accountSnapshot struct {
	AccountID      string                   `json:"id"`
	AccountName    string                   `json:"name"`
	Provider       string                   `json:"provider"`
	AdapterID      string                   `json:"adapter"`
	BaseURL        string                   `json:"base_url,omitempty"`
	Kind           accountKind              `json:"kind"`
	Status         accountStatus            `json:"status"`
	Capabilities   []string                 `json:"capabilities,omitempty"`
	Balances       []moneyBalance           `json:"balances,omitempty"`
	Windows        []quotaWindow            `json:"windows,omitempty"`
	Quantities     []quotaQuantity          `json:"quantities,omitempty"`
	CheckedAt      time.Time                `json:"checked_at"`
	LastAttemptAt  time.Time                `json:"last_attempt_at,omitempty"`
	LastSuccessAt  *time.Time               `json:"last_success_at,omitempty"`
	LatencyMs      int64                    `json:"latency_ms,omitempty"`
	ConfigRevision int64                    `json:"config_revision,omitempty"`
	Stale          bool                     `json:"stale"`
	Error          *snapshotError           `json:"error,omitempty"`
	Warnings       []string                 `json:"warnings,omitempty"`
	Sections       map[string]sectionStatus `json:"sections,omitempty"`
	Details        map[string]any           `json:"details,omitempty"`
}

type sectionStatus struct {
	Status      string     `json:"status"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	ErrorCode   string     `json:"error_code,omitempty"`
	Message     string     `json:"message,omitempty"`
	ActionURL   string     `json:"action_url,omitempty"`
	ActionLabel string     `json:"action_label,omitempty"`
}

func markSectionError(snapshot *accountSnapshot, name, code, message string) {
	if snapshot == nil || strings.TrimSpace(name) == "" {
		return
	}
	if snapshot.Sections == nil {
		snapshot.Sections = map[string]sectionStatus{}
	}
	snapshot.Sections[name] = sectionStatus{Status: "error", ErrorCode: code, Message: message}
	snapshot.Status = maxStatus(snapshot.Status, statusWarning)
}

type providerView struct {
	ID                      string           `json:"id"`
	Name                    string           `json:"name"`
	DefaultName             string           `json:"default_name,omitempty"`
	CustomName              string           `json:"custom_name,omitempty"`
	Provider                string           `json:"provider"`
	BaseURL                 string           `json:"base_url"`
	ProxyMode               string           `json:"proxy_mode"`
	ProxyConfigured         bool             `json:"proxy_configured"`
	Adapter                 string           `json:"adapter"`
	AdapterOverride         string           `json:"adapter_override,omitempty"`
	Monitored               bool             `json:"monitored"`
	ManagementPATConfigured bool             `json:"management_pat_configured"`
	PATStatus               string           `json:"pat_status,omitempty"`
	PATStatusMessage        string           `json:"pat_status_message,omitempty"`
	RelinkRequired          bool             `json:"relink_required,omitempty"`
	FirstSeenAt             *time.Time       `json:"first_seen_at,omitempty"`
	KeyHint                 string           `json:"key_hint,omitempty"`
	Latest                  *accountSnapshot `json:"latest,omitempty"`
}

type monitorUIState struct {
	Version       string              `json:"version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Revision      int64               `json:"revision"`
	Refreshing    bool                `json:"refreshing"`
	LastRefresh   *time.Time          `json:"last_refresh,omitempty"`
	LastError     string              `json:"last_error,omitempty"`
	Warnings      []string            `json:"warnings,omitempty"`
	DirectorySync directorySyncStatus `json:"directory_sync"`
	Providers     []providerView      `json:"providers"`
	RefreshJob    *accountRefreshJob  `json:"refresh_job,omitempty"`
	Report        report              `json:"report"`
	Config        monitorConfigView   `json:"config"`
}

type directorySyncStatus struct {
	Status        string     `json:"status"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type monitorConfigView struct {
	CacheTTLSeconds      int                      `json:"cache_ttl_seconds"`
	SyncIntervalSeconds  int                      `json:"sync_interval_seconds"`
	RequestTimeoutSecond int                      `json:"request_timeout_seconds"`
	AccountTimeoutSecond int                      `json:"account_timeout_seconds"`
	WarningPercent       float64                  `json:"warning_percent"`
	CriticalPercent      float64                  `json:"critical_percent"`
	CashByCurrency       map[string]cashThreshold `json:"cash_by_currency,omitempty"`
	CashByAccount        map[string]cashThreshold `json:"cash_by_account,omitempty"`
}

type report struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Status        accountStatus     `json:"status"`
	Summary       reportSummary     `json:"summary"`
	Alerts        []reportAlert     `json:"alerts,omitempty"`
	Accounts      []accountSnapshot `json:"accounts"`
}

type reportSummary struct {
	Total        int `json:"total"`
	OK           int `json:"ok"`
	Warning      int `json:"warning"`
	Critical     int `json:"critical"`
	Error        int `json:"error"`
	Unknown      int `json:"unknown"`
	Stale        int `json:"stale"`
	Balances     int `json:"balances"`
	PeriodQuotas int `json:"period_quotas"`
	Quotas       int `json:"quotas"`
}

type reportAlert struct {
	ID         string        `json:"id,omitempty"`
	Level      accountStatus `json:"level"`
	AccountID  string        `json:"account_id"`
	MetricID   string        `json:"metric_id,omitempty"`
	WindowID   string        `json:"window_id,omitempty"`
	Type       string        `json:"type"`
	Code       string        `json:"code,omitempty"`
	Message    string        `json:"message"`
	Diagnostic string        `json:"diagnostic,omitempty"`
	Current    string        `json:"current,omitempty"`
	Unit       string        `json:"unit,omitempty"`
	Threshold  string        `json:"threshold,omitempty"`
	OccurredAt *time.Time    `json:"occurred_at,omitempty"`
}

func stableAlertID(alert reportAlert) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		alert.AccountID,
		alert.MetricID,
		alert.WindowID,
		alert.Type,
		alert.Code,
	}, "\x00")))
	return "alert_" + hex.EncodeToString(sum[:8])
}

type credentialCandidate struct {
	AccountID         string
	LegacyID          string
	AuthIndex         string
	AuthID            string
	Name              string
	Provider          string
	BaseURL           string
	ManagementBaseURL string
	ProxyMode         string
	ProxyURL          string
	AllowInternalHTTP bool
	Source            string
	SourceID          string
	Fingerprint       string
}

func candidateAccountID(candidate credentialCandidate) string {
	if strings.TrimSpace(candidate.AccountID) != "" {
		return strings.TrimSpace(candidate.AccountID)
	}
	return strings.TrimSpace(candidate.AuthIndex)
}
