package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestManagementRegistrationUsesExactRoutes(t *testing.T) {
	raw, err := handleMethod("management.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	var envelopeValue envelope
	if err := json.Unmarshal(raw, &envelopeValue); err != nil {
		t.Fatal(err)
	}
	var registrationValue managementRegistration
	if err := json.Unmarshal(envelopeValue.Result, &registrationValue); err != nil {
		t.Fatal(err)
	}
	if len(registrationValue.Routes) != 8 {
		t.Fatalf("routes = %d, want 8", len(registrationValue.Routes))
	}
	for _, route := range registrationValue.Routes {
		if route.Path == "" || route.Path[0] != '/' {
			t.Fatalf("route is not absolute: %+v", route)
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/summary" {
			continue
		}
		if route.Method == http.MethodPost && route.Path == "/upstream-monitor/refresh" {
			continue
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/config" {
			continue
		}
		if route.Method == http.MethodPut && route.Path == "/upstream-monitor/config" {
			continue
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/state" {
			continue
		}
		if route.Method == http.MethodPut && route.Path == "/upstream-monitor/providers" {
			continue
		}
		if route.Method == http.MethodPost && route.Path == "/upstream-monitor/cleanup" {
			continue
		}
		if route.Method == http.MethodGet && route.Path == "/upstream-monitor/history" {
			continue
		}
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestReadTokenRequiresBearerHeader(t *testing.T) {
	const envName = "CPA_UPSTREAM_MONITOR_TEST_TOKEN"
	t.Setenv(envName, "test-token")
	state.mu.Lock()
	previous := state.cfg.ReadTokenEnv
	state.cfg.ReadTokenEnv = envName
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.cfg.ReadTokenEnv = previous
		state.mu.Unlock()
	})

	if validReadToken(http.Header{}) {
		t.Fatal("empty headers unexpectedly authorized")
	}
	if validReadToken(http.Header{"Authorization": []string{"Bearer wrong"}}) {
		t.Fatal("wrong token unexpectedly authorized")
	}
	if !validReadToken(http.Header{"Authorization": []string{"Bearer test-token"}}) {
		t.Fatal("correct token was rejected")
	}
}
