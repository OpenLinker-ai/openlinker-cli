package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type browserCaptureProvider struct {
	leases []browserclient.Lease
	rotate bool
	root   string
}

func (provider *browserCaptureProvider) Run(
	_ context.Context,
	run RunContext,
) (openlinker.RuntimeResult, error) {
	if run.Browser == nil {
		return openlinker.RuntimeResult{}, errors.New("Browser context is missing")
	}
	readLease := func() error {
		raw, err := os.ReadFile(filepath.Join(provider.root, "runs", run.RunID+".json"))
		if err != nil {
			return err
		}
		var lease browserclient.Lease
		if err := json.Unmarshal(raw, &lease); err != nil {
			return err
		}
		provider.leases = append(provider.leases, lease)
		return nil
	}
	if err := readLease(); err != nil {
		return openlinker.RuntimeResult{}, err
	}
	if provider.rotate {
		if err := run.Browser.Rotate(); err != nil {
			return openlinker.RuntimeResult{}, err
		}
		if err := readLease(); err != nil {
			return openlinker.RuntimeResult{}, err
		}
	}
	return openlinker.RuntimeResult{
		Status: "success",
		Output: map[string]any{"ok": true},
	}, nil
}

func TestBrowserExecutionLeaseUsesAuthorityAndRejectsLateAttachment(t *testing.T) {
	root := shortBrowserTestRoot(t)
	base := &browserCaptureProvider{rotate: true, root: root}
	config := browserProviderTestConfig(root)
	provider, err := newBrowserExecutionProvider(base, config)
	if err != nil {
		t.Fatal(err)
	}
	run := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(base.leases) != 2 {
		t.Fatalf("leases = %#v", base.leases)
	}
	first, rotated := base.leases[0], base.leases[1]
	if first.Identity.PrincipalScopeID != run.Authority.PrincipalScopeID ||
		first.Identity.AgentID != run.AgentID ||
		first.Identity.RunID != run.RunID {
		t.Fatalf("lease did not use authority: %#v", first.Identity)
	}
	if first.Identity.BrowserSessionID != rotated.Identity.BrowserSessionID ||
		rotated.Identity.SessionEpoch != first.Identity.SessionEpoch+1 ||
		rotated.Identity.ControlEpoch != first.Identity.ControlEpoch+1 ||
		rotated.Identity.AttachmentID == first.Identity.AttachmentID {
		t.Fatalf("rotation did not fence the old attachment: first=%#v rotated=%#v", first.Identity, rotated.Identity)
	}
	for _, path := range []string{
		filepath.Join(root, "active-lease.json"),
		filepath.Join(root, "runs", run.RunID+".json"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lease remained after completion at %s: %v", path, err)
		}
	}
}

