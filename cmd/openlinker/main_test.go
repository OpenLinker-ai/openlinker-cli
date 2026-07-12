package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

func TestContextCommandRedactsCredentials(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runCLI([]string{"context"}, strings.NewReader(""), stdout, stderr, testEnv(map[string]string{
		"OPENLINKER_API_BASE":   "http://core.test",
		"OPENLINKER_RUN_ID":     "run-1",
		"OPENLINKER_AGENT_ID":   "agent-1",
		"OPENLINKER_TRACE_ID":   "trace-1",
		"OPENLINKER_USER_TOKEN": "user-secret",
	}))
	if code != 0 {
		t.Fatalf("runCLI() code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, secret := range []string{"user-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("context output leaked %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, `"run_id": "run-1"`) || !strings.Contains(out, `"trace_id": "trace-1"`) {
		t.Fatalf("context output missing run context: %s", out)
	}
}

func TestDefaultOptionsUseOnlyCanonicalEnvironment(t *testing.T) {
	opts := shared.DefaultGlobalOptions(testEnv(map[string]string{
		"OPENLINKER_API_BASE":      "http://canonical.test",
		"OPENLINKER_USER_TOKEN":    "ol_user_canonical",
		"OPENLINKER_API_URL":       "http://legacy.test",
		"OPENLINKER_TOKEN":         "legacy-user-token",
		"OPENLINKER_DEMO_JWT":      "legacy-demo-jwt",
		"OPENLINKER_RUNTIME_TOKEN": "legacy-runtime-token",
		"OPENLINKER_AGENT_TOKEN":   "legacy-agent-token",
	}))
	if opts.APIBase != "http://canonical.test" || opts.UserToken != "ol_user_canonical" {
		t.Fatalf("canonical options = %#v", opts)
	}

	legacyOnly := shared.DefaultGlobalOptions(testEnv(map[string]string{
		"OPENLINKER_API_URL":       "http://legacy.test",
		"OPENLINKER_TOKEN":         "legacy-user-token",
		"OPENLINKER_DEMO_JWT":      "legacy-demo-jwt",
		"OPENLINKER_RUNTIME_TOKEN": "legacy-runtime-token",
		"OPENLINKER_AGENT_TOKEN":   "legacy-agent-token",
	}))
	if legacyOnly.APIBase != "http://localhost:8080" || legacyOnly.UserToken != "" {
		t.Fatalf("legacy aliases were accepted: %#v", legacyOnly)
	}
}

func TestRemovedRuntimeInterfacesAreRejected(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "runtime token flag", args: []string{"--runtime-token", "legacy", "context"}, want: "unknown flag: --runtime-token"},
		{name: "delegate command", args: []string{"delegate", "--agent", "agent-child"}, want: "unknown command \"delegate\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if code := runCLI(tt.args, strings.NewReader(""), stdout, stderr, testEnv(nil)); code == 0 {
				t.Fatalf("runCLI(%v) succeeded stdout=%s", tt.args, stdout.String())
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.want)
			}
		})
	}
}

func TestRunCommandSendsUserTokenAndInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/run" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer user-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["agent_id"] != "agent-target" {
			t.Fatalf("agent_id = %v", body["agent_id"])
		}
		input := body["input"].(map[string]any)
		if input["task"] != "hello" {
			t.Fatalf("input = %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id": "run-started",
			"status": "success",
			"output": map[string]any{"ok": true},
		})
	}))
	defer server.Close()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runCLI([]string{
		"--api", server.URL,
		"--token", "user-token",
		"run",
		"--agent", "agent-target",
		"--input", `{"task":"hello"}`,
	}, strings.NewReader(""), stdout, stderr, testEnv(nil))
	if code != 0 {
		t.Fatalf("runCLI() code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"run_id": "run-started"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunsChildrenCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runs/parent-run/children" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer user-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"child_run_id": "child-1", "status": "success"},
			},
		})
	}))
	defer server.Close()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runCLI([]string{
		"--api", server.URL,
		"--token", "user-token",
		"runs",
		"children",
		"--id", "parent-run",
	}, strings.NewReader(""), stdout, stderr, testEnv(nil))
	if code != 0 {
		t.Fatalf("runCLI() code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"child_run_id": "child-1"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestPayloadTreatsPlainTextAsTextInput(t *testing.T) {
	payload, err := shared.Payload(strings.NewReader(""), "hello world", "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"text":"hello world"`) {
		t.Fatalf("payload = %#v", payload)
	}
}

func testEnv(values map[string]string) func(string) string {
	return func(key string) string {
		if values == nil {
			return ""
		}
		return values[key]
	}
}
