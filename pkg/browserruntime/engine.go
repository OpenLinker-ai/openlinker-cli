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

type ViewerEngine interface {
	ExecuteViewer(
		context.Context,
		browserprotocol.Identity,
		browserprotocol.ViewerOperation,
		*browserprotocol.ViewerInput,
	) (*browserprotocol.ViewerFrame, *browserprotocol.Failure)
}
