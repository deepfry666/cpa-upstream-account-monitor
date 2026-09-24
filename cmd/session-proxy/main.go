package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultListenAddress = ":18320"
	defaultCPAURL        = "http://cli-proxy-api:8317"
	defaultCPAMPURL      = "http://cpa-manager-plus:18317"
	sessionCookie        = "upstream_monitor_session"
	sessionDuration      = 12 * time.Hour
	maxLoginBody         = 4096
	maxProxyBody         = 1 << 20
	maxResponseBody      = 16 << 20
	requestMarker        = "X-Upstream-Monitor-Request"
)

var allowedRoutes = map[string]map[string]bool{
	"state":          {http.MethodGet: true},
	"refresh":        {http.MethodGet: true, http.MethodPost: true},
	"config":         {http.MethodGet: true, http.MethodPut: true},
	"providers":      {http.MethodPut: true},
	"history":        {http.MethodGet: true},
	"cleanup":        {http.MethodPost: true},
	"alerts/dismiss": {http.MethodPost: true},
}

type sessionProxy struct {
	cpaKey   []byte
	cpaURL   *url.URL
	cpampURL *url.URL
	client   *http.Client
	now      func() time.Time

	sessionMu sync.Mutex
	sessions  map[string]time.Time
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		response, err := http.Get("http://127.0.0.1:18320/health")
		if err != nil || response.StatusCode != http.StatusNoContent {
			os.Exit(1)
		}
		_ = response.Body.Close()
		return
	}

	cpaKey, err := readSecret("CPA_MANAGEMENT_KEY_FILE")
	if err != nil {
		log.Fatalf("load CPA management key: %v", err)
	}
	baseURL := strings.TrimSpace(os.Getenv("CPA_BASE_URL"))
	if baseURL == "" {
		baseURL = defaultCPAURL
	}
	cpampURL := strings.TrimSpace(os.Getenv("CPAMP_BASE_URL"))
	if cpampURL == "" {
		cpampURL = defaultCPAMPURL
	}
	handler, err := newSessionProxy(cpaKey, baseURL, cpampURL, &http.Client{Timeout: 35 * time.Second})
	if err != nil {
		log.Fatalf("configure upstream monitor session proxy: %v", err)
	}

	listenAddress := strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}
	server := &http.Server{
		Addr:              listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("Upstream monitor session proxy listening on %s", listenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func readSecret(environmentName string) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(environmentName))
	if path == "" {
		return nil, fmt.Errorf("%s is required", environmentName)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if len(secret) == 0 {
		return nil, errors.New("secret file is empty")
	}
	return secret, nil
}

func newSessionProxy(cpaKey []byte, rawCPAURL, rawCPAMPURL string, client *http.Client) (*sessionProxy, error) {
	if len(cpaKey) == 0 {
		return nil, errors.New("CPA management key is required")
	}
	baseURL, err := parseBaseURL(rawCPAURL, "CPA_BASE_URL")
	if err != nil {
		return nil, err
	}
	adminURL, err := parseBaseURL(rawCPAMPURL, "CPAMP_BASE_URL")
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second}
	}
	return &sessionProxy{
		cpaKey:   append([]byte(nil), cpaKey...),
		cpaURL:   baseURL,
		cpampURL: adminURL,
		client:   client,
		now:      time.Now,
		sessions: make(map[string]time.Time),
	}, nil
}

func parseBaseURL(rawValue, name string) (*url.URL, error) {
	baseURL, err := url.Parse(strings.TrimSpace(rawValue))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" || baseURL.User != nil {
		return nil, fmt.Errorf("%s must be an http or https URL without credentials", name)
	}
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	return baseURL, nil
}

func (p *sessionProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	switch request.URL.Path {
	case "/health":
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	case "/upstream-monitor/session":
		switch request.Method {
		case http.MethodPost:
			p.createSession(writer, request)
		case http.MethodDelete:
			p.deleteSession(writer, request)
		default:
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	const prefix = "/upstream-monitor/api/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(writer, request)
		return
	}
	if !p.authorized(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	route := strings.TrimPrefix(request.URL.Path, prefix)
	methods, ok := allowedRoutes[route]
	if !ok || !methods[request.Method] {
		http.Error(writer, "route not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Method != http.MethodGet && request.Header.Get(requestMarker) != "1" {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	p.proxy(writer, request, route)
}

func (p *sessionProxy) createSession(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get(requestMarker) != "1" {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		http.Error(writer, "expected application/json", http.StatusUnsupportedMediaType)
		return
	}
	var input struct {
		Key string `json:"key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxLoginBody))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Key == "" {
		http.Error(writer, "invalid login", http.StatusBadRequest)
		return
	}
	authorized, err := p.verifyCPAMPAdmin(request.Context(), input.Key)
	if err != nil {
		http.Error(writer, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	if !authorized {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}

	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		http.Error(writer, "session unavailable", http.StatusInternalServerError)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	now := p.now()
	expires := now.Add(sessionDuration)
	p.sessionMu.Lock()
	for value, deadline := range p.sessions {
		if !now.Before(deadline) {
			delete(p.sessions, value)
		}
	}
	p.sessions[token] = expires
	p.sessionMu.Unlock()
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/upstream-monitor",
		Expires: expires, MaxAge: int(sessionDuration.Seconds()), HttpOnly: true,
		Secure: true, SameSite: http.SameSiteStrictMode,
	})
	writer.WriteHeader(http.StatusNoContent)
}

func (p *sessionProxy) verifyCPAMPAdmin(ctx context.Context, key string) (bool, error) {
	target := *p.cpampURL
	target.Path = strings.TrimRight(target.Path, "/") + "/status"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := p.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	switch response.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	default:
		return false, fmt.Errorf("CPAMP auth returned HTTP %d", response.StatusCode)
	}
}

func (p *sessionProxy) deleteSession(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get(requestMarker) != "1" {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if cookie, err := request.Cookie(sessionCookie); err == nil {
		p.sessionMu.Lock()
		delete(p.sessions, cookie.Value)
		p.sessionMu.Unlock()
	}
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/upstream-monitor", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	writer.WriteHeader(http.StatusNoContent)
}

func (p *sessionProxy) authorized(request *http.Request) bool {
	cookie, err := request.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	now := p.now()
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	expires, ok := p.sessions[cookie.Value]
	if !ok || !now.Before(expires) {
		delete(p.sessions, cookie.Value)
		return false
	}
	return true
}

func (p *sessionProxy) proxy(writer http.ResponseWriter, request *http.Request, route string) {
	rawBody, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxProxyBody))
	if err != nil {
		http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	target := *p.cpaURL
	target.Path = strings.TrimRight(target.Path, "/") + "/v0/management/upstream-monitor/" + route
	target.RawQuery = request.URL.RawQuery
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), bytes.NewReader(rawBody))
	if err != nil {
		http.Error(writer, "request unavailable", http.StatusInternalServerError)
		return
	}
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("Authorization", "Bearer "+string(p.cpaKey))
	if contentType := request.Header.Get("Content-Type"); contentType != "" {
		upstreamRequest.Header.Set("Content-Type", contentType)
	}

	response, err := p.client.Do(upstreamRequest)
	if err != nil {
		http.Error(writer, "CPA management API unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		http.Error(writer, "CPA response unavailable", http.StatusBadGateway)
		return
	}
	if len(raw) > maxResponseBody {
		http.Error(writer, "CPA response too large", http.StatusBadGateway)
		return
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(raw)
}
