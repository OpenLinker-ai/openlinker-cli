package providerbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	invokePath          = "/invoke"
	maxRequestBodyBytes = 32 << 20
)

type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
)

type Config struct {
	Provider  Provider
	Upstream  string
	Secret    string
	Transport http.RoundTripper
}

func (config Config) String() string {
	return "Provider credential broker configuration for " + config.Provider.String()
}

func (config Config) GoString() string {
	return config.String()
}

type Server struct {
	endpoint           string
	localAuthorization string
	httpServer         *http.Server
	listener           net.Listener
	transport          http.RoundTripper
	credential         *credentialState
	done               chan struct{}
	closeOnce          sync.Once
	stateMu            sync.RWMutex
	closeErr           error
}

type credentialState struct {
	mu       sync.RWMutex
	provider Provider
	secret   []byte
}

func Start(config Config) (*Server, error) {
	parsed, apiPath, err := validateConfig(config)
	if err != nil {
		return nil, err
	}
	localAuthorization, err := newLocalAuthorization()
	if err != nil {
		return nil, err
	}
	transport := config.Transport
	if transport == nil {
		transport = defaultTransport()
	}
	credential := &credentialState{
		provider: config.Provider,
		secret:   []byte(config.Secret),
	}
	handler := newHandler(
		parsed,
		apiPath,
		credential,
		localAuthorization,
		transport,
	)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("start Provider credential broker listener")
	}
	server := &Server{
		endpoint:           "http://" + listener.Addr().String() + invokePath,
		localAuthorization: localAuthorization,
		listener:           listener,
		transport:          transport,
		credential:         credential,
		done:               make(chan struct{}),
	}
	server.httpServer = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		_ = server.httpServer.Serve(listener)
		close(server.done)
	}()
	return server, nil
}

func (server *Server) Endpoint() string {
	if server == nil {
		return ""
	}
	return server.endpoint
}

func (server *Server) LocalAuthorization() string {
	if server == nil {
		return ""
	}
	server.stateMu.RLock()
	defer server.stateMu.RUnlock()
	return server.localAuthorization
}

func (server *Server) Close(ctx context.Context) error {
	if server == nil {
		return nil
	}
	server.closeOnce.Do(func() {
		closeErr := server.httpServer.Shutdown(ctx)
		if closer, ok := server.transport.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
		select {
		case <-server.done:
		case <-ctx.Done():
			if closeErr == nil {
				closeErr = ctx.Err()
			}
			_ = server.listener.Close()
		}
		server.credential.clear()
		server.stateMu.Lock()
		server.localAuthorization = ""
		server.closeErr = closeErr
		server.stateMu.Unlock()
	})
	server.stateMu.RLock()
	defer server.stateMu.RUnlock()
	return server.closeErr
}

func validateConfig(config Config) (*url.URL, string, error) {
	if config.Provider != ProviderOpenAI && config.Provider != ProviderAnthropic {
		return nil, "", errors.New("Provider credential broker type is invalid")
	}
	if strings.TrimSpace(config.Secret) == "" || len(config.Secret) > 16<<10 {
		return nil, "", errors.New("Provider credential broker secret is invalid")
	}
	parsed, err := url.Parse(strings.TrimSpace(config.Upstream))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, "", errors.New("Provider credential broker upstream must be an HTTPS base URL")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return nil, "", errors.New("Provider credential broker upstream port is not allowed")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if !validPublicProviderHost(host) {
		return nil, "", errors.New("Provider credential broker upstream host is invalid")
	}
	basePath := parsed.EscapedPath()
	if basePath == "" || basePath == "/" {
		basePath = "/v1"
	}
	if strings.Contains(strings.ToLower(basePath), "%2f") ||
		strings.Contains(strings.ToLower(basePath), "%5c") ||
		path.Clean(basePath) != strings.TrimSuffix(basePath, "/") {
		return nil, "", errors.New("Provider credential broker upstream path is invalid")
	}
	suffix := "responses"
	if config.Provider == ProviderAnthropic {
		suffix = "messages"
	}
	apiPath := path.Join(basePath, suffix)
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, apiPath, nil
}

