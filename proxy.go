package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	proxyModeInherit = "inherit"
	proxyModeDirect  = "direct"
	proxyModeURL     = "url"
)

type proxySettings struct {
	Mode string
	URL  string
}

func normalizeProxyMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", proxyModeInherit:
		return proxyModeInherit, nil
	case proxyModeDirect:
		return proxyModeDirect, nil
	case proxyModeURL:
		return proxyModeURL, nil
	default:
		return "", fmt.Errorf("代理模式仅支持 inherit、direct 或 url")
	}
}

func resolveStaticProxy(entryMode, entryURL, providerMode, providerURL string) (string, string, error) {
	mode, err := normalizeProxyMode(entryMode)
	if err != nil {
		return "", "", err
	}
	if mode == proxyModeInherit && strings.TrimSpace(entryURL) != "" && strings.TrimSpace(entryMode) == "" {
		mode = proxyModeURL
	}
	if mode != proxyModeInherit {
		if mode == proxyModeDirect && strings.TrimSpace(entryURL) != "" {
			return "", "", fmt.Errorf("direct 模式不能配置 proxy-url")
		}
		if mode == proxyModeURL && strings.TrimSpace(entryURL) == "" {
			return "", "", fmt.Errorf("指定代理模式必须提供 proxy-url")
		}
		if mode == proxyModeURL {
			if err := validateProxyURL(entryURL); err != nil {
				return "", "", err
			}
		}
		return mode, strings.TrimSpace(entryURL), nil
	}

	inheritedMode, err := normalizeProxyMode(providerMode)
	if err != nil {
		return "", "", err
	}
	if inheritedMode == proxyModeInherit && strings.TrimSpace(providerURL) != "" && strings.TrimSpace(providerMode) == "" {
		inheritedMode = proxyModeURL
	}
	if inheritedMode == proxyModeURL && strings.TrimSpace(providerURL) == "" {
		return "", "", fmt.Errorf("指定代理模式必须提供 proxy-url")
	}
	if inheritedMode == proxyModeDirect && strings.TrimSpace(providerURL) != "" {
		return "", "", fmt.Errorf("direct 模式不能配置 proxy-url")
	}
	if inheritedMode == proxyModeURL {
		if err := validateProxyURL(providerURL); err != nil {
			return "", "", err
		}
	}
	if inheritedMode == proxyModeDirect {
		return proxyModeDirect, strings.TrimSpace(providerURL), nil
	}
	return proxyModeInherit, strings.TrimSpace(providerURL), nil
}

func validateProxyURL(raw string) error {
	proxy, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("无法解析代理 URL")
	}
	if strings.TrimSpace(proxy.Host) == "" {
		return fmt.Errorf("代理 URL 缺少主机名")
	}
	switch proxy.Scheme {
	case "http", "https", "socks5":
		return nil
	default:
		return fmt.Errorf("不支持的代理协议 %q", proxy.Scheme)
	}
}

func normalizedProxyMode(mode, proxyURL string) string {
	normalized, err := normalizeProxyMode(mode)
	if err != nil {
		return proxyModeInherit
	}
	if normalized == proxyModeInherit && strings.TrimSpace(proxyURL) != "" {
		return proxyModeInherit
	}
	return normalized
}

func proxyIsConfigured(mode, proxyURL string) bool {
	return normalizedProxyMode(mode, proxyURL) != proxyModeDirect && strings.TrimSpace(proxyURL) != ""
}

func candidateProxyURL(candidate credentialCandidate) string {
	mode, err := normalizeProxyMode(candidate.ProxyMode)
	if err != nil || mode == proxyModeDirect {
		return ""
	}
	return strings.TrimSpace(candidate.ProxyURL)
}

func applyCredentialProxy(candidate *credentialCandidate, storage []byte) {
	if candidate == nil {
		return
	}
	proxyURL := proxyURLFromCredential(storage)
	if strings.EqualFold(proxyURL, proxyModeDirect) {
		candidate.ProxyMode = proxyModeDirect
		candidate.ProxyURL = ""
		return
	}
	mode, err := normalizeProxyMode(candidate.ProxyMode)
	if err != nil || mode != proxyModeInherit || strings.TrimSpace(candidate.ProxyURL) != "" {
		return
	}
	if proxyURL == "" {
		return
	}
	candidate.ProxyMode = proxyModeURL
	candidate.ProxyURL = proxyURL
}

