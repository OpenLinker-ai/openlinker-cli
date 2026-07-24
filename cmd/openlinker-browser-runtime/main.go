//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserruntime"
)

const (
	defaultBrowserSocket = "/browser-control/openlinker.browser.sock"
	maxCredentialBytes   = 4096
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
	credential, err := readCredentialFile(os.Getenv("OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE"))
	if err != nil {
		return err
	}
	server, err := browserruntime.NewServer(browserruntime.ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: credential,
		Lease:             browserruntime.NoActiveLease{},
		Engine:            browserruntime.NotReadyEngine{},
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return server.Serve(ctx)
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
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
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
