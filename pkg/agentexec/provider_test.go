package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestCodexProviderReusesTrustedConversationSession(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex-fake")
	logPath := filepath.Join(dir, "args.log")
	scriptBody := `#!/bin/sh
set -eu
mode="new"
for item in "$@"; do
  if [ "$item" = "resume" ]; then mode="resume"; fi
done
printf '%s\n' "$*" >> "$TEST_LOG"
cat > "$TEST_LOG.$mode.prompt"
printf '%s\n' '{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"provider answer"}}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	config := ProviderConfig{
		Provider: "codex", Bin: script, Workspace: dir, Sandbox: "read-only", CodexApproval: "never",
		Timeout: 5e9, SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json"),
		Env: append(os.Environ(), "TEST_LOG="+logPath), EnvAllowlist: []string{"TEST_LOG"},
	}
	provider := CodexProvider{Config: config}
	run := RunContext{
		RunID: "run-1", Input: map[string]any{"text": "current request"}, Metadata: map[string]any{},
		Conversation: &ConversationContext{
			ID: "conversation-1", SessionKey: "conversation-1", RootContextID: "conversation-1",
			CurrentRunID: "run-1", Source: "core",
			HistoryBeforeCurrent: []ConversationMessage{{Role: "user", Content: "earlier request"}},
		},
	}
	first, err := provider.Run(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "success" {
		t.Fatalf("first result = %#v", first)
	}
	if first.Output.(map[string]any)["summary"] != "provider answer" {
		t.Fatalf("first output = %#v", first.Output)
	}
	run.RunID = "run-2"
	run.Conversation.CurrentRunID = "run-2"
	second, err := provider.Run(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	output := second.Output.(map[string]any)
	if output["codex_session_resumed"] != true {
		t.Fatalf("second output = %#v", output)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "exec resume") || !strings.Contains(string(args), "--ignore-user-config") || !strings.Contains(string(args), "--ignore-rules") {
		t.Fatalf("Codex args = %s", args)
	}
	if strings.Contains(string(args), "--output-last-message") {
		t.Fatalf("Codex args reintroduced a cross-UID output file: %s", args)
	}
	newPrompt, _ := os.ReadFile(logPath + ".new.prompt")
	resumePrompt, _ := os.ReadFile(logPath + ".resume.prompt")
	if !strings.Contains(string(newPrompt), "earlier request") {
		t.Fatalf("new prompt did not include trusted fallback history: %s", newPrompt)
	}
	if strings.Contains(string(resumePrompt), "earlier request") {
		t.Fatalf("resume prompt duplicated prior history: %s", resumePrompt)
	}
	info, err := os.Stat(config.SessionStore)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %o", info.Mode().Perm())
	}
}

func TestClaudeProviderUsesSafeModeAndResumes(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude-fake")
	logPath := filepath.Join(dir, "args.log")
	scriptBody := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TEST_LOG"
cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"claude answer","session_id":"22222222-2222-4222-8222-222222222222"}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	provider := ClaudeProvider{Config: ProviderConfig{
		Provider: "claude", Bin: script, Workspace: dir, Permission: "dontAsk", Timeout: 5e9,
		SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json"),
		Env: append(os.Environ(), "TEST_LOG="+logPath), EnvAllowlist: []string{"TEST_LOG"},
	}}
	run := RunContext{RunID: "run-1", Input: map[string]any{"text": "hello"}, Metadata: map[string]any{}, Conversation: &ConversationContext{
		ID: "conversation-2", SessionKey: "conversation-2", CurrentRunID: "run-1", Source: "core",
	}}
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.RunID, run.Conversation.CurrentRunID = "run-2", "run-2"
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(logPath)
	text := string(args)
	if !strings.Contains(text, "--safe-mode") || !strings.Contains(text, "--no-chrome") || !strings.Contains(text, "--resume 22222222-2222-4222-8222-222222222222") {
		t.Fatalf("Claude args = %s", text)
	}
}

type captureProvider struct{ run RunContext }

func (provider *captureProvider) Run(_ context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	provider.run = run
	return openlinker.RuntimeResult{Status: "success", Output: map[string]any{"ok": true}}, nil
}

func TestHandlerTrustsOnlyCoreConversation(t *testing.T) {
	provider := &captureProvider{}
	handler := Handler{Provider: provider}
	assignment := openlinker.RuntimeContext{
		RunID: "run-1", AgentID: "agent-1", Input: map[string]any{"text": "hello"},
		Metadata: openlinker.RuntimeJSONMap{"conversation": map[string]any{
			"id": "spoofed", "session_key": "spoofed", "current_run_id": "run-1", "source": "caller",
		}},
	}
	if _, err := handler.Handle(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if provider.run.Conversation != nil {
		t.Fatalf("caller conversation was trusted: %#v", provider.run.Conversation)
	}
	if _, exists := provider.run.Metadata["conversation"]; exists {
		t.Fatalf("conversation control metadata leaked into provider task metadata: %#v", provider.run.Metadata)
	}
	assignment.Metadata["conversation"] = map[string]any{
		"id": "trusted", "session_key": "trusted", "current_run_id": "run-1", "source": "core",
	}
	if _, err := handler.Handle(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if provider.run.Conversation == nil || provider.run.Conversation.SessionKey != "trusted" {
		t.Fatalf("Core conversation missing: %#v", provider.run.Conversation)
	}
	if _, exists := provider.run.Metadata["conversation"]; exists {
		t.Fatalf("trusted conversation was duplicated into provider task metadata: %#v", provider.run.Metadata)
	}
}

func TestSessionStoreRejectsInsecureExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte(`{"sessions":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveSessionID(path, "codex", t.TempDir(), "conversation", "session"); err == nil {
		t.Fatal("expected insecure existing session store to be rejected")
	}
}