func proxyURLFromCredential(storage []byte) string {
	var object map[string]any
	if json.Unmarshal(storage, &object) != nil {
		return ""
	}
	if value, ok := object["proxy_url"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if metadata := objectValue(object, "metadata"); metadata != nil {
		if value, ok := metadata["proxy_url"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func queryEndpointWithProxyContext(ctx context.Context, callbackID, endpoint, authorization string, settings proxySettings, extraHeaders map[string]string) (hostHTTPResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cpa-upstream-monitor/"+pluginVersion)
	for key, value := range extraHeaders {
		request.Header.Set(key, value)
	}

	directDialer := &net.Dialer{Timeout: requestTimeout(), KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:           directDialer.DialContext,
		TLSHandshakeTimeout:   requestTimeout(),
		ResponseHeaderTimeout: requestTimeout(),
		IdleConnTimeout:       30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	if strings.TrimSpace(settings.Mode) != proxyModeDirect && strings.TrimSpace(settings.URL) != "" {
		proxy, err := url.Parse(strings.TrimSpace(settings.URL))
		if err != nil {
			return hostHTTPResponse{}, fmt.Errorf("无法解析代理 URL")
		}
		if strings.TrimSpace(proxy.Host) == "" {
			return hostHTTPResponse{}, fmt.Errorf("代理 URL 缺少主机名")
		}
		switch proxy.Scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(proxy)
		case "socks5":
			dialer := &socks5Dialer{proxyAddress: proxy.Host, timeout: requestTimeout()}
			if proxy.User != nil {
				dialer.username = proxy.User.Username()
				dialer.password, _ = proxy.User.Password()
				dialer.hasAuth = true
			}
			transport.DialContext = dialer.DialContext
		default:
			return hostHTTPResponse{}, fmt.Errorf("不支持的代理协议 %q", proxy.Scheme)
		}
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many upstream redirects")
			}
			previous := via[len(via)-1]
			if !strings.EqualFold(previous.URL.Scheme, request.URL.Scheme) || !strings.EqualFold(previous.URL.Host, request.URL.Host) {
				return fmt.Errorf("cross-host upstream redirect is not supported")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return hostHTTPResponse{}, err
	}
	return hostHTTPResponse{StatusCode: response.StatusCode, Headers: response.Header, Body: body}, nil
}

type socks5Dialer struct {
	proxyAddress string
	timeout      time.Duration
	username     string
	password     string
	hasAuth      bool
}

func (d *socks5Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("SOCKS5 proxy only supports TCP")
	}
	connection, err := (&net.Dialer{Timeout: d.timeout, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", d.proxyAddress)
	if err != nil {
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = connection.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else if d.timeout > 0 {
		_ = connection.SetDeadline(time.Now().Add(d.timeout))
	}
	if err := socks5Negotiate(connection, d.username, d.password, d.hasAuth); err != nil {
		return nil, err
	}
	if err := socks5Connect(connection, address); err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	closeOnError = false
	return connection, nil
}

func socks5Negotiate(connection net.Conn, username, password string, hasAuth bool) error {
	method := byte(0)
	if hasAuth {
		method = 2
		if len(username) > 255 || len(password) > 255 {
			return fmt.Errorf("SOCKS5 proxy credentials are too long")
		}
	}
	if _, err := connection.Write([]byte{5, 1, method}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 5 || response[1] != method {
		return fmt.Errorf("SOCKS5 proxy did not accept the requested authentication mode")
	}
	if !hasAuth {
		return nil
	}
	authRequest := []byte{1, byte(len(username))}
	authRequest = append(authRequest, username...)
	authRequest = append(authRequest, byte(len(password)))
	authRequest = append(authRequest, password...)
	if _, err := connection.Write(authRequest); err != nil {
		return err
	}
	authResponse := make([]byte, 2)
	if _, err := io.ReadFull(connection, authResponse); err != nil {
		return err
	}
	if authResponse[0] != 1 || authResponse[1] != 0 {
		return fmt.Errorf("SOCKS5 proxy authentication failed")
	}
	return nil
}

func socks5Connect(connection net.Conn, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid SOCKS5 target address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid SOCKS5 target port")
	}
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 1)
			request = append(request, ipv4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return fmt.Errorf("invalid SOCKS5 target host")
		}
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err := connection.Write(request); err != nil {
		return err
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 5 {
		return fmt.Errorf("invalid SOCKS5 response version")
	}
	if response[1] != 0 {
		return fmt.Errorf("SOCKS5 proxy connection failed with code %d", response[1])
	}
	addressLength := 0
	switch response[3] {
	case 1:
		addressLength = net.IPv4len
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return err
		}
		addressLength = int(length[0])
	case 4:
		addressLength = net.IPv6len
	default:
		return fmt.Errorf("invalid SOCKS5 response address type")
	}
	discard := make([]byte, addressLength+2)
	_, err = io.ReadFull(connection, discard)
	return err
}
