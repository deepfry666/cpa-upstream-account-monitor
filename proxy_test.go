package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryEndpointDirectModeBypassesInheritedProxy(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxy must not be used", http.StatusBadGateway)
	}))
	defer proxy.Close()

	_, err := queryEndpointWithProxyContext(
		context.Background(),
		"",
		target.URL,
		"",
		proxySettings{Mode: proxyModeDirect, URL: proxy.URL},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", targetHits.Load())
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("proxy hits = %d, want 0", proxyHits.Load())
	}
}

func TestQueryEndpointClosesIdleConnectionsAfterRequest(t *testing.T) {
	connClosed := make(chan struct{})
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	target.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case <-connClosed:
			default:
				close(connClosed)
			}
		}
	}
	target.Start()
	defer target.Close()

	if _, err := queryEndpointWithProxyContext(
		context.Background(),
		"",
		target.URL,
		"",
		proxySettings{Mode: proxyModeDirect},
		nil,
	); err != nil {
		t.Fatal(err)
	}

	select {
	case <-connClosed:
	case <-time.After(time.Second):
		t.Fatal("request returned while its idle connection remained open")
	}
}

func TestProviderViewsPreserveProxyInheritanceAndDirectOverride(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Proxy Relay
    base-url: https://relay.example.com/v1
    proxy-url: http://127.0.0.1:18080
    api-key-entries:
      - api-key: first-key
      - api-key: second-key
        proxy-mode: direct
`)

	views, err := discoverProviderViews("")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("providers = %d, want 2", len(views))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	if views[0].ProxyMode != proxyModeInherit || !views[0].ProxyConfigured {
		t.Fatalf("inherited proxy view = %+v", views[0])
	}
	if views[1].ProxyMode != proxyModeDirect || views[1].ProxyConfigured {
		t.Fatalf("direct override view = %+v", views[1])
	}
}

func TestDiscoverProviderViewsRejectsUnsupportedStaticProxyScheme(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Broken Proxy Relay
    base-url: https://relay.example.com/v1
    proxy-url: ftp://127.0.0.1:18080
    api-key-entries:
      - api-key: first-key
`)

	_, err := discoverProviderViews("")
	if err == nil {
		t.Fatal("discoverProviderViews accepted an unsupported static proxy scheme")
	}
	if !strings.Contains(err.Error(), "不支持的代理协议") {
		t.Fatalf("error = %q, want unsupported proxy scheme message", err)
	}
}

func TestDiscoverProviderViewsRejectsStaticDirectModeWithProxyURL(t *testing.T) {
	useTestPluginState(t, `openai-compatibility:
  - name: Conflicting Proxy Relay
    base-url: https://relay.example.com/v1
    proxy-mode: direct
    proxy-url: http://127.0.0.1:18080
    api-key-entries:
      - api-key: first-key
`)

	_, err := discoverProviderViews("")
	if err == nil {
		t.Fatal("discoverProviderViews accepted proxy-url in direct mode")
	}
	if !strings.Contains(err.Error(), "不能配置 proxy-url") {
		t.Fatalf("error = %q, want direct mode proxy URL rejection", err)
	}
}

