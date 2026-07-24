//go:build !windows

package browserruntime

import (
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
		identity.ConversationID != expected.ConversationID ||
		identity.BrowserGeneration != expected.BrowserGeneration ||
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
