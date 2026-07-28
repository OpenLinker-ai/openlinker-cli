//go:build !windows

package browserruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func TestProcessEngineSuccessFailureAndProtocolReset(t *testing.T) {
	t.Parallel()
	engine := testProcessEngine(t)
	identity := validRuntimeRequest().Identity

	observation, failure := engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://success.example"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.PageStateID != "state-1" ||
		observation.Origin != "https://success.example" ||
		observation.Title != "" {
		t.Fatalf("observation = %#v", observation)
	}

	_, failure = engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://blocked.example"},
	)
	if failure == nil || failure.Code != browserprotocol.ErrorTargetBlocked {
		t.Fatalf("failure = %#v, want target blocked", failure)
	}

	_, failure = engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://malformed.example"},
	)
	if failure == nil || failure.Code != browserprotocol.ErrorOutputInvalid {
		t.Fatalf("failure = %#v, want output invalid", failure)
	}
	observation, failure = engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure != nil || observation.PageStateID != "state-1" {
		t.Fatalf("process did not restart cleanly: observation=%#v failure=%#v", observation, failure)
	}
}

func TestEngineDiagnosticsAreLocalBoundedAndRedacted(t *testing.T) {
	var output strings.Builder
	writer := newEngineDiagnosticWriter(&output)
	_, _ = writer.Write([]byte(
		"launch failed at https://user:pass@example.com/private?token=value " +
			"Authorization: Bear",
	))
	_, _ = writer.Write([]byte("er sk-super-secret-value\n"))
	for index := 0; index < 100; index++ {
		_, _ = writer.Write([]byte(strings.Repeat("x", maxEngineLogLine) + "\n"))
	}
	writer.Flush()
	logged := output.String()
	for _, secret := range []string{
		"https://user:pass@example.com",
		"token=value",
		"sk-super-secret-value",
		"Bearer",
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("engine diagnostic leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "browser-engine: launch failed at [url]") {
		t.Fatalf("bounded diagnostic lost safe context: %q", logged)
	}
	if len(logged) > maxEngineLogBytes {
		t.Fatalf("engine diagnostic bytes = %d, want <= %d", len(logged), maxEngineLogBytes)
	}
}

func TestProcessEngineCancellationKillsChildAndRecovers(t *testing.T) {
	t.Parallel()
	engine := testProcessEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, failure := engine.Execute(
		ctx,
		validRuntimeRequest().Identity,
		browserprotocol.Action{Kind: browserprotocol.ActionWait, DurationMS: processIntPointer(5000)},
	)
	if failure == nil || failure.Code != browserprotocol.ErrorDeadlineExceeded {
		t.Fatalf("failure = %#v, want deadline exceeded", failure)
	}
	observation, failure := engine.Execute(
		testActionContext(t),
		validRuntimeRequest().Identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure != nil || observation.PageStateID != "state-1" {
		t.Fatalf("recovered observation=%#v failure=%#v", observation, failure)
	}
}

func TestProcessEngineRejectsForbiddenEnvironment(t *testing.T) {
	t.Parallel()
	command := []string{os.Args[0], "-test.run=^TestProcessEngineHelper$", "--", "browser-engine-helper"}
	for _, environment := range [][]string{
		{"OPENAI_API_KEY=secret"},
		{"NO_PROXY=localhost"},
		{"HOME=/tmp", "HOME=/other"},
		{"HOME=relative"},
		{"OPENLINKER_BROWSER_EGRESS_PROXY=http://user:secret@proxy:3128"},
	} {
		if _, err := NewProcessEngine(ProcessEngineOptions{
			Command:     command,
			Environment: environment,
		}); err == nil {
			t.Fatalf("NewProcessEngine(%q) succeeded", environment)
		}
	}
}

func TestProcessEngineHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "browser-engine-helper" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), browserprotocol.MaxRequestBytes)
	counter := 0
	for scanner.Scan() {
		var request engineRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		counter++
		switch request.Action.URL {
		case "https://blocked.example":
			writeHelperResponse(engineResponse{
				ContractID: engineContractID,
				ActionID:   request.ActionID,
				Status:     "error",
				Error: browserprotocol.NewFailure(
					browserprotocol.ErrorTargetBlocked,
					"fixture target is blocked",
					false,
				),
			})
		case "https://malformed.example":
			fmt.Println("{")
		default:
			if request.Action.Kind == browserprotocol.ActionWait {
				time.Sleep(5 * time.Second)
			}
			writeHelperResponse(engineResponse{
				ContractID: engineContractID,
				ActionID:   request.ActionID,
				Status:     "ok",
				Observation: &browserprotocol.Observation{
					PageStateID: fmt.Sprintf("state-%d", counter),
					Viewport: &browserprotocol.Viewport{
						Width:  browserprotocol.BrowserViewportWidth,
						Height: browserprotocol.BrowserViewportHeight,
					},
					NavigationGeneration: 1,
					Origin:               strings.TrimSuffix(request.Action.URL, "/"),
					Title:                os.Getenv("HOME"),
				},
			})
		}
	}
	os.Exit(0)
}

func writeHelperResponse(response engineResponse) {
	raw, err := json.Marshal(response)
	if err != nil {
		os.Exit(3)
	}
	fmt.Println(string(raw))
}

func testProcessEngine(t *testing.T) *ProcessEngine {
	t.Helper()
	engine, err := NewProcessEngine(ProcessEngineOptions{
		Command: []string{
			os.Args[0],
			"-test.run=^TestProcessEngineHelper$",
			"--",
			"browser-engine-helper",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = engine.Close()
	})
	return engine
}

func testActionContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func processIntPointer(value int) *int {
	return &value
}
