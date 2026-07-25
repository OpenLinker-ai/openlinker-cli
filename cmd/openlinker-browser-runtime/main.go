//go:build !windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserruntime"
)

const (
	defaultBrowserSocket    = "/browser-control/openlinker.browser.sock"
	defaultActiveLeaseFile  = "/browser-control/leases/active-lease.json"
	defaultEngineExecutable = "/usr/bin/node"
	defaultEngineScript     = "/opt/openlinker/browser-engine/dist/main.js"
	defaultProfileStore     = "/browser-state/encrypted-profiles"
	defaultProfileWorkRoot  = "/browser-tmp/profiles"
	defaultProfileRootKey   = "/browser-key/profile-root-key"
	defaultProfileDirectory = "/browser-tmp/profiles/active"
	maxCredentialBytes      = 4096
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "openlinker Browser Runtime:", err)
		os.Exit(1)
	}
}

func run() error {
	socketPath := strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_SOCKET"))
	if socketPath == "" {
		socketPath = defaultBrowserSocket
	}
	credential, err := loadOrCreateCredentialFile(os.Getenv("OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE"))
	if err != nil {
		return err
	}
	activeLeaseFile := strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_ACTIVE_LEASE_FILE"))
	if activeLeaseFile == "" {
		activeLeaseFile = defaultActiveLeaseFile
	}
	engine, err := browserruntime.NewProfileEngine(browserruntime.ProfileEngineOptions{
		Process: browserruntime.ProcessEngineOptions{
			Command:     []string{defaultEngineExecutable, defaultEngineScript},
			Environment: browserEngineEnvironment(),
		},
		StoreRoot: value("OPENLINKER_BROWSER_PROFILE_STORE", defaultProfileStore),
		WorkRoot:  value("OPENLINKER_BROWSER_PROFILE_WORK_ROOT", defaultProfileWorkRoot),
		RootKeyFile: value(
			"OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE",
			defaultProfileRootKey,
		),
	})
	if err != nil {
		return err
	}
	server, err := browserruntime.NewServer(browserruntime.ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: credential,
		Lease:             browserruntime.FileLease{Path: activeLeaseFile},
		Engine:            engine,
	})
	if err != nil {
		return errors.Join(err, engine.Close())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveErr := server.Serve(ctx)
	closeErr := engine.Close()
	return errors.Join(serveErr, closeErr)
}

func browserEngineEnvironment() []string {
	return []string{
		"HOME=" + value("HOME", "/home/pwuser"),
		"TMPDIR=" + value("TMPDIR", "/browser-tmp"),
		"NO_PROXY=",
		"OPENLINKER_BROWSER_EGRESS_PROXY=" + strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_EGRESS_PROXY")),
		"OPENLINKER_BROWSER_PROFILE_DIR=" + value("OPENLINKER_BROWSER_PROFILE_DIR", defaultProfileDirectory),
		"PLAYWRIGHT_BROWSERS_PATH=" + value("PLAYWRIGHT_BROWSERS_PATH", "/ms-playwright"),
		"LANG=" + value("LANG", "C.UTF-8"),
	}
}

func value(name, fallback string) string {
	if configured := strings.TrimSpace(os.Getenv(name)); configured != "" {
		return configured
	}
	return fallback
}

func readCredentialFile(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", errors.New("OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE is required")
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", errors.New("Browser channel credential path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("Browser channel credential must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("Browser channel credential file must have owner-only permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("Browser channel credential file must be owned by the current user")
	}
	if info.Size() < 32 || info.Size() > maxCredentialBytes {
		return "", errors.New("Browser channel credential length is invalid")
	}
	file, err := os.Open(path) // #nosec G304 -- the operator-selected path is validated above.
	if err != nil {
		return "", errors.New("open Browser channel credential file")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(raw) > maxCredentialBytes {
		return "", errors.New("read Browser channel credential file")
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	if len(value) < 32 || len(value) > 512 || strings.ContainsAny(value, "\r\n\t ") {
		return "", errors.New("Browser channel credential value is invalid")
	}
	return value, nil
}

func loadOrCreateCredentialFile(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	credential, err := readCredentialFile(path)
	if err == nil {
		return credential, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(filepath.Clean(path))
	info, statErr := os.Lstat(parent)
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("Browser channel credential directory is invalid")
	}
	var secret [32]byte
	if _, err := io.ReadFull(rand.Reader, secret[:]); err != nil {
		return "", errors.New("generate Browser channel credential")
	}
	value := hex.EncodeToString(secret[:])
	clear(secret[:])
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- fixed validated private control path.
	if errors.Is(err, fs.ErrExist) {
		return readCredentialFile(path)
	}
	if err != nil {
		return "", errors.New("create Browser channel credential")
	}
	_, writeErr := file.WriteString(value + "\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return "", errors.Join(writeErr, syncErr, closeErr)
	}
	return value, nil
}
