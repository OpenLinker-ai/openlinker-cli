//go:build linux

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRewriteCodexProviderArgumentsUsesLocalCredentialProxy(t *testing.T) {
	argv := []string{
		"codex",
		"-c", `model_providers.openlinker_proxy.base_url="https://router.example/v1"`,
		"-c", `model_providers.openlinker_proxy.env_key="CODEX_API_KEY"`,
		"exec",
	}
	rewritten := rewriteCodexProviderArguments(argv, "http://127.0.0.1:43129", codexLocalAuthEnv)
	joined := strings.Join(rewritten, " ")
	for _, expected := range []string{
		`model_providers.openlinker_proxy.base_url="http://127.0.0.1:43129"`,
		`model_providers.openlinker_proxy.env_key="OPENLINKER_LOCAL_CODEX_AUTH"`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("rewritten args do not include %s: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "router.example") || strings.Contains(joined, `env_key="CODEX_API_KEY"`) {
		t.Fatalf("rewritten args retain upstream credential details: %s", joined)
	}
}

func TestCodexProviderEnvironmentDoesNotExposeProviderSecret(t *testing.T) {
	environment := codexProviderEnvironment([]string{
		"PATH=/usr/bin",
		"CODEX_API_KEY=provider-secret",
		"OPENLINKER_AGENT_TOKEN=agent-secret",
		codexAuthProxySecretEnv + "=stale-secret",
	})
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "provider-secret") || strings.Contains(joined, "agent-secret") || strings.Contains(joined, "stale-secret") {
		t.Fatalf("Codex environment exposes an upstream secret: %s", joined)
	}
	if !strings.Contains(joined, codexLocalAuthEnv+"=local-credential-proxy") {
		t.Fatalf("Codex environment is missing the non-secret local credential: %s", joined)
	}
}

func TestCodexAuthProxyPinsUpstreamAndInjectsCredential(t *testing.T) {
	var observedPath, observedAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedPath = request.URL.Path
		observedAuthorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newCodexAuthProxyHandler(target, "provider-secret"))
	defer proxy.Close()

	request, err := http.NewRequest(http.MethodPost, proxy.URL+"/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer caller-controlled")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", response.StatusCode)
	}
	if observedPath != "/v1/responses" {
		t.Fatalf("upstream path = %q", observedPath)
	}
	if observedAuthorization != "Bearer provider-secret" {
		t.Fatalf("upstream Authorization was not replaced")
	}
}
