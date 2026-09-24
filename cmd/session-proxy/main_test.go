package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testProxy(t *testing.T, upstream http.Handler) *sessionProxy {
	t.Helper()
	cpaServer := httptest.NewServer(upstream)
	t.Cleanup(cpaServer.Close)
	cpampServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/status" || request.Header.Get("Authorization") != "Bearer cpamp-admin" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cpampServer.Close)
	proxy, err := newSessionProxy([]byte("cpa-management"), cpaServer.URL, cpampServer.URL, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}

func login(t *testing.T, proxy http.Handler, key string) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/upstream-monitor/session", strings.NewReader(`{"key":`+mustJSON(key)+`}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(requestMarker, "1")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, body=%q", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d, want 1", len(cookies))
	}
	return cookies[0]
}

func mustJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestSessionUsesSecureHTTPOnlyCookie(t *testing.T) {
	proxy := testProxy(t, http.NotFoundHandler())
	cookie := login(t, proxy, "cpamp-admin")
	if cookie.Name != sessionCookie || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/upstream-monitor" {
		t.Fatalf("cookie = %#v", cookie)
	}
	if cookie.Value == "" || strings.Contains(cookie.Value, "cpamp-admin") {
		t.Fatal("session cookie must be opaque")
	}

	request := httptest.NewRequest(http.MethodPost, "/upstream-monitor/session", strings.NewReader(`{"key":"wrong"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(requestMarker, "1")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401", response.Code)
	}
}

func TestAuthorizedRequestUsesCPAKeyAndWhitelistedPath(t *testing.T) {
	var method, path, query, authorization, cookie string
	proxy := testProxy(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method, path, query = request.Method, request.URL.Path, request.URL.RawQuery
		authorization = request.Header.Get("Authorization")
		cookie = request.Header.Get("Cookie")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	session := login(t, proxy, "cpamp-admin")

	request := httptest.NewRequest(http.MethodGet, "/upstream-monitor/api/history?account_id=acct_1&limit=50", nil)
	request.AddCookie(session)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Body.String() != `{"ok":true}` {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if method != http.MethodGet || path != "/v0/management/upstream-monitor/history" || query != "account_id=acct_1&limit=50" {
		t.Fatalf("upstream request = %s %s?%s", method, path, query)
	}
	if authorization != "Bearer cpa-management" {
		t.Fatalf("authorization = %q", authorization)
	}
	if cookie != "" {
		t.Fatalf("browser session leaked upstream: %q", cookie)
	}
}

func TestProxyRejectsUnauthorizedAndUnexpectedRoutes(t *testing.T) {
	proxy := testProxy(t, http.NotFoundHandler())

	unauthorized := httptest.NewRecorder()
	proxy.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/upstream-monitor/api/state", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	session := login(t, proxy, "cpamp-admin")
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodDelete, "/upstream-monitor/api/state", http.StatusMethodNotAllowed},
		{http.MethodGet, "/upstream-monitor/api/not-real", http.StatusMethodNotAllowed},
		{http.MethodPost, "/upstream-monitor/api/cleanup", http.StatusBadRequest},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.AddCookie(session)
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Errorf("%s %s = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}

func TestExpiredSessionIsRejectedAndDeleteClearsCookie(t *testing.T) {
	proxy := testProxy(t, http.NotFoundHandler())
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	proxy.now = func() time.Time { return now }
	session := login(t, proxy, "cpamp-admin")
	now = now.Add(sessionDuration + time.Second)

	request := httptest.NewRequest(http.MethodGet, "/upstream-monitor/api/state", nil)
	request.AddCookie(session)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d, want 401", response.Code)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/upstream-monitor/session", nil)
	deleteRequest.Header.Set(requestMarker, "1")
	deleteRequest.AddCookie(session)
	deleteResponse := httptest.NewRecorder()
	proxy.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent || len(deleteResponse.Result().Cookies()) != 1 || deleteResponse.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("delete response = %d %#v", deleteResponse.Code, deleteResponse.Result().Cookies())
	}
}

func TestWriteRequestForwardsBodyWithoutBrowserCredentials(t *testing.T) {
	var body, marker string
	proxy := testProxy(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		body = string(raw)
		marker = request.Header.Get(requestMarker)
		writer.WriteHeader(http.StatusNoContent)
	}))
	session := login(t, proxy, "cpamp-admin")
	request := httptest.NewRequest(http.MethodPost, "/upstream-monitor/api/refresh", strings.NewReader(`{"scope":"all"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(requestMarker, "1")
	request.AddCookie(session)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || body != `{"scope":"all"}` {
		t.Fatalf("write response/body = %d %q", response.Code, body)
	}
	if marker != "" {
		t.Fatalf("browser request marker leaked upstream: %q", marker)
	}
}

func TestProxyRejectsOversizedWriteBody(t *testing.T) {
	proxy := testProxy(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("oversized request reached upstream")
	}))
	session := login(t, proxy, "cpamp-admin")
	request := httptest.NewRequest(http.MethodPost, "/upstream-monitor/api/refresh", strings.NewReader(strings.Repeat("x", maxProxyBody+1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(requestMarker, "1")
	request.AddCookie(session)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413", response.Code)
	}
}
