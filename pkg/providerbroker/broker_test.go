package providerbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBrokerPinsOpenAIOriginPathAndCredential(t *testing.T) {
	t.Parallel()
	var observed struct {
		path          string
		authorization string
		apiKey        string
		cookie        string
		query         string
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		observed.path = request.URL.Path
		observed.authorization = request.Header.Get("Authorization")
		observed.apiKey = request.Header.Get("x-api-key")
		observed.cookie = request.Header.Get("Cookie")
		observed.query = request.URL.RawQuery
		writer.Header().Set("Alt-Svc", `h3=":443"`)
		writer.Header().Set("Set-Cookie", "upstream=session")
		writer.Header().Set("X-OpenAI-Request-ID", "provider-id")
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}))
	defer upstream.Close()
	server := startTestBroker(t, Config{
		Provider:  ProviderOpenAI,
		Upstream:  "https://openai.provider.example/custom/v1",
		Secret:    "real-provider-secret",
		Transport: testTransport(upstream),
	})

	request, err := http.NewRequest(
		http.MethodPost,
		server.Endpoint(),
		strings.NewReader(`{"model":"fixture"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+server.LocalAuthorization())
	request.Header.Set("x-api-key", "caller-controlled")
	request.Header.Set("Cookie", "caller=session")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if observed.path != "/custom/v1/responses" ||
		observed.authorization != "Bearer real-provider-secret" ||
		observed.apiKey != "" ||
		observed.cookie != "" ||
		observed.query != "" {
		t.Fatalf("upstream observation = %#v", observed)
	}
	for _, name := range []string{
		"Alt-Svc", "Set-Cookie", "X-OpenAI-Request-ID",
	} {
		if value := response.Header.Get(name); value != "" {
			t.Fatalf("response retained %s=%q", name, value)
		}
	}
}

func TestBrokerUsesIndependentAnthropicHeaderPolicy(t *testing.T) {
	t.Parallel()
	var authorization, apiKey, version, beta string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		authorization = request.Header.Get("Authorization")
		apiKey = request.Header.Get("x-api-key")
		version = request.Header.Get("anthropic-version")
		beta = request.Header.Get("anthropic-beta")
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}))
	defer upstream.Close()
	server := startTestBroker(t, Config{
		Provider:  ProviderAnthropic,
		Upstream:  "https://anthropic.provider.example",
		Secret:    "real-anthropic-secret",
		Transport: testTransport(upstream),
	})
	request, err := http.NewRequest(
		http.MethodPost,
		server.Endpoint(),
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+server.LocalAuthorization())
	request.Header.Set("x-api-key", "caller-controlled")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("anthropic-beta", "computer-use-fixture")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK ||
		authorization != "" ||
		apiKey != "real-anthropic-secret" ||
		version != "2023-06-01" ||
		beta != "computer-use-fixture" {
		t.Fatalf(
			"status=%d authorization=%q apiKey=%q version=%q beta=%q",
			response.StatusCode, authorization, apiKey, version, beta,
		)
	}
}

func TestBrokerRejectsUnauthorizedMethodPathQueryAndOversize(t *testing.T) {
	t.Parallel()
	var calls int
	var mu sync.Mutex
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		mu.Lock()
		calls++
		mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	server := startTestBroker(t, Config{
		Provider:  ProviderOpenAI,
		Upstream:  "https://openai.provider.example",
		Secret:    "real-secret",
		Transport: testTransport(upstream),
	})
	tests := []struct {
		name   string
		method string
		url    string
		auth   string
		body   io.Reader
		length int64
		status int
	}{
		{
			name: "wrong authorization", method: http.MethodPost,
			url: server.Endpoint(), auth: "Bearer caller", body: strings.NewReader(`{}`),
			status: http.StatusUnauthorized,
		},
		{
			name: "wrong method", method: http.MethodGet,
			url: server.Endpoint(), auth: "Bearer " + server.LocalAuthorization(),
			status: http.StatusNotFound,
		},
		{
			name: "wrong path", method: http.MethodPost,
			url:  strings.TrimSuffix(server.Endpoint(), invokePath) + "/models",
			auth: "Bearer " + server.LocalAuthorization(), body: strings.NewReader(`{}`),
			status: http.StatusNotFound,
		},
		{
			name: "query", method: http.MethodPost,
			url:  server.Endpoint() + "?target=other",
			auth: "Bearer " + server.LocalAuthorization(), body: strings.NewReader(`{}`),
			status: http.StatusNotFound,
		},
		{
			name: "oversize", method: http.MethodPost,
			url: server.Endpoint(), auth: "Bearer " + server.LocalAuthorization(),
			body:   io.LimitReader(zeroReader{}, maxRequestBodyBytes+1),
			length: maxRequestBodyBytes + 1,
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name: "oversize chunked", method: http.MethodPost,
			url: server.Endpoint(), auth: "Bearer " + server.LocalAuthorization(),
			body:   io.LimitReader(zeroReader{}, maxRequestBodyBytes+1),
			status: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, test.url, test.body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", test.auth)
			if test.length > 0 {
				request.ContentLength = test.length
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("rejected requests reached upstream %d times", calls)
	}
}

func TestBrokerConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []Config{
		{Provider: "other", Upstream: "https://api.example.com", Secret: "secret"},
		{Provider: ProviderOpenAI, Upstream: "http://api.example.com", Secret: "secret"},
		{Provider: ProviderOpenAI, Upstream: "https://user:pass@api.example.com", Secret: "secret"},
		{Provider: ProviderOpenAI, Upstream: "https://127.0.0.1", Secret: "secret"},
		{Provider: ProviderOpenAI, Upstream: "https://8.8.8.8", Secret: "secret"},
		{Provider: ProviderOpenAI, Upstream: "https://api.example.com:8443", Secret: "secret"},
		{Provider: ProviderOpenAI, Upstream: "https://api.example.com?target=other", Secret: "secret"},
		{Provider: ProviderOpenAI, Upstream: "https://api.example.com", Secret: ""},
	}
	for _, config := range tests {
		if _, err := Start(config); err == nil {
			t.Fatalf("Start(%#v) succeeded", redactedConfig(config))
		}
	}
}

func TestBrokerDrainStopsNewRequestsAndClearsLocalAuthorization(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_ = json.NewEncoder(writer).Encode(map[string]bool{"ok": true})
	}))
	defer upstream.Close()
	server := startTestBroker(t, Config{
		Provider:  ProviderOpenAI,
		Upstream:  "https://openai.provider.example",
		Secret:    "real-secret",
		Transport: testTransport(upstream),
	})
	endpoint := server.Endpoint()
	authorization := server.LocalAuthorization()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if server.LocalAuthorization() != "" {
		t.Fatal("closed broker retained local authorization")
	}
	if len(server.credential.secret) != 0 {
		t.Fatal("closed broker retained Provider credential bytes")
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+authorization)
	if _, err := http.DefaultClient.Do(request); err == nil {
		t.Fatal("closed broker accepted a new request")
	}
}

func TestBrokerFormattingRedactsRealAndLocalCredentials(t *testing.T) {
	t.Parallel()
	const secret = "real-provider-secret-sentinel"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	config := Config{
		Provider:  ProviderOpenAI,
		Upstream:  "https://openai.provider.example",
		Secret:    secret,
		Transport: testTransport(upstream),
	}
	server := startTestBroker(t, config)
	for name, value := range map[string]string{
		"config": fmt.Sprintf("%#v", config),
		"server": fmt.Sprintf("%#v", server),
	} {
		if strings.Contains(value, secret) ||
			strings.Contains(value, server.LocalAuthorization()) {
			t.Fatalf("%s formatting exposed a credential: %s", name, value)
		}
	}
}

func startTestBroker(t *testing.T, config Config) *Server {
	t.Helper()
	server, err := Start(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if !strings.HasPrefix(server.Endpoint(), "http://127.0.0.1:") ||
		!strings.HasSuffix(server.Endpoint(), invokePath) ||
		len(server.LocalAuthorization()) < 32 {
		t.Fatalf(
			"endpoint=%q local authorization length=%d",
			server.Endpoint(), len(server.LocalAuthorization()),
		)
	}
	return server
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

func redactedConfig(config Config) Config {
	config.Secret = "<redacted>"
	return config
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return function(request)
}

func testTransport(upstream *httptest.Server) http.RoundTripper {
	target := upstream.Client().Transport
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clonedURL := *request.URL
		clonedURL.Scheme = "https"
		clonedURL.Host = strings.TrimPrefix(upstream.URL, "https://")
		clone.URL = &clonedURL
		return target.RoundTrip(clone)
	})
}
