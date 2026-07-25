//go:build windows

package browserclient

import (
	"context"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func exchange(
	context.Context,
	string,
	browserprotocol.Request,
	time.Time,
) (browserprotocol.Response, *browserprotocol.Failure) {
	return browserprotocol.Response{}, browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"Browser Runtime is unavailable on Windows",
		false,
	)
}
