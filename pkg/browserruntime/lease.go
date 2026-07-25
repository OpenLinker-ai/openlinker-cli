//go:build !windows

package browserruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

type LeaseValidator interface {
	Validate(browserprotocol.Identity) *browserprotocol.Failure
}

type StaticLease struct {
	Identity browserprotocol.Identity
}

func (lease StaticLease) Validate(identity browserprotocol.Identity) *browserprotocol.Failure {
	expected := lease.Identity
	if identity.RunID != expected.RunID ||
		identity.AgentID != expected.AgentID ||
		identity.PrincipalScopeID != expected.PrincipalScopeID ||
		identity.BrowserSessionID != expected.BrowserSessionID ||
		identity.SessionEpoch != expected.SessionEpoch ||
		identity.AttachmentID != expected.AttachmentID {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorIdentityMismatch,
			"browser attachment identity does not match the active lease",
			false,
		)
	}
	if identity.ControlEpoch != expected.ControlEpoch {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorStaleControlEpoch,
			"browser control epoch is stale",
			false,
		)
	}
	return nil
}

type NoActiveLease struct{}

func (NoActiveLease) Validate(browserprotocol.Identity) *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"browser runtime has no active attachment",
		true,
	)
}

type FileLease struct {
	Path string
	Now  func() time.Time
}

func (lease FileLease) Validate(identity browserprotocol.Identity) *browserprotocol.Failure {
	path := filepath.Clean(strings.TrimSpace(lease.Path))
	if !filepath.IsAbs(path) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease path is invalid",
			false,
		)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime has no active attachment",
			true,
		)
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		!owned ||
		int(stat.Uid) != os.Geteuid() ||
		info.Size() <= 0 ||
		info.Size() > 16<<10 {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is invalid",
			false,
		)
	}
	file, err := os.Open(path) // #nosec G304 -- operator-selected active lease path is validated above.
	if err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is unavailable",
			true,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	_ = file.Close()
	if err != nil || len(raw) > 16<<10 {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is unreadable",
			true,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var active browserclient.Lease
	if err := decoder.Decode(&active); err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is invalid",
			false,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is invalid",
			false,
		)
	}
	now := lease.Now
	if now == nil {
		now = time.Now
	}
	if failure := active.Validate(now().UTC()); failure != nil {
		return failure
	}
	return StaticLease{Identity: active.Identity}.Validate(identity)
}
