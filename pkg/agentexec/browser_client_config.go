package agentexec

import (
	"encoding/json"
	"fmt"
	"strings"
)

var browserEnvironmentNames = []string{
	"OPENLINKER_BROWSER_TOOL_SOCKET",
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

func codexBrowserMCPArguments(config ProviderConfig) []string {
	if !browserProfileEnabled(config) {
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
