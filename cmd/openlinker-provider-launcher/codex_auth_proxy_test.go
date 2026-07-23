//go:build linux

package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

func TestCreateCodexProxyCertificateBuildsTrustedLoopbackBundle(t *testing.T) {
	certFile, keyFile, bundleFile, cleanup, err := createCodexProxyCertificate()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(bundleFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		t.Fatal("CA bundle did not contain a certificate")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{
		DNSName:   "127.0.0.1",
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("loopback certificate did not verify: %v", err)
	}
	keyInfo, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("TLS key mode = %o", keyInfo.Mode().Perm())
	}
	cleanup()
	if _, err := os.Stat(bundleFile); !os.IsNotExist(err) {
		t.Fatalf("TLS cleanup did not remove the bundle: %v", err)
	}
}