func validPublicProviderHost(host string) bool {
	if host == "" || host == "localhost" || !strings.Contains(host, ".") ||
		strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") ||
		strings.ContainsAny(host, " /\\@") {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') ||
				(character >= '0' && character <= '9') ||
				character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func newLocalAuthorization() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("generate Provider credential broker authorization")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func newHandler(
	upstream *url.URL,
	apiPath string,
	credential *credentialState,
	localAuthorization string,
	transport http.RoundTripper,
) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = transport
	proxy.Director = func(request *http.Request) {
		request.URL.Scheme = upstream.Scheme
		request.URL.Host = upstream.Host
		request.URL.Path = apiPath
		request.URL.RawPath = ""
		request.URL.RawQuery = ""
		request.Host = upstream.Host
		sanitizeIncomingHeaders(request.Header)
		name, value, ok := credential.header()
		if ok {
			request.Header.Set(name, value)
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		sanitizeResponseHeaders(response.Header)
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(writer, "provider upstream unavailable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host == "" || request.URL.IsAbs() {
			http.Error(writer, "invalid provider request", http.StatusBadRequest)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != invokePath ||
			request.URL.RawQuery != "" {
			http.Error(writer, "provider request is not allowed", http.StatusNotFound)
			return
		}
		if !matchesLocalAuthorization(
			request.Header.Get("Authorization"),
			localAuthorization,
		) {
			http.Error(writer, "provider request is not authorized", http.StatusUnauthorized)
			return
		}
		if request.ContentLength > maxRequestBodyBytes {
			http.Error(writer, "provider request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
		if err != nil {
			http.Error(writer, "provider request is invalid", http.StatusBadRequest)
			return
		}
		if len(raw) > maxRequestBodyBytes {
			http.Error(writer, "provider request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(raw))
		request.ContentLength = int64(len(raw))
		proxy.ServeHTTP(writer, request)
	})
}

func (credential *credentialState) header() (string, string, bool) {
	if credential == nil {
		return "", "", false
	}
	credential.mu.RLock()
	defer credential.mu.RUnlock()
	if len(credential.secret) == 0 {
		return "", "", false
	}
	switch credential.provider {
	case ProviderOpenAI:
		return "Authorization", "Bearer " + string(credential.secret), true
	case ProviderAnthropic:
		return "x-api-key", string(credential.secret), true
	default:
		return "", "", false
	}
}

func (credential *credentialState) clear() {
	if credential == nil {
		return
	}
	credential.mu.Lock()
	defer credential.mu.Unlock()
	for index := range credential.secret {
		credential.secret[index] = 0
	}
	credential.secret = nil
}

func matchesLocalAuthorization(header, localAuthorization string) bool {
	expected := "Bearer " + localAuthorization
	if len(header) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

func sanitizeIncomingHeaders(header http.Header) {
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"x-api-key",
		"Cookie",
		"OpenAI-Organization",
		"OpenAI-Project",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"Forwarded",
	} {
		header.Del(name)
	}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-openlinker-") {
			header.Del(name)
		}
	}
}

func sanitizeResponseHeaders(header http.Header) {
	for _, name := range []string{
		"Alt-Svc",
		"NEL",
		"Report-To",
		"Set-Cookie",
		"Server",
	} {
		header.Del(name)
	}
	for name := range header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "cf-") ||
			strings.HasPrefix(lower, "x-openai-") ||
			strings.HasPrefix(lower, "anthropic-") {
			header.Del(name)
		}
	}
}

func defaultTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

func (provider Provider) String() string {
	return string(provider)
}

func (server *Server) String() string {
	if server == nil {
		return "Provider credential broker"
	}
	return fmt.Sprintf("Provider credential broker at %s", server.endpoint)
}

func (server *Server) GoString() string {
	return server.String()
}
