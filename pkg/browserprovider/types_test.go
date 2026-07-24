package browserprovider

import (
	"context"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func TestNormalizeRunInputAndScreenshotDataURL(t *testing.T) {
	t.Parallel()
	input, failure := NormalizeRunInput(RunInput{Task: " inspect page "})
	if failure != nil {
		t.Fatal(failure)
	}
	if input.Task != "inspect page" ||
		input.MaxTurns != DefaultMaxTurns ||
		input.MaxActions != DefaultMaxActions {
		t.Fatalf("input = %#v", input)
	}
	dataURL, failure := ScreenshotDataURL(browserprotocol.Observation{
		PageStateID: "page-1",
		Screenshot: &browserprotocol.Screenshot{
			MIMEType: "image/jpeg",
			Data:     []byte("image"),
		},
	})
	if failure != nil || dataURL != "data:image/jpeg;base64,aW1hZ2U=" {
		t.Fatalf("dataURL=%q failure=%#v", dataURL, failure)
	}
}

func TestExecuteFuncAndContextFailure(t *testing.T) {
	t.Parallel()
	executor := ExecuteFunc(func(
		_ context.Context,
		action browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure) {
		return browserprotocol.Observation{PageStateID: string(action.Kind)}, nil
	})
	observation, failure := executor.Execute(
		context.Background(),
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure != nil || observation.PageStateID != "screenshot" {
		t.Fatalf("observation=%#v failure=%#v", observation, failure)
	}
	if ContextFailure(context.Canceled).Code != browserprotocol.ErrorCanceled ||
		ContextFailure(context.DeadlineExceeded).Code != browserprotocol.ErrorDeadlineExceeded {
		t.Fatal("context failures were not stable")
	}
}
