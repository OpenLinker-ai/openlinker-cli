package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNodeIDGeneratesPersistsAndRejectsMismatch(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	generated, err := resolveNodeID(runtimeDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !canonicalUUID.MatchString(generated) || generated[14] != '4' {
		t.Fatalf("generated Node ID = %q", generated)
	}
	persisted, err := resolveNodeID(runtimeDir, generated)
	if err != nil || persisted != generated {
		t.Fatalf("persisted Node ID = %q, %v", persisted, err)
	}
	info, err := os.Stat(filepath.Join(runtimeDir, "node-id"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("node-id mode = %v, %v", info, err)
	}
	if _, err := resolveNodeID(runtimeDir, "11111111-1111-4111-8111-111111111111"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestResolveRequiredSecretSupportsDirectAndFile(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "codex-direct")
	value, err := resolveRequiredSecret("CODEX_API_KEY")
	if err != nil || value != "codex-direct" {
		t.Fatalf("direct secret = %q, %v", value, err)
	}

	t.Setenv("CODEX_API_KEY", "")
	path := filepath.Join(t.TempDir(), "codex-key")
	if err := os.WriteFile(path, []byte("codex-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_API_KEY_FILE", path)
	value, err = resolveRequiredSecret("CODEX_API_KEY")
	if err != nil || value != "codex-file" {
		t.Fatalf("file secret = %q, %v", value, err)
	}

	t.Setenv("CODEX_API_KEY", "codex-direct")
	if _, err := resolveRequiredSecret("CODEX_API_KEY"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("dual source error = %v", err)
	}
}

func TestAgentStageEnvironmentResolvesOnlySelectedProviderSecrets(t *testing.T) {
	for _, name := range []string{
		"OPENLINKER_AGENT_TOKEN", "OPENLINKER_AGENT_TOKEN_FILE",
		"CODEX_API_KEY", "CODEX_API_KEY_FILE",
		"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE",
		entrypointStageEnv,
	} {
		t.Setenv(name, "")
	}
	agentTokenFile := filepath.Join(t.TempDir(), "agent-token")
	if err := os.WriteFile(agentTokenFile, []byte("ol_agent_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLINKER_AGENT_TOKEN_FILE", agentTokenFile)
	t.Setenv("CODEX_API_KEY", "codex-direct")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-pass")

	environment, err := agentStageEnvironment("codex")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	if values["OPENLINKER_AGENT_TOKEN"] != "ol_agent_file" || values["CODEX_API_KEY"] != "codex-direct" {
		t.Fatalf("resolved stage secrets = %#v", values)
	}
	for _, forbidden := range []string{"OPENLINKER_AGENT_TOKEN_FILE", "CODEX_API_KEY_FILE", "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE"} {
		if _, ok := values[forbidden]; ok {
			t.Fatalf("stage environment retained %s", forbidden)
		}
	}
	if values[entrypointStageEnv] != "1" {
		t.Fatalf("stage marker = %q", values[entrypointStageEnv])
	}
}

func TestConfigureCodexUsesFixedRuntimeBoundaries(t *testing.T) {
	runtimeDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLINKER_URL", "https://openlinker.example")
	t.Setenv("OPENLINKER_AGENT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("OPENLINKER_AGENT_TOKEN", "ol_agent_test")
	t.Setenv("CODEX_API_KEY", "codex-test")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-selected")
	t.Setenv("OPENLINKER_NODE_ID", "")
	t.Setenv("OPENLINKER_BLOCK_PRIVATE_NETWORK", "true")
	t.Setenv("OPENLINKER_EGRESS_PROXY_URL", "http://gateway:3128")
	t.Setenv("OPENLINKER_CODEX_BASE_URL", "https://router.example/v1")
	if err := configure("codex", runtimeDir, workspace, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if runtimeLock != nil {
			_ = runtimeLock.Close()
			runtimeLock = nil
		}
	})
	for key, want := range map[string]string{
		"OPENLINKER_PROVIDER":            "codex",
		"OPENLINKER_AGENT_STATE_DIR":     runtimeDir,
		"OPENLINKER_AGENT_CONFIG":        filepath.Join(runtimeDir, "agent.json"),
		"OPENLINKER_WORKSPACE":           workspace,
		"OPENLINKER_AGENT_SESSION_REUSE": "true",
		"OPENLINKER_CODEX_BIN":           "/usr/local/bin/openlinker-provider-launcher",
		"OPENLINKER_CODEX_BASE_URL":      "https://router.example/v1",
		"CODEX_API_KEY":                  "codex-test",
		"HTTP_PROXY":                     "http://gateway:3128",
		"NO_PROXY":                       "",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if os.Getenv("OPENLINKER_NODE_ID") == "" {
		t.Fatal("entrypoint did not persist and export Node ID")
	}
}

func TestRuntimeAndWorkspaceValidationFailClosed(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeDir(runtimeDir, false); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("runtime mode error = %v", err)
	}
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkspace(workspace, false); err == nil || !strings.Contains(err.Error(), "group/world writable") {
		t.Fatalf("workspace mode error = %v", err)
	}
}

func TestURLValidationRejectsCredentialsAndNonHTTPS(t *testing.T) {
	for _, raw := range []string{"", "http://openlinker.example", "https://user:pass@openlinker.example", "https://openlinker.example?token=x"} {
		if _, err := validatePlatformURL(raw); err == nil {
			t.Errorf("expected platform URL rejection: %q", raw)
		}
	}
	if got, err := validatePlatformURL("https://openlinker.example/"); err != nil || got != "https://openlinker.example" {
		t.Fatalf("platform URL = %q, %v", got, err)
	}
}

func TestGatewayProxyValidationRejectsCredentialsAndPaths(t *testing.T) {
	for _, raw := range []string{"https://gateway:3128", "http://user:pass@gateway:3128", "http://gateway:3128/proxy", "http://gateway:3128?token=x"} {
		if _, err := validateGatewayProxy(raw); err == nil {
			t.Errorf("expected gateway proxy rejection: %q", raw)
		}
	}
	if got, err := validateGatewayProxy("http://gateway:3128"); err != nil || got != "http://gateway:3128" {
		t.Fatalf("gateway proxy = %q, %v", got, err)
	}
}
