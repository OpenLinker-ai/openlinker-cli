package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDoHResolverRequiresPublicLiteralHTTPSOrigin(t *testing.T) {
	for _, raw := range []string{
		"http://1.1.1.1/dns-query",
		"https://resolver.example/dns-query",
		"https://127.0.0.1/dns-query",
		"https://1.1.1.1:8443/dns-query",
		"https://1.1.1.1/",
	} {
		if _, err := newDoHResolver(raw, nil); err == nil {
			t.Errorf("expected DoH endpoint rejection: %s", raw)
		}
	}
	if _, err := newDoHResolver("https://1.1.1.1/dns-query", nil); err != nil {
		t.Fatalf("valid DoH endpoint: %v", err)
	}
}

func TestDoHResolverReturnsAndCachesAddressRecords(t *testing.T) {
	resolver, err := newDoHResolver("https://1.1.1.1/dns-query", nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	resolver.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		body := `{"Status":0,"Answer":[]}`
		if request.URL.Query().Get("type") == "A" {
			body = `{"Status":0,"Answer":[{"type":1,"TTL":60,"data":"93.184.216.34"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	first, err := resolver.LookupIP(context.Background(), "Example.COM.")
	if err != nil || len(first) != 1 || !first[0].Equal(net.ParseIP("93.184.216.34")) {
		t.Fatalf("first lookup = %v, %v", first, err)
	}
	second, err := resolver.LookupIP(context.Background(), "example.com")
	if err != nil || len(second) != 1 || calls != 2 {
		t.Fatalf("cached lookup = %v, calls=%d, err=%v", second, calls, err)
	}
	second[0][0] ^= 0xff
	third, _ := resolver.LookupIP(context.Background(), "example.com")
	if !third[0].Equal(net.ParseIP("93.184.216.34")) {
		t.Fatal("cached IP slice was returned without cloning")
	}
}

func TestGatewayRejectsPrivateDoHAnswer(t *testing.T) {
	g := &gateway{
		resolver: net.DefaultResolver,
		publicLookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
		dialer: &net.Dialer{},
		logger: log.New(io.Discard, "", 0),
	}
	if _, err := g.resolvePublic(context.Background(), "attacker.example"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private secure DNS answer was accepted: %v", err)
	}
}

func TestUpstreamProxyURLCanBePassedToDoHTransport(t *testing.T) {
	upstream, _ := url.Parse("http://proxy.example:8080")
	if _, err := newDoHResolver("https://1.1.1.1/dns-query", upstream); err != nil {
		t.Fatal(err)
	}
}
