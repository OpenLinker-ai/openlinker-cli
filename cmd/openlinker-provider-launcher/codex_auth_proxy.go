//go:build linux

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
)

const (
	codexAuthProxyStageEnv    = "OPENLINKER_CODEX_AUTH_PROXY_STAGE"
	codexAuthProxyUpstreamEnv = "OPENLINKER_CODEX_AUTH_PROXY_UPSTREAM"
	codexAuthProxySecretEnv   = "OPENLINKER_CODEX_AUTH_PROXY_SECRET"
	codexAuthProxyCertEnv     = "OPENLINKER_CODEX_AUTH_PROXY_CERT"
	codexAuthProxyKeyEnv      = "OPENLINKER_CODEX_AUTH_PROXY_KEY"
	codexLocalAuthEnv         = "OPENLINKER_LOCAL_CODEX_AUTH"
	providerExecStageEnv      = "OPENLINKER_PROVIDER_EXEC_STAGE"
)

type codexProxyLaunch struct {
	argv        []string
	environment []string
	proxy       *exec.Cmd
	cleanup     func()
}

func prepareCodexAuthProxy(argv, environment []string) (*codexProxyLaunch, error) {
	upstream, ok := codexProviderArgument(argv, "model_providers.openlinker_proxy.base_url")
	if !ok {
		return nil, nil
	}
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("Codex custom Provider Base URL is invalid")
	}
	secret := environmentValue(environment, "CODEX_API_KEY")
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("CODEX_API_KEY is required for the Codex custom Provider")
	}
	certFile, keyFile, bundleFile, cleanup, err := createCodexProxyCertificate()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cleanup()
		return nil, errors.New("start local Codex credential proxy listener")
	}
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		_ = listener.Close()
		cleanup()
		return nil, errors.New("local Codex credential proxy listener is not TCP")
	}
	listenerAddress := tcpListener.Addr().String()
	listenerFile, err := tcpListener.File()
	_ = listener.Close()
	if err != nil {
		cleanup()
		return nil, errors.New("prepare local Codex credential proxy listener")
	}
	executable, err := os.Executable()
	if err != nil {
		_ = listenerFile.Close()
		cleanup()
		return nil, errors.New("resolve Provider launcher executable")
	}
	proxyCommand := exec.Command(executable)
	proxyCommand.ExtraFiles = []*os.File{listenerFile}
	proxyCommand.Env = codexAuthProxyEnvironment(environment, upstream, secret, certFile, keyFile)
	proxyCommand.Stdin = nil
	proxyCommand.Stdout = nil
	proxyCommand.Stderr = os.Stderr
	proxyCommand.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := proxyCommand.Start(); err != nil {
		_ = listenerFile.Close()
		cleanup()
		return nil, errors.New("start local Codex credential proxy")
	}
	_ = listenerFile.Close()

	localBaseURL := "https://" + listenerAddress
	rewritten := rewriteCodexProviderArguments(argv, localBaseURL, codexLocalAuthEnv)
	providerEnvironment := setEnvironmentValue(codexProviderEnvironment(environment), "SSL_CERT_FILE", bundleFile)
	providerEnvironment = setEnvironmentValue(providerEnvironment, "CODEX_CA_CERTIFICATE", bundleFile)
	providerEnvironment = setEnvironmentValue(providerEnvironment, "NO_PROXY", "127.0.0.1,localhost")
	providerEnvironment = setEnvironmentValue(providerEnvironment, "no_proxy", "127.0.0.1,localhost")
	return &codexProxyLaunch{
		argv: rewritten, environment: providerEnvironment, proxy: proxyCommand, cleanup: cleanup,
	}, nil
}

func runCodexWithAuthProxy(launch *codexProxyLaunch) int {
	defer launch.cleanup()
	executable, err := os.Executable()
	if err != nil {
		stopProxyCommand(launch.proxy)
		return 1
	}
	command := exec.Command(executable, launch.argv[1:]...)
	command.Env = append(removeEnvironment(launch.environment, providerExecStageEnv), providerExecStageEnv+"=1")
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	err = command.Run()
	stopProxyCommand(launch.proxy)
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Exited() {
			return status.ExitStatus()
		}
	}
	return 1
}

func stopProxyCommand(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
}

