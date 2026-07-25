//go:build !windows

package browserruntime

import (
	"context"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

type Engine interface {
	Execute(
		context.Context,
		browserprotocol.Identity,
		browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure)
}

type NotReadyEngine struct{}

func (NotReadyEngine) Execute(
	context.Context,
	browserprotocol.Identity,
	browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	return browserprotocol.Observation{}, browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"browser engine is not configured",
		true,
	)
}
