package agentexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var browserEnvironmentNames = []string{
	"OPENLINKER_BROWSER_TOOL_SOCKET",
}

var browserClientFallbackReasons = map[string]struct{}{
	"native_bundle_unavailable":    {},
	"native_bundle_invalid":        {},
	"native_host_incompatible":     {},
	"native_activation_failed":     {},
	"native_tool_handshake_failed": {},
}

func providerConfigForBrowserRun(
	config ProviderConfig,
	run *BrowserRunContext,
) ProviderConfig {
	if run == nil {
		return config
	}
	config.ExecutionProfile = "browser"
	config.BrowserPluginBin = run.PluginBin
	config.Env = setEnvironmentValues(config.Env, map[string]string{
		"OPENLINKER_BROWSER_TOOL_SOCKET": run.ToolSocket,
	})
	for _, name := range browserEnvironmentNames {
		config.EnvAllowlist = appendUniqueString(config.EnvAllowlist, name)
	}
	return config
}

func browserProfileEnabled(config ProviderConfig) bool {
	return strings.EqualFold(strings.TrimSpace(config.ExecutionProfile), "browser")
}

func browserClientMode(config ProviderConfig) string {
	if !browserProfileEnabled(config) {
		return ""
	}
	switch strings.TrimSpace(config.BrowserClientMode) {
	case "native":
		return "native"
	default:
		return "mcp"
	}
}

func nativeBrowserClientEnabled(config ProviderConfig) bool {
	return browserClientMode(config) == "native"
}

func directMCPBrowserClientEnabled(config ProviderConfig) bool {
	return browserClientMode(config) == "mcp"
}

func validateBrowserClientConfig(config ProviderConfig) error {
	if !browserProfileEnabled(config) {
		return nil
	}
	requested := strings.TrimSpace(config.BrowserClientModeRequested)
	if requested == "" {
		requested = "mcp"
	}
	switch requested {
	case "auto", "native", "mcp":
	default:
		return errors.New("Browser client mode request must be auto, native, or mcp")
	}
	selected := strings.TrimSpace(config.BrowserClientMode)
	if selected == "" {
		selected = "mcp"
	}
	if selected != "native" && selected != "mcp" {
		return errors.New("effective Browser client mode must be native or mcp")
	}
	if requested != "auto" && requested != selected {
		return errors.New("strict Browser client mode cannot select another surface")
	}
	fallback := strings.TrimSpace(config.BrowserClientFallbackReason)
	if fallback != "" {
		if requested != "auto" || selected != "mcp" {
			return errors.New("Browser fallback evidence requires auto to select mcp")
		}
		if _, ok := browserClientFallbackReasons[fallback]; !ok {
			return errors.New("Browser fallback reason is invalid")
		}
	}
	if selected == "native" && strings.TrimSpace(config.BrowserNativePlugin) == "" {
		return errors.New("native Browser client mode requires a Runtime-owned Plugin path")
	}
	return nil
}

func browserClientEvidence(config ProviderConfig) map[string]any {
	requested := strings.TrimSpace(config.BrowserClientModeRequested)
	if requested == "" {
		requested = "mcp"
	}
	selected := "direct_mcp"
	if nativeBrowserClientEnabled(config) {
		selected = "plugin_native"
	}
	evidence := map[string]any{
		"browser_client_mode_requested": requested,
		"browser_client_mode_selected":  selected,
	}
	if fallback := strings.TrimSpace(config.BrowserClientFallbackReason); fallback != "" {
		evidence["browser_client_mode_fallback_reason"] = fallback
	}
	return evidence
}

func codexBrowserMCPArguments(config ProviderConfig) []string {
	if !directMCPBrowserClientEnabled(config) {
		return nil
	}
	command, _ := json.Marshal(config.BrowserPluginBin)
	arguments, _ := json.Marshal([]string{
		"plugin",
		"browser-proxy",
		"--host",
		"codex",
	})
	environment, _ := json.Marshal(browserEnvironmentNames)
	return []string{
		"-c", "mcp_servers.openlinker_browser.command=" + string(command),
		"-c", "mcp_servers.openlinker_browser.args=" + string(arguments),
		"-c", "mcp_servers.openlinker_browser.env_vars=" + string(environment),
		"-c", "mcp_servers.openlinker_browser.required=true",
		"-c", `mcp_servers.openlinker_browser.enabled_tools=["browser_session"]`,
		"-c", `mcp_servers.openlinker_browser.default_tools_approval_mode="auto"`,
	}
}

func claudeBrowserMCPConfig(config ProviderConfig) string {
	if !directMCPBrowserClientEnabled(config) {
		return ""
	}
	environment := map[string]string{}
	for _, item := range config.Env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		for _, allowed := range browserEnvironmentNames {
			if key == allowed {
				environment[key] = value
			}
		}
	}
	payload := map[string]any{
		"mcpServers": map[string]any{
			"openlinker_browser": map[string]any{
				"type":    "stdio",
				"command": config.BrowserPluginBin,
				"args": []string{
					"plugin",
					"browser-proxy",
					"--host",
					"claude",
				},
				"env": environment,
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("marshal static Browser MCP configuration: %v", err))
	}
	return string(raw)
}

func setEnvironmentValues(environment []string, values map[string]string) []string {
	result := make([]string, 0, len(environment)+len(values))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, replaced := values[key]; !replaced {
			result = append(result, item)
		}
	}
	for _, key := range browserEnvironmentNames {
		if value, ok := values[key]; ok {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func appendUniqueString(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}
