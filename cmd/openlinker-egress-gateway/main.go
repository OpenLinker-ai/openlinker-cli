package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const gatewayVersion = "0.1.0"
const maxGatewaySecretBytes = 64 << 10

var blockedDestinationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

type gateway struct {
	resolver     *net.Resolver
	publicLookup func(context.Context, string) ([]net.IP, error)
	dialer       *net.Dialer
	upstream     *url.URL
	logger       *log.Logger
}

func main() {
	logger := log.New(os.Stderr, "openlinker-egress ", log.LstdFlags)
	upstreamRaw, err := resolveSecret("OPENLINKER_EGRESS_UPSTREAM_PROXY")
	if err != nil {
		logger.Fatal(err)
	}
	var upstream *url.URL
	if upstreamRaw != "" {
		upstream, err = parseUpstreamProxy(upstreamRaw)
		if err != nil {
			logger.Fatal(err)
		}
	}
	dohRaw := strings.TrimSpace(os.Getenv("OPENLINKER_EGRESS_DOH_URL"))
	if dohRaw == "" {
		dohRaw = "https://1.1.1.1/dns-query"
	}
	doh, err := newDoHResolver(dohRaw, upstream)
	if err != nil {
		logger.Fatal(err)
	}
	handler := &gateway{
		resolver:     net.DefaultResolver,
		publicLookup: doh.LookupIP,
		dialer:       &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		upstream:     upstream,
		logger:       logger,
	}
	listen := strings.TrimSpace(os.Getenv("OPENLINKER_EGRESS_LISTEN"))
	if listen == "" {
		listen = "0.0.0.0:3128"
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Printf("version=%s listen=%s upstream=%t secure_dns=%s", gatewayVersion, listen, upstream != nil, doh.EndpointHost())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		g.serveConnect(w, r)
		return
	}
	if r.URL == nil || r.URL.Scheme == "" || r.URL.Host == "" || r.URL.User != nil {
		http.Error(w, "absolute public URL required", http.StatusBadRequest)
		return
	}
	if r.URL.Scheme != "http" {
		http.Error(w, "HTTPS proxy requests must use CONNECT", http.StatusBadRequest)
		return
	}
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	if err := validatePort(port, false); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	addresses, err := g.resolvePublic(r.Context(), r.URL.Hostname())
	if err != nil {
		g.logger.Printf("blocked method=%s host=%s", r.Method, safeHost(r.URL.Hostname()))
		http.Error(w, "private or invalid destination blocked", http.StatusForbidden)
		return
	}

	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""
	// Pin the actual TCP destination to the address that passed validation. The
	// original authority remains in Host, so HTTP virtual hosting still works.
	// This avoids a second DNS lookup in either the local transport or an
	// operator upstream proxy and closes DNS-rebinding/TOCTOU bypasses.
	outbound.Host = r.URL.Host
	outbound.URL.Host = net.JoinHostPort(addresses[0].String(), port)
	removeHopHeaders(outbound.Header)
	outbound.Header.Del("Proxy-Authorization")
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(g.upstream),
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		http.Error(w, "public destination unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	removeHopHeaders(response.Header)
	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (g *gateway) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "CONNECT requires host:port", http.StatusBadRequest)
		return
	}
	if err := validatePort(port, true); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	addresses, err := g.resolvePublic(r.Context(), host)
	if err != nil {
		g.logger.Printf("blocked method=CONNECT host=%s", safeHost(host))
		http.Error(w, "private or invalid destination blocked", http.StatusForbidden)
		return
	}
	target := net.JoinHostPort(addresses[0].String(), port)

	var upstreamConn net.Conn
	if g.upstream == nil {
		upstreamConn, err = g.dialer.DialContext(r.Context(), "tcp", target)
	} else {
		upstreamConn, err = g.connectThroughUpstream(r.Context(), target)
	}
	if err != nil {
		http.Error(w, "public destination unavailable", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		http.Error(w, "tunneling unavailable", http.StatusInternalServerError)
		return
	}
	clientConn, buffered, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		return
	}
	_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buffered.Flush()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstreamConn, clientConn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(clientConn, upstreamConn); done <- struct{}{} }()
	<-done
	_ = clientConn.Close()
	_ = upstreamConn.Close()
}

func (g *gateway) resolvePublic(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if !validPublicHostname(host) {
		return nil, errors.New("invalid public hostname")
	}
	if literal := net.ParseIP(host); literal != nil {
		if blockedIP(literal) {
			return nil, errors.New("private address")
		}
		return []net.IP{literal}, nil
	}
	var resolved []net.IP
	var err error
	if g.publicLookup != nil {
		resolved, err = g.publicLookup(ctx, host)
	} else {
		resolved, err = g.resolver.LookupIP(ctx, "ip", host)
	}
	if err != nil || len(resolved) == 0 {
		return nil, errors.New("DNS resolution failed")
	}
	for _, address := range resolved {
		if blockedIP(address) {
			return nil, errors.New("DNS resolved to private address")
		}
	}
	return resolved, nil
}

func validPublicHostname(host string) bool {
	if host == "" || strings.ContainsAny(host, " /\\@") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	lower := strings.ToLower(host)
	if !strings.Contains(lower, ".") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".localhost") || lower == "metadata.google.internal" {
		return false
	}
	return true
}

func blockedIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedDestinationPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func validatePort(raw string, secure bool) error {
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("invalid destination port")
	}
	if secure && port != 443 {
		return errors.New("CONNECT is restricted to port 443")
	}
	if !secure && port != 80 {
		return errors.New("plain HTTP is restricted to port 80")
	}
	return nil
}

func parseUpstreamProxy(raw string) (*url.URL, error) {
	proxyURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || proxyURL.Hostname() == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
		return nil, errors.New("OPENLINKER_EGRESS_UPSTREAM_PROXY must be an HTTP or HTTPS proxy URL")
	}
	return proxyURL, nil
}

func (g *gateway) connectThroughUpstream(ctx context.Context, target string) (net.Conn, error) {
	port := g.upstream.Port()
	if port == "" {
		port = map[bool]string{true: "443", false: "80"}[g.upstream.Scheme == "https"]
	}
	conn, err := g.dialer.DialContext(ctx, "tcp", net.JoinHostPort(g.upstream.Hostname(), port))
	if err != nil {
		return nil, err
	}
	if g.upstream.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: g.upstream.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if g.upstream.User != nil {
		password, _ := g.upstream.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(g.upstream.User.Username() + ":" + password))
		request.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	if err := request.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("upstream proxy CONNECT failed")
	}
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func resolveSecret(name string) (string, error) {
	direct := strings.TrimSpace(os.Getenv(name))
	file := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if direct != "" && file != "" {
		return "", fmt.Errorf("%s and %s_FILE are mutually exclusive", name, name)
	}
	if file == "" {
		return direct, nil
	}
	info, err := os.Lstat(file)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maxGatewaySecretBytes {
		return "", fmt.Errorf("%s_FILE must be an owner-only regular non-symlink file", name)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("%s_FILE must be owned by the current process user", name)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE", name)
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s_FILE is empty", name)
	}
	return value, nil
}

func removeHopHeaders(header http.Header) {
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(key)
	}
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func safeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if len(host) > 200 {
		return host[:200]
	}
	return host
}
