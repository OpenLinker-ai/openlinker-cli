// Package credentials stores platform caller credentials, never Runtime state.
package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const service = "openlinker-cli"

var ErrNotFound = errors.New("no saved login for this API instance")

type Record struct {
	API       string    `json:"api"`
	Store     string    `json:"store"`
	UserID    string    `json:"user_id"`
	TokenID   string    `json:"token_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Token     string    `json:"token,omitempty"`
}

type Keyring interface {
	Get(string, string) (string, error)
	Set(string, string, string) error
	Delete(string, string) error
}
type systemKeyring struct{}

func (systemKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (systemKeyring) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (systemKeyring) Delete(s, u string) error        { return keyring.Delete(s, u) }

type Store struct {
	Dir     string
	Keyring Keyring
}

func New(getenv func(string) string) (*Store, error) {
	dir := ""
	if getenv != nil {
		dir = getenv("OPENLINKER_CONFIG_DIR")
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, errors.New("cannot determine CLI config directory")
		}
		dir = filepath.Join(base, "openlinker")
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("OPENLINKER_CONFIG_DIR must be absolute")
	}
	return &Store{Dir: filepath.Join(dir, "auth"), Keyring: systemKeyring{}}, nil
}

// Origin binding includes an optional API prefix and never accepts userinfo,
// queries, fragments, or remote plaintext HTTP for stored credentials.
func NormalizeAPI(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", errors.New("invalid API URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", errors.New("login requires HTTPS (HTTP is allowed only on loopback)")
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") {
		return "", errors.New("invalid API path")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func key(api string) string             { hash := sha256.Sum256([]byte(api)); return hex.EncodeToString(hash[:]) }
func (s *Store) path(api string) string { return filepath.Join(s.Dir, key(api)+".json") }
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create CLI credential directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe CLI credential directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("CLI credential directory must be private (0700)")
	}
	return nil
}
func (s *Store) Lock(api string) (func(), error) {
	if err := privateDir(s.Dir); err != nil {
		return nil, err
	}
	p := filepath.Join(s.Dir, key(api)+".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("another login/logout is active; if it exited, remove this instance's stale auth .lock file")
	}
	_ = f.Close()
	return func() { _ = os.Remove(p) }, nil
}

func (s *Store) Load(api string) (*Record, error) {
	p := s.path(api)
	// Existing non-auth configuration may have a public or symlinked parent.
	// No saved record means no login, regardless of that parent's permissions.
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	for _, dir := range []string{s.Dir} {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("unsafe CLI credential directory")
		}
	}
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("unsafe CLI credential file")
	}
	if info.Size() > 8192 {
		return nil, errors.New("invalid CLI credential file")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, errors.New("cannot read saved CLI login")
	}
	var r Record
	if json.Unmarshal(data, &r) != nil || r.API != api || r.TokenID == "" || r.UserID == "" || r.ExpiresAt.IsZero() {
		return nil, errors.New("invalid CLI credential record")
	}
	switch r.Store {
	case "keyring":
		r.Token, err = s.Keyring.Get(service, key(api)+":"+r.TokenID)
		if err != nil {
			return nil, errors.New("cannot read system keyring; unlock it or use your existing User Token")
		}
	case "file":
		if runtime.GOOS == "windows" {
			return nil, errors.New("file credential storage is unavailable on Windows")
		}
	default:
		return nil, errors.New("unknown CLI credential store")
	}
	if !strings.HasPrefix(r.Token, "ol_user_") {
		return nil, errors.New("invalid saved User Token")
	}
	return &r, nil
}

// ProbeKeyring fails before a browser authorization if the selected backend is
// unavailable. The temporary entry contains no credential and is always removed.
func (s *Store) ProbeKeyring() error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("cannot prepare system keyring")
	}
	key := "probe:" + hex.EncodeToString(random[:])
	if err := s.Keyring.Set(service, key, "openlinker-keyring-check"); err != nil {
		return errors.New("system keyring unavailable; unlock it or explicitly use --credential-store=file")
	}
	value, readErr := s.Keyring.Get(service, key)
	deleteErr := s.Keyring.Delete(service, key)
	if readErr != nil || value != "openlinker-keyring-check" || deleteErr != nil {
		return errors.New("system keyring check failed; unlock it or explicitly use --credential-store=file")
	}
	return nil
}

// Save requires the caller to hold Lock. A file backend must be explicitly
// selected; an unavailable system keyring never silently becomes plaintext.
func (s *Store) Save(r Record) error {
	if runtime.GOOS == "windows" && r.Store == "file" {
		return errors.New("file credential storage is unavailable on Windows")
	}
	if r.Store != "keyring" && r.Store != "file" {
		return errors.New("credential store must be keyring or file")
	}
	if _, err := os.Lstat(s.path(r.API)); err == nil {
		return errors.New("already logged in; log out before replacing this login")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect saved login")
	}
	saved := false
	defer func() {
		if !saved && r.Store == "keyring" {
			_ = s.Keyring.Delete(service, key(r.API)+":"+r.TokenID)
		}
	}()
	if r.Store == "keyring" {
		if err := s.Keyring.Set(service, key(r.API)+":"+r.TokenID, r.Token); err != nil {
			return errors.New("cannot save to system keyring; use --credential-store=file explicitly if needed")
		}
		r.Token = ""
	}
	data, err := json.Marshal(r)
	if err != nil {
		return errors.New("cannot encode CLI login")
	}
	f, err := os.CreateTemp(s.Dir, ".login-")
	if err != nil {
		return errors.New("cannot create CLI login file")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.path(r.API))
	}
	if err != nil {
		if r.Store == "keyring" {
			_ = s.Keyring.Delete(service, key(r.API)+":"+r.TokenID)
		}
		return errors.New("cannot save CLI login")
	}
	saved = true
	return nil
}

func (s *Store) Delete(r *Record) error {
	if r.Store == "keyring" {
		if err := s.Keyring.Delete(service, key(r.API)+":"+r.TokenID); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return errors.New("cannot remove CLI credential from system keyring")
		}
	}
	if err := os.Remove(s.path(r.API)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot remove saved CLI login")
	}
	return nil
}

func Resolve(api string, getenv func(string) string) (string, error) {
	base, err := NormalizeAPI(api)
	if err != nil {
		return "", nil
	} // Preserve existing explicit-token API compatibility.
	s, err := New(getenv)
	if err != nil {
		return "", nil // No usable config location: preserve anonymous invocation.
	}
	r, err := s.Load(base)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !r.ExpiresAt.After(time.Now()) {
		return "", fmt.Errorf("saved login expired; run openlinker auth logout, then auth login")
	}
	return r.Token, nil
}
