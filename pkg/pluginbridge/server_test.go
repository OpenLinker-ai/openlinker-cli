package pluginbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/agent"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

func TestServerInitializeAndToolListUseOnlyProtocolOutput(t *testing.T) {
	dir := t.TempDir()
	environment := map[string]string{"OPENLINKER_AGENT_CONFIG": filepath.Join(dir, "agent.json")}
	getenv := func(key string) string { return environment[key] }
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n",
	)
	var stdout, stderr bytes.Buffer
	server := &Server{Host: "codex", IO: shared.IO{Getenv: getenv, Stderr: &stderr}, Agent: agent.NewService(getenv, nil)}
	if err := server.Serve(context.Background(), input, &stdout); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	responses := map[float64]map[string]any{}
	for _, line := range lines {
		var response map[string]any
		if json.Unmarshal([]byte(line), &response) != nil {
			t.Fatalf("invalid JSON-RPC output: %s", stdout.String())
		}
		responses[response["id"].(float64)] = response
	}
	initialized, listed := responses[1], responses[2]
	if initialized == nil || listed == nil {
		t.Fatalf("missing JSON-RPC responses: %s", stdout.String())
	}
	result := listed["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 14 {
		t.Fatalf("tools = %d, want 14", len(tools))
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestServerCancellationDoesNotWaitForStdinEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := &Server{Host: "codex", IO: shared.IO{Getenv: func(string) string { return "" }}, Agent: agent.NewService(func(string) string { return "" }, nil)}
	go func() { done <- server.Serve(ctx, reader, &bytes.Buffer{}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve waited for stdin EOF after cancellation")
	}
}

func TestCancelRequestCancelsMatchingJSONRPCRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancels := map[string]context.CancelFunc{`"run-1"`: cancel}
	cancelRequest(json.RawMessage(`{"requestId":"run-1"}`), cancels)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("matching MCP cancellation did not cancel the request context")
	}
}

func TestAgentConfigurationToolRejectsSecrets(t *testing.T) {
	server := &Server{IO: shared.IO{Getenv: func(string) string { return "" }}}
	_, err := server.configureAgent(map[string]any{
		"provider": "codex", "agent_id": "11111111-1111-4111-8111-111111111111", "workspace": t.TempDir(),
		"api_key": "never-accept-this",
	})
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("error = %v", err)
	}
	_, err = server.configureAgent(map[string]any{
		"provider": "codex", "agent_id": "11111111-1111-4111-8111-111111111111", "workspace": t.TempDir(),
		"nested": map[string]any{"agent_token": "never-accept-this"},
	})
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("nested secret error = %v", err)
	}
}

func TestA2AContextUsesConversationID(t *testing.T) {
	value := a2aContextArgument(map[string]any{
		"conversation_id": "conversation-1",
		"a2a_context":     map[string]any{"protocol_task_id": "turn-2"},
	})
	if value == nil || value.ProtocolContextID != "conversation-1" || value.RootContextID != "conversation-1" || value.ProtocolTaskID != "turn-2" {
		t.Fatalf("A2A context = %#v", value)
	}
}
