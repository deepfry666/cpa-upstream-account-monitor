package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMonitorPrefsEncryptsAndReloadsPAT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	prefs := newMonitorPrefs()
	if err := prefs.configure(path); err != nil {
		t.Fatal(err)
	}
	if err := prefs.update("account-1", boolPtr(true), "newapi-usage", "pat-secret-value", false, false, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || string(raw) == `{"known":{"account-1":true},"monitored":{"account-1":true},"adapters":{"account-1":"newapi-usage"},"management_pats":{"account-1":"pat-secret-value"}}` {
		t.Fatal("PAT was written as plaintext")
	}
	reloaded := newMonitorPrefs()
	if err := reloaded.configure(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.pat("account-1"); got != "pat-secret-value" {
		t.Fatalf("reloaded PAT = %q", got)
	}
	if err := reloaded.update("account-1", nil, "", "", true, false, nil); err != nil {
		t.Fatal(err)
	}
	if reloaded.pat("account-1") != "" || reloaded.patConfigured("account-1") {
		t.Fatal("PAT was not cleared")
	}
}

func TestMonitorPrefsStoresAndClearsCustomName(t *testing.T) {
	dir := t.TempDir()
	prefs := newMonitorPrefs()
	if err := prefs.configure(filepath.Join(dir, "prefs.json")); err != nil {
		t.Fatal(err)
	}
	name := "主力中转"
	if err := prefs.update("account-1", boolPtr(true), "", "", false, false, &name); err != nil {
		t.Fatal(err)
	}
	if got := prefs.name("account-1", "fallback"); got != name {
		t.Fatalf("custom name = %q, want %q", got, name)
	}
	clearName := ""
	if err := prefs.update("account-1", nil, "", "", false, false, &clearName); err != nil {
		t.Fatal(err)
	}
	if got := prefs.name("account-1", "fallback"); got != "fallback" {
		t.Fatalf("cleared custom name = %q, want fallback", got)
	}
}

func TestMonitorPrefsRejectsInvalidCustomName(t *testing.T) {
	prefs := newMonitorPrefs()
	tooLong := strings.Repeat("x", 81)
	if err := prefs.update("account-1", nil, "", "", false, false, &tooLong); err == nil {
		t.Fatal("too-long custom name was accepted")
	}
	control := "bad\nname"
	if err := prefs.update("account-1", nil, "", "", false, false, &control); err == nil {
		t.Fatal("control character custom name was accepted")
	}
}

func boolPtr(value bool) *bool { return &value }