func TestBrowserExecutionReusesConversationSessionAndFencesRuntimeReattach(t *testing.T) {
	root := shortBrowserTestRoot(t)
	base := &browserCaptureProvider{root: root}
	provider, err := newBrowserExecutionProvider(base, browserProviderTestConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	firstRun := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	if _, err := provider.Run(context.Background(), firstRun); err != nil {
		t.Fatal(err)
	}
	secondRun := browserProviderTestRun("55555555-5555-4555-8555-555555555555")
	if _, err := provider.Run(context.Background(), secondRun); err != nil {
		t.Fatal(err)
	}
	thirdRun := browserProviderTestRun("66666666-6666-4666-8666-666666666666")
	thirdRun.Authority.RuntimeAttachmentID = "77777777-7777-4777-8777-777777777777"
	if _, err := provider.Run(context.Background(), thirdRun); err != nil {
		t.Fatal(err)
	}
	if len(base.leases) != 3 {
		t.Fatalf("leases = %#v", base.leases)
	}
	first, second, reattached := base.leases[0], base.leases[1], base.leases[2]
	if first.Identity.BrowserSessionID != second.Identity.BrowserSessionID ||
		second.Identity.SessionEpoch != first.Identity.SessionEpoch ||
		second.Identity.ControlEpoch <= first.Identity.ControlEpoch {
		t.Fatalf("same conversation was not reused safely: first=%#v second=%#v", first.Identity, second.Identity)
	}
	if reattached.Identity.BrowserSessionID != first.Identity.BrowserSessionID ||
		reattached.Identity.SessionEpoch != second.Identity.SessionEpoch+1 {
		t.Fatalf("Runtime reattach did not advance Browser epoch: %#v", reattached.Identity)
	}
}

func TestBrowserClientConfigurationIsOptInAndSecretFree(t *testing.T) {
	standard := codexArguments(ProviderConfig{}, "/workspace", "read-only", "", true)
	if strings.Contains(strings.Join(standard, " "), "openlinker_browser") {
		t.Fatalf("ordinary Codex arguments changed: %#v", standard)
	}
	run := &BrowserRunContext{
		PluginBin:  "/opt/openlinker",
		ToolSocket: "/browser/tool.sock",
	}
	config := providerConfigForBrowserRun(ProviderConfig{
		Env: []string{
			"CODEX_API_KEY=must-not-enter-mcp-config",
			"ANTHROPIC_API_KEY=must-not-enter-mcp-config",
		},
	}, run)
	codexArgs := strings.Join(codexArguments(config, "/workspace", "read-only", "", true), " ")
	for _, expected := range []string{
		"mcp_servers.openlinker_browser.command",
		"browser-proxy",
		`enabled_tools=["browser_session"]`,
	} {
		if !strings.Contains(codexArgs, expected) {
			t.Fatalf("Browser Codex config is missing %q: %s", expected, codexArgs)
		}
	}
	claudeArgs := strings.Join(claudeArguments(config, "dontAsk", ""), " ")
	if !strings.Contains(claudeArgs, "--bare") ||
		!strings.Contains(claudeArgs, "--strict-mcp-config") ||
		!strings.Contains(claudeArgs, "--allowedTools mcp__openlinker_browser__browser_session") ||
		strings.Contains(claudeArgs, "--safe-mode") {
		t.Fatalf("Browser Claude config is not isolated: %s", claudeArgs)
	}
	for _, secret := range []string{"must-not-enter-mcp-config", "CODEX_API_KEY", "ANTHROPIC_API_KEY"} {
		if strings.Contains(claudeArgs, secret) || strings.Contains(codexArgs, secret) {
			t.Fatalf("Browser client config leaked %q", secret)
		}
	}
}

func TestBrowserLifecycleEventCountDoesNotGrowWithActionCount(t *testing.T) {
	for _, actionCount := range []int{1, 500} {
		t.Run(fmt.Sprintf("actions-%d", actionCount), func(t *testing.T) {
			root := shortBrowserTestRoot(t)
			base := &browserCaptureProvider{root: root}
			provider, err := newBrowserExecutionProvider(base, browserProviderTestConfig(root))
			if err != nil {
				t.Fatal(err)
			}
			run := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
			run.Metadata = map[string]any{"fixture_action_count": actionCount}
			var events []string
			run.Emit = func(eventType string, _ any) error {
				events = append(events, eventType)
				return nil
			}
			result, err := provider.Run(context.Background(), run)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 ||
				events[0] != "run.browser.lifecycle" ||
				events[1] != "run.browser.lifecycle" {
				t.Fatalf("Browser lifecycle events = %#v, want exactly two", events)
			}
			output, ok := result.Output.(map[string]any)
			if !ok || output["browser_execution_profile"] != "isolated" ||
				output["browser_tool"] != "browser_session" {
				t.Fatalf("Browser result evidence = %#v", result.Output)
			}
		})
	}
}

func browserProviderTestConfig(root string) ProviderConfig {
	return ProviderConfig{
		Provider:              "codex",
		ExecutionProfile:      "browser",
		BrowserPluginBin:      "/opt/openlinker",
		BrowserSocket:         "/browser/control.sock",
		BrowserCredentialFile: "/browser/channel",
		BrowserLeaseRoot:      root,
		BrowserBrokerRoot:     filepath.Join(root, "broker"),
		Timeout:               time.Minute,
	}
}

func shortBrowserTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "olb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func browserProviderTestRun(runID string) RunContext {
	return RunContext{
		RunID:         runID,
		AgentID:       "11111111-1111-4111-8111-111111111111",
		RunDeadlineAt: time.Now().Add(time.Minute),
		Authority: &openlinker.RuntimeAuthorityContext{
			PrincipalScopeID:    "22222222-2222-4222-8222-222222222222",
			RuntimeSessionID:    "33333333-3333-4333-8333-333333333333",
			RuntimeSessionEpoch: 1,
			RuntimeAttachmentID: "99999999-9999-4999-8999-999999999999",
		},
		Conversation: &ConversationContext{
			ID:           "conversation-one",
			SessionKey:   "conversation-one",
			CurrentRunID: runID,
			Source:       "core",
		},
	}
}