func TestQueryCandidateDirectOverrideBypassesInheritedProxy(t *testing.T) {
	useTestPluginState(t, "")

	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		http.NotFound(w, nil)
	}))
	defer target.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxy must not be used", http.StatusBadGateway)
	}))
	defer proxy.Close()

	candidate := credentialCandidate{
		AccountID:         "acct_proxy_direct",
		AuthIndex:         "acct_proxy_direct",
		Name:              "Proxy Direct",
		Provider:          "zai",
		BaseURL:           target.URL,
		AllowInternalHTTP: true,
		ProxyMode:         proxyModeDirect,
		ProxyURL:          proxy.URL,
	}
	_, err := queryCandidateContext(context.Background(), "", candidate, []byte(`{"api_key":"secret"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", targetHits.Load())
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("proxy hits = %d, want 0", proxyHits.Load())
	}
}

func TestQueryCandidateInheritsHostCredentialProxy(t *testing.T) {
	useTestPluginState(t, "")

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer proxy.Close()

	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	candidate := credentialCandidate{
		AccountID:         "acct_host_proxy",
		AuthIndex:         "acct_host_proxy",
		Name:              "Host Proxy",
		Provider:          "zai",
		BaseURL:           target.URL,
		AllowInternalHTTP: true,
		Source:            "host",
		SourceID:          "host-auth-1",
	}
	_, _ = queryCandidateContext(
		context.Background(),
		"",
		candidate,
		[]byte(`{"api_key":"secret","proxy_url":"`+proxy.URL+`"}`),
		nil,
	)
	if proxyHits.Load() != 1 {
		t.Fatalf("proxy hits = %d, want 1", proxyHits.Load())
	}
	if targetHits.Load() != 0 {
		t.Fatalf("target hits = %d, want 0 direct hits", targetHits.Load())
	}
}

func TestQueryCandidateCredentialDirectOverridesProviderProxy(t *testing.T) {
	useTestPluginState(t, "")

	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		http.NotFound(w, nil)
	}))
	defer target.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "provider proxy must be bypassed", http.StatusBadGateway)
	}))
	defer proxy.Close()

	candidate := credentialCandidate{
		AccountID:         "acct_proxy_credential_direct",
		AuthIndex:         "acct_proxy_credential_direct",
		Name:              "Credential Direct",
		Provider:          "zai",
		BaseURL:           target.URL,
		AllowInternalHTTP: true,
		ProxyMode:         proxyModeURL,
		ProxyURL:          proxy.URL,
		Source:            "host",
		SourceID:          "host-auth-direct",
	}
	_, err := queryCandidateContext(
		context.Background(),
		"",
		candidate,
		[]byte(`{"api_key":"secret","proxy_url":"direct"}`),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1 direct hit", targetHits.Load())
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("proxy hits = %d, want 0", proxyHits.Load())
	}
}

func TestQueryEndpointUsesSOCKS5Proxy(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	proxyURL, proxyHits, closeProxy := startTestSOCKS5Proxy(t)
	defer closeProxy()

	_, err := queryEndpointWithProxyContext(
		context.Background(),
		"",
		target.URL,
		"",
		proxySettings{Mode: proxyModeURL, URL: proxyURL},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", targetHits.Load())
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("proxy hits = %d, want 1", proxyHits.Load())
	}
}

func TestQueryEndpointUsesSOCKS5Authentication(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	proxyURL, proxyHits, closeProxy := startTestSOCKS5ProxyWithAuth(t, "proxy-user", "proxy-pass")
	defer closeProxy()
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	parsedProxy.User = url.UserPassword("proxy-user", "proxy-pass")

	_, err = queryEndpointWithProxyContext(
		context.Background(),
		"",
		target.URL,
		"",
		proxySettings{Mode: proxyModeURL, URL: parsedProxy.String()},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", targetHits.Load())
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("proxy hits = %d, want 1", proxyHits.Load())
	}
}

func TestQueryEndpointUsesHTTPProxyAuthentication(t *testing.T) {
	const proxyUsername = "proxy-user"
	const proxyPassword = "proxy-pass"
	expectedAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(proxyUsername+":"+proxyPassword))

	var proxyHits atomic.Int32
	var authorizedHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		proxyHits.Add(1)
		if request.Header.Get("Proxy-Authorization") != expectedAuthorization {
			http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
			return
		}
		authorizedHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer proxy.Close()
	parsedProxy, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsedProxy.User = url.UserPassword(proxyUsername, proxyPassword)

	response, err := queryEndpointWithProxyContext(
		context.Background(),
		"",
		"http://upstream.example.test/status",
		"",
		proxySettings{Mode: proxyModeURL, URL: parsedProxy.String()},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if proxyHits.Load() != 1 || authorizedHits.Load() != 1 {
		t.Fatalf("proxy hits = %d, authorized hits = %d, want 1 each", proxyHits.Load(), authorizedHits.Load())
	}
}

func TestQueryEndpointRejectsUnsupportedProxySchemeWithoutDirectFallback(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	_, err := queryEndpointWithProxyContext(
		context.Background(),
		"",
		target.URL,
		"",
		proxySettings{Mode: proxyModeURL, URL: "ftp://127.0.0.1:18080"},
		nil,
	)
	if err == nil {
		t.Fatal("expected unsupported proxy scheme error")
	}
	if !strings.Contains(err.Error(), "不支持的代理") {
		t.Fatalf("error = %q, want Chinese unsupported proxy message", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("target hits = %d, want 0", targetHits.Load())
	}
}

func TestQueryEndpointProxyAuthenticationFailureDoesNotExposeCredentials(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	const username = "credential-user"
	const password = "credential-secret"
	proxyURL, proxyHits, closeProxy := startTestSOCKS5ProxyWithAuth(t, "expected-user", "expected-secret")
	defer closeProxy()
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	parsedProxy.User = url.UserPassword(username, password)

	_, err = queryEndpointWithProxyContext(
		context.Background(),
		"",
		target.URL,
		"",
		proxySettings{Mode: proxyModeURL, URL: parsedProxy.String()},
		nil,
	)
	if err == nil {
		t.Fatal("expected proxy authentication failure")
	}
	if strings.Contains(err.Error(), username) || strings.Contains(err.Error(), password) {
		t.Fatalf("error exposed proxy credentials: %q", err)
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("proxy hits = %d, want 1", proxyHits.Load())
	}
	if targetHits.Load() != 0 {
		t.Fatalf("target hits = %d, want 0", targetHits.Load())
	}
}

func TestQueryEndpointRejectsProxyURLWithoutHost(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()

	_, err := queryEndpointWithProxyContext(
		context.Background(),
		"",
		target.URL,
		"",
		proxySettings{Mode: proxyModeURL, URL: "http://"},
		nil,
	)
	if err == nil {
		t.Fatal("expected invalid proxy URL error")
	}
	if !strings.Contains(err.Error(), "代理 URL") {
		t.Fatalf("error = %q, want Chinese proxy URL message", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("target hits = %d, want 0", targetHits.Load())
	}
}

func TestAccountProxyOverrideBeatsProviderProxy(t *testing.T) {
	useTestPluginState(t, "")
	candidate := credentialCandidate{
		AccountID: "acct_proxy_override",
		AuthIndex: "acct_proxy_override",
		Provider:  "zai",
		BaseURL:   "https://relay.example.test/v1",
		ProxyMode: proxyModeInherit,
		ProxyURL:  "http://127.0.0.1:18080",
	}
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{
		candidate.AccountID: {ProxyMode: proxyModeDirect},
	}
	state.mu.Unlock()

	_, normalized, matched := adapterFor(candidate)
	if !matched {
		t.Fatal("candidate did not match an adapter")
	}
	if normalized.ProxyMode != proxyModeDirect {
		t.Fatalf("proxy mode = %q, want %q", normalized.ProxyMode, proxyModeDirect)
	}
	if normalized.ProxyURL != "" {
		t.Fatalf("proxy URL = %q, want empty direct override", normalized.ProxyURL)
	}
}

func TestAccountProxyURLOverrideBeatsProviderProxy(t *testing.T) {
	useTestPluginState(t, "")
	candidate := credentialCandidate{
		AccountID: "acct_proxy_url_override",
		AuthIndex: "acct_proxy_url_override",
		Provider:  "zai",
		BaseURL:   "https://relay.example.test/v1",
		ProxyMode: proxyModeURL,
		ProxyURL:  "http://127.0.0.1:18080",
	}
	state.mu.Lock()
	state.cfg.Monitors = map[string]monitorConfig{
		candidate.AccountID: {ProxyMode: proxyModeURL, ProxyURL: "http://127.0.0.1:28080"},
	}
	state.mu.Unlock()

	_, normalized, matched := adapterFor(candidate)
	if !matched {
		t.Fatal("candidate did not match an adapter")
	}
	if normalized.ProxyMode != proxyModeURL || normalized.ProxyURL != "http://127.0.0.1:28080" {
		t.Fatalf("proxy = (%q, %q), want account override", normalized.ProxyMode, normalized.ProxyURL)
	}
}

func TestConfigureRejectsInvalidMonitorProxyMode(t *testing.T) {
	useTestPluginState(t, "")
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	body := map[string]any{
		"source_config_path": cfg.SourceConfigPath,
		"cache_path":         cfg.CachePath,
		"preferences_path":   cfg.PreferencesPath,
		"monitors": map[string]any{
			"acct": map[string]any{"proxy_mode": "socks4"},
		},
	}

	err := configure(mustJSON(body))
	if err == nil {
		t.Fatal("configure accepted an invalid monitor proxy mode")
	}
	if !strings.Contains(err.Error(), "代理模式仅支持") {
		t.Fatalf("error = %q, want invalid proxy mode message", err)
	}
}

func TestConfigureRejectsMonitorProxyURLModeWithoutURL(t *testing.T) {
	useTestPluginState(t, "")
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	body := map[string]any{
		"source_config_path": cfg.SourceConfigPath,
		"cache_path":         cfg.CachePath,
		"preferences_path":   cfg.PreferencesPath,
		"monitors": map[string]any{
			"acct": map[string]any{"proxy_mode": proxyModeURL},
		},
	}

	err := configure(mustJSON(body))
	if err == nil {
		t.Fatal("configure accepted URL proxy mode without a proxy URL")
	}
	if !strings.Contains(err.Error(), "必须提供 proxy_url") {
		t.Fatalf("error = %q, want missing proxy URL message", err)
	}
}

func TestConfigureRejectsUnsupportedMonitorProxyScheme(t *testing.T) {
	useTestPluginState(t, "")
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	body := map[string]any{
		"source_config_path": cfg.SourceConfigPath,
		"cache_path":         cfg.CachePath,
		"preferences_path":   cfg.PreferencesPath,
		"monitors": map[string]any{
			"acct": map[string]any{"proxy_mode": proxyModeURL, "proxy_url": "ftp://127.0.0.1:18080"},
		},
	}

	err := configure(mustJSON(body))
	if err == nil {
		t.Fatal("configure accepted an unsupported proxy scheme")
	}
	if !strings.Contains(err.Error(), "不支持的代理协议") {
		t.Fatalf("error = %q, want unsupported proxy scheme message", err)
	}
}

func TestConfigureRejectsMonitorProxyURLWithoutMode(t *testing.T) {
	useTestPluginState(t, "")
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	body := map[string]any{
		"source_config_path": cfg.SourceConfigPath,
		"cache_path":         cfg.CachePath,
		"preferences_path":   cfg.PreferencesPath,
		"monitors": map[string]any{
			"acct": map[string]any{"proxy_url": "http://127.0.0.1:18080"},
		},
	}

	err := configure(mustJSON(body))
	if err == nil {
		t.Fatal("configure accepted a proxy URL without an explicit mode")
	}
	if !strings.Contains(err.Error(), "必须指定 proxy_mode") {
		t.Fatalf("error = %q, want explicit proxy mode message", err)
	}
}

func TestConfigureRejectsMonitorProxyURLForDirectMode(t *testing.T) {
	useTestPluginState(t, "")
	state.mu.RLock()
	cfg := state.cfg
	state.mu.RUnlock()
	body := map[string]any{
		"source_config_path": cfg.SourceConfigPath,
		"cache_path":         cfg.CachePath,
		"preferences_path":   cfg.PreferencesPath,
		"monitors": map[string]any{
			"acct": map[string]any{"proxy_mode": proxyModeDirect, "proxy_url": "http://127.0.0.1:18080"},
		},
	}

	err := configure(mustJSON(body))
	if err == nil {
		t.Fatal("configure accepted a proxy URL in direct mode")
	}
	if !strings.Contains(err.Error(), "不能配置 proxy_url") {
		t.Fatalf("error = %q, want direct mode proxy URL rejection", err)
	}
}

func startTestSOCKS5Proxy(t *testing.T) (string, *atomic.Int32, func()) {
	t.Helper()
	return startTestSOCKS5ProxyWithAuth(t, "", "")
}

func startTestSOCKS5ProxyWithAuth(t *testing.T, username, password string) (string, *atomic.Int32, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				hits.Add(1)
				if err := serveTestSOCKS5Connection(connection, username, password); err != nil {
					return
				}
			}()
		}
	}()
	proxyURL := &url.URL{Scheme: "socks5", Host: listener.Addr().String()}
	return proxyURL.String(), &hits, func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("SOCKS5 proxy did not stop")
		}
	}
}

func serveTestSOCKS5Connection(connection net.Conn, username, password string) error {
	reader := bufio.NewReader(connection)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	if header[0] != 5 {
		return io.ErrUnexpectedEOF
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return err
	}
	expectedMethod := byte(0)
	if username != "" {
		expectedMethod = 2
	}
	methodAccepted := false
	for _, method := range methods {
		if method == expectedMethod {
			methodAccepted = true
			break
		}
	}
	if !methodAccepted {
		_, _ = connection.Write([]byte{5, 255})
		return io.ErrUnexpectedEOF
	}
	if _, err := connection.Write([]byte{5, expectedMethod}); err != nil {
		return err
	}
	if expectedMethod == 2 {
		authHeader := make([]byte, 2)
		if _, err := io.ReadFull(reader, authHeader); err != nil {
			return err
		}
		usernameBytes := make([]byte, int(authHeader[1]))
		if _, err := io.ReadFull(reader, usernameBytes); err != nil {
			return err
		}
		passwordLength := make([]byte, 1)
		if _, err := io.ReadFull(reader, passwordLength); err != nil {
			return err
		}
		passwordBytes := make([]byte, int(passwordLength[0]))
		if _, err := io.ReadFull(reader, passwordBytes); err != nil {
			return err
		}
		if authHeader[0] != 1 || string(usernameBytes) != username || string(passwordBytes) != password {
			_, _ = connection.Write([]byte{1, 1})
			return io.ErrUnexpectedEOF
		}
		if _, err := connection.Write([]byte{1, 0}); err != nil {
			return err
		}
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil {
		return err
	}
	if request[0] != 5 || request[1] != 1 {
		return io.ErrUnexpectedEOF
	}
	host, err := readSOCKS5Host(reader, request[3])
	if err != nil {
		return err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return err
	}
	target, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))))
	if err != nil {
		return err
	}
	defer target.Close()
	if _, err := connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	copyConnection := func(destination io.Writer, source io.Reader) {
		_, _ = io.Copy(destination, source)
		done <- struct{}{}
	}
	go copyConnection(target, reader)
	go copyConnection(connection, target)
	<-done
	return nil
}

func readSOCKS5Host(reader io.Reader, addressType byte) (string, error) {
	switch addressType {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", err
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		return string(address), nil
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	default:
		return "", io.ErrUnexpectedEOF
	}
}
