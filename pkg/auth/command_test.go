package authcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/credentials"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

func TestCallbackRejectsCSRFAndAcceptsOnlyMatchingLoopback(t *testing.T) {
	state := strings.Repeat("s", 43)
	base, ch, closeServer, err := startCallback(state)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	for _, query := range []string{"state=wrong&code=" + strings.Repeat("c", 43), "state=" + state + "&code=short", "state=" + state + "&state=" + state + "&code=" + strings.Repeat("c", 43)} {
		r, err := http.Get(base + "?" + query)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Fatal("bad callback accepted")
		}
	}
	select {
	case <-ch:
		t.Fatal("invalid callback consumed login")
	default:
	}
	r, err := http.Get(base + "?state=" + state + "&code=" + strings.Repeat("c", 43))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	select {
	case result := <-ch:
		if result.code != strings.Repeat("c", 43) {
			t.Fatal("wrong result")
		}
	case <-time.After(time.Second):
		t.Fatal("callback lost")
	}
}

func TestBrowserLoginPersistsThenVerifiesAndRevokes(t *testing.T) {
	const secret = "ol_user_test-secret-never-print"
	var out, diagnostics bytes.Buffer
	var request map[string]any
	var revoked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli-auth/start":
			json.NewDecoder(r.Body).Decode(&request)
			json.NewEncoder(w).Encode(startResponse{DeviceCode: strings.Repeat("d", 43), UserCode: "ABCD-EFGH", VerificationURI: "https://WEB.example.test:443/cli/authorize", VerificationComplete: "https://web.example.test/cli/authorize?user_code=ABCDEFGH", ExpiresIn: 60, Interval: 1})
		case "/api/v1/cli-auth/token":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["grant_type"] != "authorization_code" || body["redirect_uri"] != request["redirect_uri"] || body["code_verifier"] == "" {
				t.Error("wrong exchange")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": secret, "token_type": "Bearer", "token": map[string]any{"id": "11111111-1111-4111-8111-111111111111", "user_id": "22222222-2222-4222-8222-222222222222", "expires_at": time.Now().Add(time.Hour)}})
		case "/api/v1/cli-auth/session":
			if r.Header.Get("Authorization") != "Bearer "+secret {
				t.Error("wrong credential")
			}
			if r.Method == "DELETE" {
				revoked = true
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "access_token": secret, "token": map[string]any{"id": "11111111-1111-4111-8111-111111111111", "plaintext_token": secret}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := &credentials.Store{Dir: filepath.Join(t.TempDir(), "openlinker", "auth"), Keyring: &testKeyring{values: map[string]string{}}}
	c := &commands{io: shared.IO{Stdout: &out, Stderr: &diagnostics}, opts: &shared.GlobalOptions{APIBase: server.URL, Timeout: time.Second}, store: func() (*credentials.Store, error) { return s, nil }, open: func(ctx context.Context, link string) error {
		if !strings.HasPrefix(link, "https://web.example.test/") {
			t.Fatal("wrong browser target")
		}
		r, err := http.Get(request["redirect_uri"].(string) + "?state=" + request["state"].(string) + "&code=" + strings.Repeat("c", 43))
		if err != nil {
			return err
		}
		r.Body.Close()
		return nil
	}}
	if err := c.login(context.Background(), false, false, "keyring", []string{"agents:read"}); err != nil {
		t.Fatal(err)
	}
	if err := c.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+diagnostics.String(), secret) {
		t.Fatal("secret leaked in output")
	}
	if err := c.logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("logout did not revoke")
	}
	if _, err := s.Load(server.URL); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal("logout retained local credential")
	}
}

type testKeyring struct {
	values map[string]string
	locked bool
}

func (s *testKeyring) Set(service, user, value string) error {
	if s.locked {
		return errors.New("locked")
	}
	s.values[service+user] = value
	return nil
}
func (s *testKeyring) Get(service, user string) (string, error) {
	if s.locked {
		return "", errors.New("locked")
	}
	return s.values[service+user], nil
}
func (s *testKeyring) Delete(service, user string) error { delete(s.values, service+user); return nil }

func TestLockedKeyringFailsBeforeStartingAuthorization(t *testing.T) {
	var out bytes.Buffer
	s := &credentials.Store{Dir: filepath.Join(t.TempDir(), "config", "auth"), Keyring: &testKeyring{locked: true}}
	c := &commands{io: shared.IO{Stdout: &out, Stderr: &out}, opts: &shared.GlobalOptions{APIBase: "https://example.test"}, store: func() (*credentials.Store, error) { return s, nil }}
	if err := c.login(context.Background(), false, true, "keyring", []string{"agents:read"}); err == nil || !strings.Contains(err.Error(), "keyring unavailable") {
		t.Fatal("keyring did not fail before network", err)
	}
	if out.Len() != 0 {
		t.Fatal("authorization started with locked keyring")
	}
	unlock, err := s.Lock("https://example.test")
	if err != nil {
		t.Fatal("login retained lock", err)
	}
	unlock()
}

func TestQuotaErrorExplainsRecoveryWithoutEchoingResponse(t *testing.T) {
	err := (&responseError{status: 409, code: "TOKEN_QUOTA_EXCEEDED"}).Error()
	if !strings.Contains(err, "revoke an old token") {
		t.Fatal(err)
	}
}

func TestAuthenticationClientDoesNotFollowRedirectsOrEchoBodies(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
		w.Write([]byte("ol_user_do-not-print"))
	}))
	defer source.Close()
	err := newClient(source.URL, time.Second).request(context.Background(), "GET", "/session", "ol_user_credential", nil, nil)
	if err == nil || hit || strings.Contains(err.Error(), "ol_user_") {
		t.Fatal("unsafe redirect or error", err)
	}
}

func TestDevicePollingHonorsPendingAndCancellation(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"code":"AUTHORIZATION_PENDING"}}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	_, err := pollDevice(ctx, newClient(server.URL, time.Second), startResponse{Interval: 1, DeviceCode: strings.Repeat("d", 43)}, strings.Repeat("v", 43))
	if err == nil || calls != 1 {
		t.Fatal("poll interval or cancellation ignored", calls, err)
	}
}

func TestDeniedCallback(t *testing.T) {
	state := strings.Repeat("s", 43)
	base, ch, stop, err := startCallback(state)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r, err := http.Get(base + "?" + url.Values{"state": {state}, "error": {"access_denied"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if result := <-ch; !result.denied {
		t.Fatal("denial ignored")
	}
}
