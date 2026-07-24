package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

func TestPluginCommandIncludesBrowserOnlyServer(t *testing.T) {
	var output bytes.Buffer
	command := New(shared.IO{
		Stdin:  strings.NewReader(""),
		Stdout: &output,
		Stderr: &output,
		Getenv: func(string) string { return "" },
	}, nil, nil)
	found, _, err := command.Find([]string{"browser-serve"})
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Name() != "browser-serve" {
		t.Fatalf("browser command = %#v", found)
	}
	command.SetArgs([]string{"browser-serve"})
	if err := command.ExecuteContext(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "--host codex or --host claude") {
		t.Fatalf("browser-serve error = %v", err)
	}
}

func TestBrowserToolEnvironmentRedactsProviderAndOpenLinkerCredentials(t *testing.T) {
	values := map[string]string{
		"CODEX_API_KEY":                              "provider-secret",
		"ANTHROPIC_API_KEY":                          "provider-secret",
		"OPENLINKER_AGENT_TOKEN":                     "agent-secret",
		"OPENLINKER_USER_TOKEN":                      "user-secret",
		"OPENLINKER_BROWSER_SOCKET":                  "/browser/control.sock",
		"OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE": "/browser/channel",
		"OPENLINKER_BROWSER_LEASE_FILE":              "/browser/run.json",
	}
	getenv := browserToolGetenv(func(name string) string { return values[name] })
	for _, name := range []string{
		"CODEX_API_KEY",
		"ANTHROPIC_API_KEY",
		"OPENLINKER_AGENT_TOKEN",
		"OPENLINKER_USER_TOKEN",
	} {
		if value := getenv(name); value != "" {
			t.Fatalf("%s was visible to Browser tool: %q", name, value)
		}
	}
	if getenv("OPENLINKER_BROWSER_SOCKET") != "/browser/control.sock" {
		t.Fatal("Browser control setting was redacted")
	}
}
