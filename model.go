package main

import "time"

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
	Unit              string     `json:"unit,omitempty"`
	ResetAt           *time.Time `json:"reset_at,omitempty"`
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
}

type thresholdConfig struct {
	WarningPercent  float64 `yaml:"warning_percent"`
	CriticalPercent float64 `yaml:"critical_percent"`
}

type snapshotError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type accountSnapshot struct {
	AccountID     string          `json:"id"`
	AccountName   string          `json:"name"`
	Provider      string          `json:"provider"`
	AdapterID     string          `json:"adapter"`
	BaseURL       string          `json:"base_url,omitempty"`
	Kind          accountKind     `json:"kind"`
	Status        accountStatus   `json:"status"`
	Capabilities  []string        `json:"capabilities,omitempty"`
	Balances      []moneyBalance  `json:"balances,omitempty"`
	Windows       []quotaWindow   `json:"windows,omitempty"`
	Quantities    []quotaQuantity `json:"quantities,omitempty"`
	CheckedAt     time.Time       `json:"checked_at"`
	LastSuccessAt *time.Time      `json:"last_success_at,omitempty"`
	LatencyMs     int64           `json:"latency_ms,omitempty"`
	Stale         bool            `json:"stale"`
	Error         *snapshotError  `json:"error,omitempty"`
	Warnings      []string        `json:"warnings,omitempty"`
	Details       map[string]any  `json:"details,omitempty"`
}

type providerView struct {
	ID                      string           `json:"id"`
	Name                    string           `json:"name"`
	DefaultName             string           `json:"default_name,omitempty"`
	CustomName              string           `json:"custom_name,omitempty"`
	Provider                string           `json:"provider"`
	BaseURL                 string           `json:"base_url"`
	ProxyConfigured         bool             `json:"proxy_configured"`
	Adapter                 string           `json:"adapter"`
	AdapterOverride         string           `json:"adapter_override,omitempty"`
	Monitored               bool             `json:"monitored"`
	ManagementPATConfigured bool             `json:"management_pat_configured"`
	KeyHint                 string           `json:"key_hint,omitempty"`
	Latest                  *accountSnapshot `json:"latest,omitempty"`
}

type monitorUIState struct {
	Version     string            `json:"version"`
	GeneratedAt time.Time         `json:"generated_at"`
	Refreshing  bool              `json:"refreshing"`
	LastRefresh *time.Time        `json:"last_refresh,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
	Providers   []providerView    `json:"providers"`
	Report      report            `json:"report"`
	Config      monitorConfigView `json:"config"`
}

type monitorConfigView struct {
	CacheTTLSeconds      int     `json:"cache_ttl_seconds"`
	RequestTimeoutSecond int     `json:"request_timeout_seconds"`
	WarningPercent       float64 `json:"warning_percent"`
	CriticalPercent      float64 `json:"critical_percent"`
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
	Level     accountStatus `json:"level"`
	AccountID string        `json:"account_id"`
	Type      string        `json:"type"`
	Message   string        `json:"message"`
}

type credentialCandidate struct {
	AuthIndex string
	AuthID    string
	Name      string
	Provider  string
	BaseURL   string
	ProxyURL  string
}
