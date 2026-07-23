//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	codexAuthProxyStageEnv    = "OPENLINKER_CODEX_AUTH_PROXY_STAGE"
	codexAuthProxyUpstreamEnv = "OPENLINKER_CODEX_AUTH_PROXY_UPSTREAM"
	codexAuthProxySecretEnv   = "OPENLINKER_CODEX_AUTH_PROXY_SECRET"
	codexLocalAuthEnv         = "OPENLINKER_LOCAL_CODEX_AUTH"
)

func prepareCodexAuthProxy(argv, environment []string) ([]string, []string, error) {
	upstream, ok := codexProviderArgument(argv, "model_providers.openlinker_proxy.base_url")
	if !ok {
		return argv, environment, nil
	}
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, nil, errors.New("Codex custom Provider Base URL is invalid")
	}
	secret := environmentValue(environment, "CODEX_API_KEY")
	if strings.TrimSpace(secret) == "" {
		return nil, nil, errors.New("CODEX_API_KEY is required for the Codex custom Provider")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, errors.New("start local Codex credential proxy listener")
	}
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		_ = listener.Close()
		return nil, nil, errors.New("local Codex credential proxy listener is not TCP")
	}
	listenerFile, err := tcpListener.File()
	_ = listener.Close()
	if err != nil {
		return nil, nil, errors.New("prepare local Codex credential proxy listener")
	}
	executable, err := os.Executable()
	if err != nil {
		_ = listenerFile.Close()
		return nil, nil, errors.New("resolve Provider launcher executable")
	}
	proxyCommand := exec.Command(executable)
	proxyCommand.ExtraFiles = []*os.File{listenerFile}
	proxyCommand.Env = codexAuthProxyEnvironment(environment, upstream, secret)
	proxyCommand.Stdin = nil
	proxyCommand.Stdout = nil
	proxyCommand.Stderr = os.Stderr
	proxyCommand.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := proxyCommand.Start(); err != nil {
		_ = listenerFile.Close()
		return nil, nil, errors.New("start local Codex credential proxy")
	}
	_ = listenerFile.Close()

	localBaseURL := "http://" + tcpListener.Addr().String()
	rewritten := rewriteCodexProviderArguments(argv, localBaseURL, codexLocalAuthEnv)
	return rewritten, codexProviderEnvironment(environment), nil
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
		codexLocalAuthEnv,
	)
	return append(clean, codexLocalAuthEnv+"=local-credential-proxy")
}

func codexAuthProxyEnvironment(environment []string, upstream, secret string) []string {
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
	)
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
	if upstreamValue == "" || secret == "" {
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

	server := &http.Server{
		Handler:           newCodexAuthProxyHandler(upstream, secret),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	err = server.Serve(listener)
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
