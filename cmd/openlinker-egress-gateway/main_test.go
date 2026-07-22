package main

import (
	"bytes"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlockedIPRejectsPrivateReservedAndMetadataRanges(t *testing.T) {
	blocked := []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.168.1.1", "192.0.0.1", "192.0.2.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "224.0.0.1", "255.255.255.255",
		"192.88.99.1", "::", "::1", "64:ff9b::a00:1", "64:ff9b:1::1", "100::1",
		"2001::1", "2001:db8::1", "2002::1", "3fff::1", "5f00::1", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1",
	}
	for _, raw := range blocked {
		if !blockedIP(net.ParseIP(raw)) {
			t.Errorf("expected %s to be blocked", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if blockedIP(net.ParseIP(raw)) {
			t.Errorf("expected %s to remain public", raw)
		}
	}
}

func TestValidPublicHostnameRejectsInternalNames(t *testing.T) {
	for _, host := range []string{"localhost", "gateway", "service.internal", "printer.local", "metadata.google.internal", "bad host.example"} {
		if validPublicHostname(host) {
			t.Errorf("expected %q to be rejected", host)
		}
	}
	for _, host := range []string{"api.openai.com", "api.anthropic.com", "1.1.1.1"} {
		if !validPublicHostname(host) {
			t.Errorf("expected %q to be accepted as a hostname", host)
		}
	}
}

func TestGatewayRejectsPrivateHTTPAndConnectWithoutLeakingURL(t *testing.T) {
	var logs bytes.Buffer
	g := &gateway{resolver: net.DefaultResolver, dialer: &net.Dialer{}, logger: log.New(&logs, "", 0)}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/private?token=secret", nil)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("private HTTP status = %d", rec.Code)
	}
	if strings.Contains(logs.String(), "token") || strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "/private") {
		t.Fatalf("gateway log leaked URL data: %s", logs.String())
	}

	req = httptest.NewRequest(http.MethodConnect, "http://gateway.invalid", nil)
	req.Host = "169.254.169.254:443"
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("private CONNECT status = %d", rec.Code)
	}
}

func TestResolveSecretSupportsDirectOrOwnerOnlyFile(t *testing.T) {
	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY", "http://proxy.example:8080")
	value, err := resolveSecret("OPENLINKER_EGRESS_UPSTREAM_PROXY")
	if err != nil || value != "http://proxy.example:8080" {
		t.Fatalf("direct secret = %q, %v", value, err)
	}

	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY", "")
	path := filepath.Join(t.TempDir(), "proxy")
	if err := os.WriteFile(path, []byte("https://proxy.example:8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY_FILE", path)
	value, err = resolveSecret("OPENLINKER_EGRESS_UPSTREAM_PROXY")
	if err != nil || value != "https://proxy.example:8443" {
		t.Fatalf("file secret = %q, %v", value, err)
	}

	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY", "http://second.example:8080")
	if _, err := resolveSecret("OPENLINKER_EGRESS_UPSTREAM_PROXY"); err == nil {
		t.Fatal("expected dual-source rejection")
	}
}
