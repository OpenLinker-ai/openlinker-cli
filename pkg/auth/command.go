package authcmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/credentials"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

var proofPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var identityPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var userCodePattern = regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{4}$`)

type client struct {
	base string
	http *http.Client
}
type responseError struct {
	status int
	code   string
}

func (e *responseError) Error() string {
	if e.code == "TOKEN_QUOTA_EXCEEDED" {
		return "active User Token limit reached; revoke an old token in website settings, then retry login"
	}
	return fmt.Sprintf("CLI authentication failed (%s, HTTP %d)", e.code, e.status)
}

func newClient(base string, timeout time.Duration) *client {
	return &client{base: base, http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *client) request(ctx context.Context, method, path, token string, payload, dst any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return errors.New("invalid authentication request")
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1/cli-auth"+path, body)
	if err != nil {
		return errors.New("invalid authentication URL")
	}
	r.Header.Set("Accept", "application/json")
	if payload != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(r)
	if err != nil {
		return errors.New("cannot reach authentication service")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return errors.New("invalid authentication response")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var result struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &result)
		code := "REQUEST_FAILED"
		switch result.Error.Code {
		case "INVALID_GRANT", "INVALID_REQUEST", "INVALID_SCOPE", "UNSUPPORTED_GRANT_TYPE", "EXPIRED_TOKEN", "ACCESS_DENIED", "AUTHORIZATION_PENDING", "SLOW_DOWN", "RATE_LIMITED", "UNAUTHORIZED", "CONFLICT", "TOKEN_QUOTA_EXCEEDED":
			code = result.Error.Code
		}
		return &responseError{status: res.StatusCode, code: code}
	}
	if dst != nil && json.Unmarshal(raw, dst) != nil {
		return errors.New("invalid authentication response")
	}
	return nil
}

type startResponse struct {
	DeviceCode           string `json:"device_code"`
	UserCode             string `json:"user_code"`
	VerificationURI      string `json:"verification_uri"`
	VerificationComplete string `json:"verification_uri_complete"`
	ExpiresIn            int    `json:"expires_in"`
	Interval             int    `json:"interval"`
}
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Token       struct {
		ID        string    `json:"id"`
		UserID    string    `json:"user_id"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"token"`
}

type commands struct {
	io    shared.IO
	opts  *shared.GlobalOptions
	store func() (*credentials.Store, error)
	open  func(context.Context, string) error
}

func New(streams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	c := &commands{io: streams, opts: opts, store: func() (*credentials.Store, error) { return credentials.New(streams.Getenv) }, open: openBrowser}
	return c.command()
}
func (c *commands) command() *cobra.Command {
	root := &cobra.Command{Use: "auth", Short: "Sign in to an OpenLinker instance"}
	var device, noBrowser bool
	var backend string
	var scopes []string
	login := &cobra.Command{Use: "login", Short: "Authorize this CLI in your browser", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := interruptibleAuth(cmd.Context())
		defer stop()
		return c.login(ctx, device, noBrowser, backend, scopes)
	}}
	login.Flags().BoolVar(&device, "device-code", false, "Use device-code login for SSH or a headless machine")
	login.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the authorization URL without opening a browser")
	login.Flags().StringVar(&backend, "credential-store", "keyring", "Credential storage: keyring or explicit plaintext file")
	login.Flags().StringSliceVar(&scopes, "scopes", []string{"agents:read", "agents:run", "runs:read", "runs:cancel", "tasks:create"}, "Requested Core permissions (shown for approval in the browser)")
	root.AddCommand(login, &cobra.Command{Use: "status", Short: "Verify the active caller credential without printing it", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return c.status(cmd.Context()) }}, &cobra.Command{Use: "logout", Short: "Revoke and remove this instance's saved CLI credential", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := interruptibleAuth(cmd.Context())
		defer stop()
		return c.logout(ctx)
	}})
	return root
}

// Only credential mutations intercept signals so they can release their lock.
// Existing platform commands retain the OS default termination behavior. Once
// cancellation starts, a second signal can terminate even a blocked keyring.
func interruptibleAuth(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() { <-ctx.Done(); stop() }()
	return ctx, stop
}

func (c *commands) login(ctx context.Context, device, noBrowser bool, backend string, scopes []string) error {
	if strings.TrimSpace(c.opts.UserToken) != "" {
		return errors.New("unset OPENLINKER_USER_TOKEN and omit --token before signing in; explicit credentials take precedence")
	}
	if backend != "keyring" && backend != "file" {
		return errors.New("credential store must be keyring or file")
	}
	if runtime.GOOS == "windows" && backend == "file" {
		return errors.New("file credential storage is unavailable on Windows; use the system keyring")
	}
	base, err := credentials.NormalizeAPI(c.opts.APIBase)
	if err != nil {
		return err
	}
	s, err := c.store()
	if err != nil {
		return err
	}
	unlock, err := s.Lock(base)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := s.Load(base); err == nil {
		return errors.New("already logged in to this instance; run auth logout before changing accounts")
	} else if !errors.Is(err, credentials.ErrNotFound) {
		return err
	}
	if backend == "keyring" {
		if err := s.ProbeKeyring(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	verifier, err := randomProof()
	if err != nil {
		return err
	}
	state, err := randomProof()
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	redirect := ""
	var callback <-chan callbackResult
	if !device {
		var closeServer func()
		redirect, callback, closeServer, err = startCallback(state)
		if err != nil {
			return errors.New("cannot open loopback callback; use --device-code")
		}
		defer closeServer()
	} else {
		state = ""
	}
	api := newClient(base, c.opts.Timeout)
	var start startResponse
	err = api.request(ctx, "POST", "/start", "", map[string]any{"code_challenge": challenge, "code_challenge_method": "S256", "redirect_uri": redirect, "state": state, "scopes": scopes}, &start)
	if err != nil {
		return err
	}
	if !proofPattern.MatchString(start.DeviceCode) || !userCodePattern.MatchString(start.UserCode) || start.ExpiresIn < 1 || start.ExpiresIn > 600 || start.Interval < 1 || start.Interval > 60 {
		return errors.New("invalid login challenge")
	}
	web, err := credentials.NormalizeAPI(start.VerificationURI)
	if err != nil {
		return errors.New("invalid verification URL")
	}
	complete, err := url.Parse(start.VerificationComplete)
	if err != nil || complete.User != nil || complete.Fragment != "" {
		return errors.New("invalid verification URL")
	}
	q := complete.Query()
	complete.RawQuery = ""
	normalizedComplete, err := credentials.NormalizeAPI(complete.String())
	if err != nil || normalizedComplete != web || len(q) != 1 || q.Get("user_code") != strings.ReplaceAll(start.UserCode, "-", "") || len(q["user_code"]) != 1 {
		return errors.New("invalid verification URL")
	}
	fmt.Fprintf(c.io.Stderr, "Sign in to %s\nOpen: %s\nConfirm code: %s\n", base, start.VerificationComplete, start.UserCode)
	if backend == "file" {
		fmt.Fprintln(c.io.Stderr, "Credential storage: private plaintext file (explicitly selected).")
	}
	if !noBrowser && !device {
		if err := c.open(ctx, start.VerificationComplete); err != nil {
			fmt.Fprintln(c.io.Stderr, "Could not open the browser. Open the URL above on this machine.")
		}
	}
	waitCtx, stop := context.WithTimeout(ctx, time.Duration(start.ExpiresIn)*time.Second)
	defer stop()
	var token tokenResponse
	if device {
		token, err = pollDevice(waitCtx, api, start, verifier)
	} else {
		select {
		case <-waitCtx.Done():
			return errors.New("login expired or was cancelled; no credential saved")
		case result := <-callback:
			if result.denied {
				return errors.New("login authorization was denied")
			}
			err = api.request(waitCtx, "POST", "/token", "", map[string]any{"grant_type": "authorization_code", "code": result.code, "code_verifier": verifier, "redirect_uri": redirect}, &token)
		}
	}
	if err != nil {
		return err
	}
	if token.TokenType != "Bearer" || !strings.HasPrefix(token.AccessToken, "ol_user_") || len(token.AccessToken) > 512 || !identityPattern.MatchString(token.Token.ID) || !identityPattern.MatchString(token.Token.UserID) || !token.Token.ExpiresAt.After(time.Now()) {
		return errors.New("invalid login credential response")
	}
	r := credentials.Record{API: base, Store: backend, Token: token.AccessToken, TokenID: token.Token.ID, UserID: token.Token.UserID, ExpiresAt: token.Token.ExpiresAt}
	if err := s.Save(r); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if revokeErr := api.request(cleanupCtx, "DELETE", "/session", r.Token, nil, nil); revokeErr != nil {
			return fmt.Errorf("%w; credential revocation could not be confirmed: remove the OpenLinker CLI token in website settings", err)
		}
		return fmt.Errorf("%w; the newly issued credential was revoked", err)
	}
	return shared.WriteJSON(c.io.Stdout, map[string]any{"authenticated": true, "api_base": base, "user_id": r.UserID, "token_id": r.TokenID, "expires_at": r.ExpiresAt, "credential_store": backend})
}

func pollDevice(ctx context.Context, api *client, start startResponse, verifier string) (tokenResponse, error) {
	interval := time.Duration(start.Interval) * time.Second
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return tokenResponse{}, errors.New("login expired or was cancelled; no credential saved")
		case <-timer.C:
		}
		var token tokenResponse
		err := api.request(ctx, "POST", "/token", "", map[string]any{"grant_type": deviceGrant, "device_code": start.DeviceCode, "code_verifier": verifier}, &token)
		if err == nil {
			return token, nil
		}
		var response *responseError
		if errors.As(err, &response) {
			switch response.code {
			case "AUTHORIZATION_PENDING":
				continue
			case "SLOW_DOWN":
				interval += 5 * time.Second
				continue
			}
		}
		return tokenResponse{}, err
	}
}

func (c *commands) status(ctx context.Context) error {
	base, err := credentials.NormalizeAPI(c.opts.APIBase)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(c.opts.UserToken)
	source := "explicit"
	if token == "" {
		s, err := c.store()
		if err != nil {
			return err
		}
		r, err := s.Load(base)
		if errors.Is(err, credentials.ErrNotFound) {
			return shared.WriteJSON(c.io.Stdout, map[string]any{"authenticated": false, "api_base": base})
		}
		if err != nil {
			return err
		}
		token = r.Token
		source = r.Store
	}
	var result map[string]any
	if err := newClient(base, c.opts.Timeout).request(ctx, "GET", "/session", token, nil, &result); err != nil {
		var response *responseError
		if errors.As(err, &response) && response.status == 401 {
			return shared.WriteJSON(c.io.Stdout, map[string]any{"authenticated": false, "api_base": base, "credential_source": source, "reason": "expired_or_revoked"})
		}
		return err
	}
	// Decode only the known public session fields; never forward arbitrary server
	// JSON (which could accidentally include a token) to stdout.
	var safe struct {
		Authenticated bool `json:"authenticated"`
		User          struct {
			ID string `json:"id"`
		} `json:"user"`
		Token struct {
			ID      string   `json:"id"`
			Issuer  string   `json:"issuer_instance_id"`
			Expires string   `json:"expires_at"`
			Scopes  []string `json:"scopes"`
			Grants  []struct {
				Permission   string  `json:"permission"`
				ResourceType string  `json:"resource_type"`
				ResourceID   *string `json:"resource_id,omitempty"`
			} `json:"grants"`
		} `json:"token"`
	}
	data, _ := json.Marshal(result)
	if err := json.Unmarshal(data, &safe); err != nil {
		return errors.New("invalid session response")
	}
	return shared.WriteJSON(c.io.Stdout, map[string]any{"api_base": base, "credential_source": source, "session": safe})
}

func (c *commands) logout(ctx context.Context) error {
	base, err := credentials.NormalizeAPI(c.opts.APIBase)
	if err != nil {
		return err
	}
	s, err := c.store()
	if err != nil {
		return err
	}
	unlock, err := s.Lock(base)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.Load(base)
	if errors.Is(err, credentials.ErrNotFound) {
		return shared.WriteJSON(c.io.Stdout, map[string]any{"logged_out": true, "api_base": base, "saved_login": false, "explicit_token_active": strings.TrimSpace(c.opts.UserToken) != ""})
	}
	if err != nil {
		return err
	}
	if err := newClient(base, c.opts.Timeout).request(ctx, "DELETE", "/session", r.Token, nil, nil); err != nil {
		var response *responseError
		if !errors.As(err, &response) || response.status != 401 {
			return errors.New("could not confirm revocation; saved credential retained, retry auth logout")
		}
	}
	if err := s.Delete(r); err != nil {
		return err
	}
	return shared.WriteJSON(c.io.Stdout, map[string]any{"logged_out": true, "api_base": base, "explicit_token_active": strings.TrimSpace(c.opts.UserToken) != ""})
}

func randomProof() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("cannot generate login proof")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func openBrowser(ctx context.Context, link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "/usr/bin/open", link)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", link)
	}
	return cmd.Run()
}

type callbackResult struct {
	code   string
	denied bool
}

func startCallback(state string) (string, <-chan callbackResult, func(), error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	result := make(chan callbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/callback" || r.Host != listener.Addr().String() || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid login callback", 400)
			return
		}
		value := callbackResult{}
		if q.Get("error") == "access_denied" && len(q["error"]) == 1 && len(q["code"]) == 0 {
			value.denied = true
		} else if len(q["code"]) == 1 && proofPattern.MatchString(q.Get("code")) && len(q["error"]) == 0 {
			value.code = q.Get("code")
		} else {
			http.Error(w, "Invalid login callback", 400)
			return
		}
		select {
		case result <- value:
			fmt.Fprintln(w, "Return to your terminal to finish OpenLinker sign-in.")
		default:
			http.Error(w, "Login callback already received", 409)
		}
	})
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String() + "/callback", result, func() { _ = server.Close() }, nil
}
