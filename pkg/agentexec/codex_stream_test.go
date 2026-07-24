package agentexec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexJSONLObserverProjectsOnlySafeToolProgress(t *testing.T) {
	var events []map[string]any
	observer := newCodexJSONLObserver(func(eventType string, payload any) error {
		if eventType != "run.status.changed" {
			t.Fatalf("event type = %q", eventType)
		}
		event, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("payload = %#v", payload)
		}
		events = append(events, event)
		return nil
	})
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`,
		`{"type":"item.started","item":{"id":"item-1","type":"command_execution","command":"curl https://secret.invalid/?token=do-not-emit","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"command_execution","command":"curl https://secret.invalid/?token=do-not-emit","status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"mcp_tool_call","arguments":{"api_key":"do-not-emit"},"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"item-3","type":"agent_message","text":"do-not-stream-final"}}`,
	}, "\n") + "\n"
	split := len(input) / 2
	if _, err := observer.Write([]byte(input[:split])); err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Write([]byte(input[split:])); err != nil {
		t.Fatal(err)
	}
	observer.Flush()

	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	want := []map[string]any{
		{"status": "provider_tool_started", "provider": "codex", "phase": "started", "tool_kind": "command"},
		{"status": "provider_tool_completed", "provider": "codex", "phase": "completed", "tool_kind": "command"},
		{"status": "provider_tool_failed", "provider": "codex", "phase": "failed", "tool_kind": "mcp_tool"},
	}
	for index := range want {
		gotJSON, _ := json.Marshal(events[index])
		wantJSON, _ := json.Marshal(want[index])
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("event %d = %s, want %s", index, gotJSON, wantJSON)
		}
	}
	encoded, _ := json.Marshal(events)
	for _, forbidden := range []string{
		"11111111-1111-4111-8111-111111111111",
		"secret.invalid",
		"do-not-emit",
		"do-not-stream-final",
		"command_execution",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("projected events leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBuildCodexPromptAdvertisesWebOnlyWhenEnabled(t *testing.T) {
	run := RunContext{
		RunID: "run-1",
		Input: map[string]any{"text": "latest news"},
	}
	enabled := buildCodexPrompt(run, true, true)
	for _, expected := range []string{
		"Live public-web access is enabled",
		"use web search or a permitted public HTTP tool before answering",
		"Do not claim that internet access is unavailable unless an actual web tool attempt fails",
		"Identify the public source hosts or URLs",
		"private, loopback, link-local, metadata, or credential-bearing destinations",
	} {
		if !strings.Contains(enabled, expected) {
			t.Fatalf("enabled prompt missing %q: %s", expected, enabled)
		}
	}
	disabled := buildCodexPrompt(run, true, false)
	if strings.Contains(disabled, "Live public-web access") ||
		strings.Contains(disabled, "use web search or a permitted public HTTP tool") {
		t.Fatalf("disabled prompt advertised web access: %s", disabled)
	}
}
