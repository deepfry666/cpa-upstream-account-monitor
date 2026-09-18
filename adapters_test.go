package main

import (
	"testing"
	"time"
)

func TestQuotaWindowFromPercentUsesExplicitUnits(t *testing.T) {
	tests := []struct {
		name          string
		object        map[string]any
		wantUsed      float64
		wantRemaining float64
	}{
		{
			name:          "percent field is a percentage",
			object:        map[string]any{"usagePercent": 0.5},
			wantUsed:      0.005,
			wantRemaining: 0.995,
		},
		{
			name:          "one percent is not one hundred percent",
			object:        map[string]any{"percent": 1.0},
			wantUsed:      0.01,
			wantRemaining: 0.99,
		},
		{
			name:          "fraction field is a fraction",
			object:        map[string]any{"usedFraction": 0.5},
			wantUsed:      0.5,
			wantRemaining: 0.5,
		},
		{
			name:          "amounts are converted using their limit",
			object:        map[string]any{"used": 1.0, "limit": 100.0},
			wantUsed:      0.01,
			wantRemaining: 0.99,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window := quotaWindowFromPercent(test.object, "session")
			if window == nil || window.UsedFraction == nil || window.RemainingFraction == nil {
				t.Fatalf("window = %+v", window)
			}
			if absFloat(*window.UsedFraction-test.wantUsed) > 0.000001 || absFloat(*window.RemainingFraction-test.wantRemaining) > 0.000001 {
				t.Fatalf("fractions = used %.6f remaining %.6f, want used %.6f remaining %.6f", *window.UsedFraction, *window.RemainingFraction, test.wantUsed, test.wantRemaining)
			}
		})
	}
}

