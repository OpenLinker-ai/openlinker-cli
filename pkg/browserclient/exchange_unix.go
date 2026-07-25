//go:build !windows

package browserclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func exchange(
	ctx context.Context,
	socketPath string,
	request browserprotocol.Request,
	deadline time.Time,
) (browserprotocol.Response, *browserprotocol.Failure) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return browserprotocol.Response{}, contextFailure(ctx, "connect to Browser Runtime")
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		return browserprotocol.Response{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"set Browser Runtime deadline",
			true,
		)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return browserprotocol.Response{}, contextFailure(ctx, "send Browser Runtime request")
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		if err := unixConnection.CloseWrite(); err != nil {
			return browserprotocol.Response{}, contextFailure(ctx, "finish Browser Runtime request")
		}
	}
	limited := &io.LimitedReader{
		R: connection,
		N: int64(browserprotocol.MaxResponseBytes) + 1,
	}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var response browserprotocol.Response
	if err := decoder.Decode(&response); err != nil {
		if limited.N == 0 {
			return browserprotocol.Response{}, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"Browser Runtime response exceeds the output limit",
				false,
			)
		}
		if ctx.Err() != nil {
			return browserprotocol.Response{}, contextFailure(ctx, "read Browser Runtime response")
		}
		if errors.Is(err, io.EOF) {
			return browserprotocol.Response{}, browserprotocol.NewFailure(
				browserprotocol.ErrorRuntimeUnavailable,
				"Browser Runtime closed without a response",
				true,
			)
		}
		return browserprotocol.Response{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"Browser Runtime response is invalid",
			false,
		)
	}
	if limited.N == 0 {
		return browserprotocol.Response{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputTooLarge,
			"Browser Runtime response exceeds the output limit",
			false,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return browserprotocol.Response{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"Browser Runtime response contains trailing data",
			false,
		)
	}
	return response, nil
}

func contextFailure(
	ctx context.Context,
	operation string,
) *browserprotocol.Failure {
	switch ctx.Err() {
	case context.Canceled:
		return browserprotocol.NewFailure(
			browserprotocol.ErrorCanceled,
			"browser request was canceled",
			false,
		)
	case context.DeadlineExceeded:
		return browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser request exceeded its deadline",
			true,
		)
	default:
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			operation+" failed",
			true,
		)
	}
}