func codexProviderArgument(argv []string, key string) (string, bool) {
	prefix := key + "="
	for _, argument := range argv {
		if !strings.HasPrefix(argument, prefix) {
			continue
		}
		value := strings.TrimPrefix(argument, prefix)
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", false
		}
		return decoded, true
	}
	return "", false
}

func rewriteCodexProviderArguments(argv []string, localBaseURL, authEnv string) []string {
	rewritten := append([]string(nil), argv...)
	for index, argument := range rewritten {
		switch {
		case strings.HasPrefix(argument, "model_providers.openlinker_proxy.base_url="):
			rewritten[index] = fmt.Sprintf("model_providers.openlinker_proxy.base_url=%q", localBaseURL)
		case strings.HasPrefix(argument, "model_providers.openlinker_proxy.env_key="):
			rewritten[index] = fmt.Sprintf("model_providers.openlinker_proxy.env_key=%q", authEnv)
		}
	}
	return rewritten
}

func codexProviderEnvironment(environment []string) []string {
	clean := removeEnvironment(environment,
		"CODEX_API_KEY",
		"CODEX_API_KEY_FILE",
		"OPENLINKER_AGENT_TOKEN",
		"OPENLINKER_AGENT_TOKEN_FILE",
		codexAuthProxyStageEnv,
		codexAuthProxyUpstreamEnv,
		codexAuthProxySecretEnv,
		codexAuthProxyCertEnv,
		codexAuthProxyKeyEnv,
		"CODEX_CA_CERTIFICATE",
		codexLocalAuthEnv,
		providerExecStageEnv,
	)
	return append(clean, codexLocalAuthEnv+"=local-credential-proxy")
}

func codexAuthProxyEnvironment(environment []string, upstream, secret, certFile, keyFile string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "HTTP_PROXY": true, "HTTPS_PROXY": true,
		"http_proxy": true, "https_proxy": true, "NO_PROXY": true, "no_proxy": true,
		"SSL_CERT_FILE": true,
	}
	clean := make([]string, 0, len(environment)+3)
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok && allowed[key] {
			clean = append(clean, item)
		}
	}
	return append(clean,
		codexAuthProxyStageEnv+"=1",
		codexAuthProxyUpstreamEnv+"="+upstream,
		codexAuthProxySecretEnv+"="+secret,
		codexAuthProxyCertEnv+"="+certFile,
		codexAuthProxyKeyEnv+"="+keyFile,
	)
}

func setEnvironmentValue(environment []string, name, value string) []string {
	clean := removeEnvironment(environment, name)
	return append(clean, name+"="+value)
}

func removeEnvironment(environment []string, keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
	}
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok && blocked[key] {
			continue
		}
		clean = append(clean, item)
	}
	return clean
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func runCodexAuthProxyStage() error {
	if os.Geteuid() != runtimeUID || os.Getegid() != runtimeGID {
		return errors.New("Codex credential proxy must run as the fixed Runtime UID/GID")
	}
	upstreamValue := strings.TrimSpace(os.Getenv(codexAuthProxyUpstreamEnv))
	secret := strings.TrimSpace(os.Getenv(codexAuthProxySecretEnv))
	certFile := strings.TrimSpace(os.Getenv(codexAuthProxyCertEnv))
	keyFile := strings.TrimSpace(os.Getenv(codexAuthProxyKeyEnv))
	if upstreamValue == "" || secret == "" || certFile == "" || keyFile == "" {
		return errors.New("Codex credential proxy configuration is incomplete")
	}
	upstream, err := url.Parse(upstreamValue)
	if err != nil || upstream.Host == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") {
		return errors.New("Codex credential proxy upstream is invalid")
	}
	listenerFile := os.NewFile(3, "codex-auth-proxy-listener")
	if listenerFile == nil {
		return errors.New("Codex credential proxy listener is missing")
	}
	listener, err := net.FileListener(listenerFile)
	_ = listenerFile.Close()
	if err != nil {
		return errors.New("open Codex credential proxy listener")
	}
	defer listener.Close()
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return errors.New("load Codex credential proxy certificate")
	}
	server := &http.Server{
		Handler:           newCodexAuthProxyHandler(upstream, secret),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		},
	}
	if err := http2.ConfigureServer(server, &http2.Server{}); err != nil {
		return errors.New("configure Codex credential proxy HTTP/2")
	}
	tlsListener := tls.NewListener(listener, server.TLSConfig)
	err = server.Serve(tlsListener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func newCodexAuthProxyHandler(upstream *url.URL, secret string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = upstream.Host
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Del("Proxy-Authorization")
	}
	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Body = newResponseCompletionObserver(response.Body, response.StatusCode)
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
		proxy.ServeHTTP(writer, request)
	})
}