func TestParseOpenCodeSnapshotKeepsDefinedWindowOrder(t *testing.T) {
	snapshot, err := parseOpenCodeSnapshot(credentialCandidate{AuthIndex: "opencode-1", Name: "OpenCode Go"}, []byte(`{"usage":{"monthly":{"percent":4},"weekly":{"percent":12},"rolling":{"percent":63}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Windows) != 3 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	want := []string{"session", "weekly", "monthly"}
	for index, name := range want {
		if snapshot.Windows[index].Name != name {
			t.Fatalf("window %d = %q, want %q; all=%+v", index, snapshot.Windows[index].Name, name, snapshot.Windows)
		}
	}
}

func TestQuotaWindowFunctionsPreserveOverage(t *testing.T) {
	tests := []struct {
		name   string
		window *quotaWindow
	}{
		{
			name:   "values helper",
			window: quotaWindowFromValues("daily", "-2", "10", "12", "USD", nil),
		},
		{
			name:   "limit helper",
			window: quotaWindowFromLimit(map[string]any{"limit": float64(10), "remaining": float64(-2), "used": float64(12), "unit": "USD"}, "daily"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window := test.window
			if window == nil {
				t.Fatal("overage window was discarded")
			}
			if window.RemainingAmount != "0" || window.TotalAmount != "10" || window.UsedAmount != "12" || window.ExcessAmount != "2" {
				t.Fatalf("window = %+v", window)
			}
			if window.RemainingFraction == nil || *window.RemainingFraction != 0 || window.UsedFraction == nil || *window.UsedFraction != 1 {
				t.Fatalf("fractions = %+v", window)
			}
		})
	}
}

func TestTimeValueParsesSupportedFormatsAndRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "unix seconds", value: float64(1789779723), want: "2026-09-19T01:02:03Z"},
		{name: "unix milliseconds", value: float64(1789779723000), want: "2026-09-19T01:02:03Z"},
		{name: "RFC3339 string", value: "2026-09-19T01:02:03Z", want: "2026-09-19T01:02:03Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := timeValue(map[string]any{"at": test.value}, "at")
			if value == nil || value.Format(time.RFC3339) != test.want {
				t.Fatalf("timeValue(%v) = %v, want %s", test.value, value, test.want)
			}
		})
	}
	if value := timeValue(map[string]any{"at": "not-a-time"}, "at"); value != nil {
		t.Fatalf("invalid time parsed as %s", value)
	}
}

func TestDecimalStringKeepsSmallNonZeroValuesVisible(t *testing.T) {
	if got := decimalString(0.0042); got != "0.0042" {
		t.Fatalf("decimalString(0.0042) = %q", got)
	}
}

func TestSub2APIUsageEndpointPreservesDeploymentPrefix(t *testing.T) {
	endpoint, err := sub2APIUsageEndpoint(credentialCandidate{BaseURL: "https://relay.example.com/team-a/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://relay.example.com/team-a/v1/usage?days=30" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}

func TestNewAPIManagementEndpointStripsInferenceSuffix(t *testing.T) {
	endpoint, err := newAPIManagementEndpoint(credentialCandidate{BaseURL: "https://relay.example.com/team-a/v1"}, "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://relay.example.com/team-a/api/status" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}

func TestFixedEndpointAllowsInternalHTTPOnlyWhenExplicit(t *testing.T) {
	if _, err := fixedEndpointWithOptions("http://relay.internal/team-a/v1", "", "/api/status", endpointOptions{}); err == nil {
		t.Fatal("internal HTTP endpoint was accepted without explicit opt-in")
	}
	endpoint, err := fixedEndpointWithOptions("http://relay.internal/team-a/v1", "", "/api/status", endpointOptions{AllowInternalHTTP: true, StripInferenceSuffix: true})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "http://relay.internal/team-a/api/status" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}

func TestFixedEndpointNormalizesDeploymentPaths(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		path    string
		want    string
		options endpointOptions
	}{
		{
			name: "double and trailing slashes",
			base: "https://relay.example.com/team-a//v1/",
			path: "/v1//usage?days=7",
			want: "https://relay.example.com/team-a/v1/usage?days=7",
		},
		{
			name: "endpoint already contains deployment prefix",
			base: "https://relay.example.com/team-a/v1",
			path: "/team-a/v1/usage",
			want: "https://relay.example.com/team-a/v1/usage",
		},
		{
			name:    "encoded deployment path",
			base:    "https://relay.example.com/team%20a/v1",
			path:    "/v1/usage",
			want:    "https://relay.example.com/team%20a/v1/usage",
			options: endpointOptions{},
		},
		{
			name:    "management suffix stripped",
			base:    "https://relay.example.com/team-a/v1/",
			path:    "/api/status?probe=1",
			want:    "https://relay.example.com/team-a/api/status?probe=1",
			options: endpointOptions{StripInferenceSuffix: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, err := fixedEndpointWithOptions(test.base, "", test.path, test.options)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint != test.want {
				t.Fatalf("endpoint = %q, want %q", endpoint, test.want)
			}
		})
	}
}

func TestConfigureRejectsInternalHTTPWithoutExplicitOptIn(t *testing.T) {
	useTestPluginState(t, "")
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	baseConfig := map[string]any{
		"source_config_path": cfg.SourceConfigPath,
		"cache_path":         cfg.CachePath,
		"preferences_path":   cfg.PreferencesPath,
	}
	baseConfig["monitors"] = map[string]any{
		"acct": map[string]any{"base_url": "http://relay.internal/team-a/v1"},
	}
	if err := configure(mustJSON(baseConfig)); err == nil {
		t.Fatal("configure accepted internal HTTP without explicit opt-in")
	}
	baseConfig["monitors"] = map[string]any{
		"acct": map[string]any{"base_url": "http://relay.internal/team-a/v1", "allow_internal_http": true},
	}
	if err := configure(mustJSON(baseConfig)); err != nil {
		t.Fatal(err)
	}
	if !state.cfg.Monitors["acct"].AllowInternalHTTP {
		t.Fatal("explicit internal HTTP opt-in was not retained")
	}
}
