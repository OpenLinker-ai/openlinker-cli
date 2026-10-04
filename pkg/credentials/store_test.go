package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type memoryKeyring struct {
	values map[string]string
	fail   bool
}

func (m *memoryKeyring) Get(s, u string) (string, error) {
	if m.fail {
		return "", errors.New("locked")
	}
	v, ok := m.values[s+u]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}
func (m *memoryKeyring) Set(s, u, p string) error {
	if m.fail {
		return errors.New("locked")
	}
	m.values[s+u] = p
	return nil
}
func (m *memoryKeyring) Delete(s, u string) error { delete(m.values, s+u); return nil }

func TestAPIIdentityAndTransport(t *testing.T) {
	for _, raw := range []string{"https://EXAMPLE.test:443/", "https://example.test"} {
		if got, err := NormalizeAPI(raw); err != nil || got != "https://example.test" {
			t.Fatal("equivalent origins differ", got, err)
		}
	}
	for _, raw := range []string{"https://user:secret@example.test", "https://example.test?token=x", "https://example.test/#x", "http://example.test", "https://example.test/a/../b", "file:///tmp/test"} {
		if _, err := NormalizeAPI(raw); err == nil {
			t.Errorf("unsafe API accepted: %s", raw)
		}
	}
	for _, raw := range []string{"https://EXAMPLE.test/", "http://127.0.0.1:8080", "https://example.test/prefix"} {
		if _, err := NormalizeAPI(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKeyringPreflightLeavesNoCredential(t *testing.T) {
	m := &memoryKeyring{values: map[string]string{}}
	s := &Store{Keyring: m}
	if err := s.ProbeKeyring(); err != nil {
		t.Fatal(err)
	}
	if len(m.values) != 0 {
		t.Fatal("probe entry retained")
	}
	m.fail = true
	if err := s.ProbeKeyring(); err == nil {
		t.Fatal("locked keyring accepted")
	}
}

func TestPublicOrSymlinkedConfigParentIsCompatible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX parent permissions")
	}
	for _, symlink := range []bool{false, true} {
		parent := filepath.Join(t.TempDir(), "config")
		if err := os.Mkdir(parent, 0755); err != nil {
			t.Fatal(err)
		}
		if symlink {
			link := filepath.Join(t.TempDir(), "linked")
			if err := os.Symlink(parent, link); err != nil {
				t.Fatal(err)
			}
			parent = link
		}
		s := &Store{Dir: filepath.Join(parent, "auth"), Keyring: &memoryKeyring{values: map[string]string{}}}
		api := "https://example.test"
		if _, err := s.Load(api); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		unlock, err := s.Lock(api)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Save(Record{API: api, Store: "keyring", UserID: "user", TokenID: "token", Token: "ol_user_test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		unlock()
		if _, err := s.Load(api); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreInstanceIsolationAndExplicitFile(t *testing.T) {
	for _, backend := range []string{"keyring", "file"} {
		t.Run(backend, func(t *testing.T) {
			if runtime.GOOS == "windows" && backend == "file" {
				t.Skip("POSIX file backend")
			}
			s := &Store{Dir: filepath.Join(t.TempDir(), "openlinker", "auth"), Keyring: &memoryKeyring{values: map[string]string{}}}
			api := "https://one.example.test"
			unlock, err := s.Lock(api)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			r := Record{API: api, Store: backend, UserID: "user", TokenID: "token", Token: "ol_user_test-only", ExpiresAt: time.Now().Add(time.Hour)}
			if err := s.Save(r); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(s.path(api))
			if backend == "keyring" && strings.Contains(string(data), r.Token) {
				t.Fatal("keyring token written to disk")
			}
			got, err := s.Load(api)
			if err != nil || got.Token != r.Token {
				t.Fatal("saved credential did not round trip", err)
			}
			if _, err := s.Load("https://two.example.test"); !errors.Is(err, ErrNotFound) {
				t.Fatal("cross-instance credential reuse")
			}
			if err := s.Save(r); err == nil {
				t.Fatal("silently overwrote account")
			}
			if err := s.Delete(got); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(api); !errors.Is(err, ErrNotFound) {
				t.Fatal("logout retained credential")
			}
		})
	}
}

func TestKeyringFailureNeverFallsBackToFile(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), "openlinker", "auth"), Keyring: &memoryKeyring{fail: true}}
	api := "https://example.test"
	unlock, err := s.Lock(api)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := s.Save(Record{API: api, Store: "keyring", Token: "ol_user_test-secret"}); err == nil {
		t.Fatal("locked keyring succeeded")
	}
	files, _ := os.ReadDir(s.Dir)
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".json") {
			t.Fatal("plaintext fallback created")
		}
	}
}

func TestUnsafeFilesAndConcurrentWritersAreRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink fixture")
	}
	s := &Store{Dir: filepath.Join(t.TempDir(), "openlinker", "auth")}
	api := "https://example.test"
	unlock, err := s.Lock(api)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := s.Lock(api); err == nil {
		t.Fatal("concurrent login acquired lock")
	}
	out := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(out, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, s.path(api)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(api); err == nil {
		t.Fatal("read symlink credential")
	}
}