type responseCompletionObserver struct {
	body       io.ReadCloser
	statusCode int
	bytesRead  int64
	tail       []byte
	completed  bool
	finished   bool
}

var responseCompletedMarker = []byte(`"type":"response.completed"`)

func newResponseCompletionObserver(body io.ReadCloser, statusCode int) io.ReadCloser {
	return &responseCompletionObserver{body: body, statusCode: statusCode}
}

func (observer *responseCompletionObserver) Read(buffer []byte) (int, error) {
	count, err := observer.body.Read(buffer)
	observer.bytesRead += int64(count)
	if count > 0 && !observer.completed {
		window := append(append([]byte(nil), observer.tail...), buffer[:count]...)
		observer.completed = bytes.Contains(window, responseCompletedMarker)
		keep := len(responseCompletedMarker) - 1
		if len(window) > keep {
			window = window[len(window)-keep:]
		}
		observer.tail = window
	}
	if err != nil {
		observer.finish(err)
	}
	return count, err
}

func (observer *responseCompletionObserver) Close() error {
	observer.finish(errors.New("body closed"))
	return observer.body.Close()
}

func (observer *responseCompletionObserver) finish(err error) {
	if observer.finished {
		return
	}
	observer.finished = true
	if observer.completed {
		return
	}
	errLabel := "none"
	if err != nil {
		errLabel = strings.ReplaceAll(err.Error(), "\n", " ")
	}
	fmt.Fprintf(os.Stderr,
		"openlinker Codex credential proxy: incomplete upstream stream status=%d bytes=%d completed=%t error=%s\n",
		observer.statusCode, observer.bytesRead, observer.completed, errLabel,
	)
}

func createCodexProxyCertificate() (string, string, string, func(), error) {
	directory, err := os.MkdirTemp("/tmp", "openlinker-codex-proxy-*")
	if err != nil {
		return "", "", "", nil, errors.New("create Codex credential proxy TLS directory")
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := os.Chmod(directory, 0o755); err != nil {
		cleanup()
		return "", "", "", nil, errors.New("protect Codex credential proxy TLS directory")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		cleanup()
		return "", "", "", nil, errors.New("generate Codex credential proxy TLS key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		cleanup()
		return "", "", "", nil, errors.New("generate Codex credential proxy TLS serial")
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "OpenLinker local Codex credential proxy"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		cleanup()
		return "", "", "", nil, errors.New("create Codex credential proxy TLS certificate")
	}
	privateDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		cleanup()
		return "", "", "", nil, errors.New("encode Codex credential proxy TLS key")
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER})
	certFile := filepath.Join(directory, "server.crt")
	keyFile := filepath.Join(directory, "server.key")
	bundleFile := filepath.Join(directory, "ca-bundle.crt")
	systemBundle, err := os.ReadFile("/etc/ssl/certs/ca-certificates.crt")
	if err != nil {
		cleanup()
		return "", "", "", nil, errors.New("read system CA bundle")
	}
	if err := os.WriteFile(certFile, certificatePEM, 0o644); err != nil {
		cleanup()
		return "", "", "", nil, errors.New("write Codex credential proxy TLS certificate")
	}
	if err := os.WriteFile(keyFile, privatePEM, 0o600); err != nil {
		cleanup()
		return "", "", "", nil, errors.New("write Codex credential proxy TLS key")
	}
	bundle := append(append([]byte(nil), systemBundle...), '\n')
	bundle = append(bundle, certificatePEM...)
	if err := os.WriteFile(bundleFile, bundle, 0o644); err != nil {
		cleanup()
		return "", "", "", nil, errors.New("write Codex credential proxy CA bundle")
	}
	return certFile, keyFile, bundleFile, cleanup, nil
}
