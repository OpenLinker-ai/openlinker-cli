package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

func TestNodeIDIsGeneratedPersistedAndConflictsFail(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateNodeID(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateNodeID(dir, "")
	if err != nil || second != first {
		t.Fatalf("persisted Node ID = %q, %v; want %q", second, err, first)
	}
	info, err := os.Stat(filepath.Join(dir, "node-id"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Node ID file = %#v, %v", info, err)
	}
	if _, err := loadOrCreateNodeID(dir, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"); err == nil {
		t.Fatal("expected explicit Node ID conflict")
	}
}

func TestSecretSourcesAreExclusiveAndPrivate(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "token")
	if err := os.WriteFile(secretPath, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"TOKEN_FILE": secretPath}
	getenv := func(key string) string { return environment[key] }
	value, source, err := resolveSecret(getenv, "TOKEN", "TOKEN_FILE", true)
	if err != nil || value != "secret-value" || source != "file" {
		t.Fatalf("secret = %q/%q, %v", value, source, err)
	}
	environment["TOKEN"] = "direct"
	if _, _, err := resolveSecret(getenv, "TOKEN", "TOKEN_FILE", true); err == nil {
		t.Fatal("expected mutually exclusive secret error")
	}
	if err := os.Chmod(secretPath, 0o644); err != nil {
		t.Fatal(err)
	}
	delete(environment, "TOKEN")
	if _, _, err := resolveSecret(getenv, "TOKEN", "TOKEN_FILE", true); err == nil {
		t.Fatal("expected insecure secret mode error")
	}
}

func TestConfigureNonSecretNeverWritesCredentials(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config", "agent.json")
	environment := map[string]string{"OPENLINKER_AGENT_CONFIG": configPath, "OPENLINKER_AGENT_TOKEN": "must-not-leak"}
	getenv := func(key string) string { return environment[key] }
	config, _, err := ConfigureNonSecret(getenv, ConfigureOptions{
		Provider: "codex", AgentID: "11111111-1111-4111-8111-111111111111", Workspace: dir,
		CodexBaseURL: "https://router.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Provider != "codex" || config.CodexBaseURL != "https://router.example/v1" {
		t.Fatalf("config = %#v", config)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") || strings.Contains(strings.ToLower(string(raw)), "agent_token") {
		t.Fatalf("config leaked a credential: %s", raw)
	}
}

func TestApplyRuntimeEnvironmentUsesProviderSpecificSettings(t *testing.T) {
	config := defaultConfig()
	config.Provider = "codex"
	environment := map[string]string{
		"OPENLINKER_AGENT_TRANSPORT":       "pull",
		"OPENLINKER_AGENT_CAPACITY":        "3",
		"OPENLINKER_AGENT_TIMEOUT_SECONDS": "90",
		"OPENLINKER_AGENT_SESSION_REUSE":   "false",
		"OPENLINKER_CODEX_MODEL":           "gpt-test",
		"OPENLINKER_CODEX_BASE_URL":        "https://router.example/v1",
		"OPENLINKER_CODEX_WEB_SEARCH":      "enabled",
		"OPENLINKER_CODEX_SANDBOX":         "workspace-write",
		"OPENLINKER_CODEX_APPROVAL":        "never",
	}
	if err := applyRuntimeEnvironment(&config, func(key string) string { return environment[key] }); err != nil {
		t.Fatal(err)
	}
	if config.Transport != "pull" || config.Capacity != 3 || config.TimeoutSeconds != 90 || config.SessionReuse ||
		config.Model != "gpt-test" || config.CodexBaseURL != "https://router.example/v1" || !config.WebSearch || config.CodexSandbox != "workspace-write" {
		t.Fatalf("environment overrides = %#v", config)
	}
}

func TestValidateCodexBaseURL(t *testing.T) {
	workspace := t.TempDir()
	base := defaultConfig()
	base.Provider = "codex"
	base.AgentID = "11111111-1111-4111-8111-111111111111"
	base.Workspace = workspace

	for _, value := range []string{"https://router.example/v1", "http://127.0.0.1:8080/v1"} {
		config := base
		config.CodexBaseURL = value
		if err := validateNonSecretConfig(config); err != nil {
			t.Fatalf("valid Codex Base URL %q: %v", value, err)
		}
	}
	for _, value := range []string{
		"router.example/v1", "ftp://router.example/v1", "https://user:pass@router.example/v1",
		"https://router.example/v1?debug=true", "https://router.example/v1#fragment",
	} {
		config := base
		config.CodexBaseURL = value
		if err := validateNonSecretConfig(config); err == nil {
			t.Fatalf("invalid Codex Base URL %q was accepted", value)
		}
	}
}

func TestConfigureCommandStoresCodexBaseURL(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config", "agent.json")
	environment := map[string]string{"OPENLINKER_AGENT_CONFIG": configPath}
	var stdout bytes.Buffer
	command := newConfigureCommand(shared.IO{
		Getenv: func(key string) string { return environment[key] },
		Stdout: &stdout,
	})
	command.SetArgs([]string{
		"--provider", "codex",
		"--agent-id", "11111111-1111-4111-8111-111111111111",
		"--workspace", dir,
		"--codex-base-url", "https://router.example/v1",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"codex_base_url": "https://router.example/v1"`) {
		t.Fatalf("stored config = %s", raw)
	}
}

func TestApplyRuntimeEnvironmentRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		"OPENLINKER_AGENT_CAPACITY":      "zero",
		"OPENLINKER_AGENT_SESSION_REUSE": "sometimes",
		"OPENLINKER_CLAUDE_WEB_SEARCH":   "perhaps",
	} {
		config := defaultConfig()
		config.Provider = "claude"
		if err := applyRuntimeEnvironment(&config, func(key string) string {
			if key == name {
				return value
			}
			return ""
		}); err == nil {
			t.Fatalf("expected %s=%q to fail", name, value)
		}
	}
}

func TestAgentModeLockIsExclusiveAndReusable(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireAgentModeLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireAgentModeLock(dir); err == nil || !strings.Contains(err.Error(), "already serving") {
		t.Fatalf("second lock error = %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireAgentModeLock(dir)
	if err != nil {
		t.Fatalf("reacquire lock: %v", err)
	}
	if err := second.release(); err != nil {
		t.Fatal(err)
	}
}
