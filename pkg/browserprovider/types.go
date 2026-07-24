package browserprovider

import (
	"context"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const (
	DefaultMaxTurns   = 32
	DefaultMaxActions = 128
	MaxTaskBytes      = 64 << 10
	MaxFinalTextBytes = 1 << 20
)

// Executor is the complete shared surface between a Provider-native protocol
// adapter and the Browser Runtime. Provider wire payloads and session state do
// not cross this boundary.
type Executor interface {
	Execute(
		context.Context,
		browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure)
}

type ExecuteFunc func(
	context.Context,
	browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure)

func (execute ExecuteFunc) Execute(
	ctx context.Context,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	return execute(ctx, action)
}

type Progress struct {
	Action      browserprotocol.Action
	Observation browserprotocol.Observation
}

type ProgressSink func(context.Context, Progress)

type RunInput struct {
	Task       string
	MaxTurns   int
	MaxActions int
	OnProgress ProgressSink
}

type Result struct {
	FinalText string
	Turns     int
	Actions   int
}

func NormalizeRunInput(input RunInput) (RunInput, *browserprotocol.Failure) {
	input.Task = strings.TrimSpace(input.Task)
	if input.Task == "" || len(input.Task) > MaxTaskBytes {
		return RunInput{}, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser task is empty or too large",
			false,
		)
	}
	if input.MaxTurns == 0 {
		input.MaxTurns = DefaultMaxTurns
	}
	if input.MaxActions == 0 {
		input.MaxActions = DefaultMaxActions
	}
	if input.MaxTurns < 1 || input.MaxTurns > 128 ||
		input.MaxActions < 1 || input.MaxActions > 4096 {
		return RunInput{}, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser loop limits are invalid",
			false,
		)
	}
	return input, nil
}

func ContextFailure(err error) *browserprotocol.Failure {
	if err == nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"browser provider failed",
			true,
		)
	}
	if err == context.Canceled {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorCanceled,
			"browser provider request was canceled",
			false,
		)
	}
	if err == context.DeadlineExceeded {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser provider request exceeded its deadline",
			false,
		)
	}
	return browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"browser provider request failed",
		true,
	)
}

func ProviderOutputFailure(message string) *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorOutputInvalid,
		message,
		false,
	)
}

func ScreenshotDataURL(
	observation browserprotocol.Observation,
) (string, *browserprotocol.Failure) {
	mimeType, data, failure := ScreenshotBase64(observation)
	if failure != nil {
		return "", failure
	}
	return "data:" + mimeType + ";base64," + data, nil
}

func ScreenshotBase64(
	observation browserprotocol.Observation,
) (string, string, *browserprotocol.Failure) {
	if observation.Screenshot == nil ||
		len(observation.Screenshot.Data) == 0 ||
		len(observation.Screenshot.Data) > browserprotocol.MaxScreenshotBytes {
		return "", "", ProviderOutputFailure("browser screenshot is missing or invalid")
	}
	mimeType := observation.Screenshot.MIMEType
	if mimeType != "image/jpeg" && mimeType != "image/png" {
		return "", "", ProviderOutputFailure("browser screenshot media type is not allowed")
	}
	return mimeType, encodeBase64(observation.Screenshot.Data), nil
}
