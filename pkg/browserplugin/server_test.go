package browserplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

type fakeExecutor struct {
	mu          sync.Mutex
	actions     []browserprotocol.Action
	observation browserprotocol.Observation
	failure     *browserprotocol.Failure
	execute     func(context.Context, browserprotocol.Action) (
		browserprotocol.Observation,
		*browserprotocol.Failure,
	)
}

func (executor *fakeExecutor) Execute(
	ctx context.Context,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	executor.mu.Lock()
	executor.actions = append(executor.actions, action)
	executor.mu.Unlock()
	if executor.execute != nil {
		return executor.execute(ctx, action)
	}
	return executor.observation, executor.failure
}

func TestServerListsOnlyClientOwnedBrowserTool(t *testing.T) {
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   shared.IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			t.Fatal("tools/list must not initialize Browser Runtime")
			return nil, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponses(t, output.String())
	initialized := responses["1"]["result"].(map[string]any)
	serverInfo := initialized["serverInfo"].(map[string]any)
	if serverInfo["name"] != "openlinker-browser-codex" {
		t.Fatalf("serverInfo = %#v", serverInfo)
	}
	listed := responses["2"]["result"].(map[string]any)
	tools := listed["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "browser_session" {
		t.Fatalf("tool = %#v", tool)
	}
	schemaRaw, _ := json.Marshal(tool["inputSchema"])
	for _, forbidden := range []string{
		"run_id",
		"agent_id",
		"principal_scope_id",
		"conversation_id",
		"attachment_id",
		"channel_credential",
		"api_key",
	} {
		if strings.Contains(string(schemaRaw), forbidden) {
			t.Fatalf("schema exposes trusted or secret field %q: %s", forbidden, schemaRaw)
		}
	}
}

func TestServerReturnsStructuredObservationAndImage(t *testing.T) {
	executor := &fakeExecutor{observation: browserprotocol.Observation{
		PageStateID: "state-1",
		Origin:      "https://example.com",
		Title:       "Example",
		AXTree:      json.RawMessage(`{"role":"document"}`),
		Screenshot: &browserprotocol.Screenshot{
			MIMEType: "image/png",
			Data:     []byte("png-fixture"),
		},
	}}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe"},"_meta":{"threadId":"thread-1"}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   shared.IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	result := decodeResponses(t, output.String())["1"]["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("result = %#v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["page_state_id"] != "state-1" ||
		structured["origin"] != "https://example.com" ||
		structured["title"] != "Example" {
		t.Fatalf("structuredContent = %#v", structured)
	}
	content := result["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["type"] != "image" ||
		content[1].(map[string]any)["mimeType"] != "image/png" {
		t.Fatalf("content = %#v", content)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.actions) != 1 ||
		executor.actions[0].Kind != browserprotocol.ActionScreenshot {
		t.Fatalf("actions = %#v", executor.actions)
	}
}

func TestServerRejectsCallerIdentityAndUnsafeBatch(t *testing.T) {
	executor := &fakeExecutor{}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe","run_id":"11111111-1111-4111-8111-111111111111"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"act","actions":[{"kind":"click","x":1,"y":2},{"kind":"wait","duration_ms":1}]}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "claude",
		IO:   shared.IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponses(t, output.String())
	for _, id := range []string{"1", "2"} {
		result := responses[id]["result"].(map[string]any)
		if result["isError"] != true {
			t.Fatalf("response %s = %#v", id, responses[id])
		}
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.actions) != 0 {
		t.Fatalf("rejected calls executed actions: %#v", executor.actions)
	}
}

func TestServerCloseRejectsLaterActions(t *testing.T) {
	executor := &fakeExecutor{}
	server := &Server{
		Host: "codex",
		IO:   shared.IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	var closeOutput bytes.Buffer
	if err := server.Serve(
		context.Background(),
		strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"close"}}}`+"\n",
		),
		&closeOutput,
	); err != nil {
		t.Fatal(err)
	}
	closeResult := decodeResponses(t, closeOutput.String())["1"]["result"].(map[string]any)
	if closeResult["structuredContent"].(map[string]any)["status"] != "closed" {
		t.Fatalf("close result = %#v", closeResult)
	}
	var observeOutput bytes.Buffer
	if err := server.Serve(
		context.Background(),
		strings.NewReader(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe"}}}`+"\n",
		),
		&observeOutput,
	); err != nil {
		t.Fatal(err)
	}
	observeResult := decodeResponses(t, observeOutput.String())["2"]["result"].(map[string]any)
	if observeResult["isError"] != true {
		t.Fatalf("observe result = %#v", observeResult)
	}
}

func TestServerCancellationInterruptsBrowserAction(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	started := make(chan struct{})
	executor := &fakeExecutor{
		execute: func(
			ctx context.Context,
			_ browserprotocol.Action,
		) (browserprotocol.Observation, *browserprotocol.Failure) {
			close(started)
			<-ctx.Done()
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorCanceled,
				"browser request was canceled",
				false,
			)
		},
	}
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   shared.IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background(), reader, &output)
	}()
	_, _ = io.WriteString(
		writer,
		`{"jsonrpc":"2.0","id":"action-1","method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe"}}}`+"\n",
	)
	<-started
	_, _ = io.WriteString(
		writer,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"action-1"}}`+"\n",
	)
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Browser plugin cancellation did not finish")
	}
	responses := decodeResponses(t, output.String())
	result := responses[`"action-1"`]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func decodeResponses(
	t *testing.T,
	output string,
) map[string]map[string]any {
	t.Helper()
	responses := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid JSON-RPC output %q: %v", line, err)
		}
		idRaw, _ := json.Marshal(response["id"])
		key := string(idRaw)
		if number, ok := response["id"].(float64); ok {
			key = strings.TrimSuffix(strings.TrimSuffix(
				json.Number(fmtFloat(number)).String(),
				".0",
			), ".")
		}
		responses[key] = response
	}
	return responses
}

func fmtFloat(value float64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
