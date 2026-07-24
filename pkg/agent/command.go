package agent

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, service *Service) *cobra.Command {
	if service == nil {
		service = NewService(ioStreams.Getenv, nil)
	}
	command := &cobra.Command{Use: "agent", Short: "Configure and serve this CLI as an OpenLinker Agent"}
	command.AddCommand(newConfigureCommand(ioStreams))
	command.AddCommand(newServeCommand(service))
	command.AddCommand(newStatusCommand(ioStreams, service))
	command.AddCommand(newDoctorCommand(ioStreams))
	return command
}

func newConfigureCommand(ioStreams shared.IO) *cobra.Command {
	var provider, agentID, workspace, platformURL, state, bin, model, transport, codexBaseURL, sandbox, approval, permission string
	var capacity int64
	var timeout int
	var webSearch, sessionReuse, enabled bool
	var allowedTools shared.StringList
	command := &cobra.Command{
		Use:   "configure",
		Short: "Write non-secret Agent mode configuration",
		RunE: func(command *cobra.Command, args []string) error {
			config, path, err := loadConfig(ioStreams.Getenv)
			if err != nil {
				return err
			}
			if command.Flags().Changed("provider") {
				config.Provider = strings.ToLower(strings.TrimSpace(provider))
			}
			if command.Flags().Changed("agent-id") {
				config.AgentID = strings.TrimSpace(agentID)
			}
			if command.Flags().Changed("workspace") {
				config.Workspace, err = filepath.Abs(strings.TrimSpace(workspace))
				if err != nil {
					return err
				}
			}
			if command.Flags().Changed("url") {
				config.OpenLinkerURL = strings.TrimSpace(platformURL)
			}
			if command.Flags().Changed("state-dir") {
				config.StateDir, err = filepath.Abs(strings.TrimSpace(state))
				if err != nil {
					return err
				}
			}
			if command.Flags().Changed("provider-bin") {
				config.ProviderBin = strings.TrimSpace(bin)
			}
			if command.Flags().Changed("model") {
				config.Model = strings.TrimSpace(model)
			}
			if command.Flags().Changed("transport") {
				config.Transport = strings.ToLower(strings.TrimSpace(transport))
			}
			if command.Flags().Changed("capacity") {
				config.Capacity = capacity
			}
			if command.Flags().Changed("timeout") {
				config.TimeoutSeconds = timeout
			}
			if command.Flags().Changed("session-reuse") {
				config.SessionReuse = sessionReuse
			}
			if command.Flags().Changed("web-search") {
				config.WebSearch = webSearch
			}
			if command.Flags().Changed("codex-base-url") {
				config.CodexBaseURL = strings.TrimSpace(codexBaseURL)
			}
			if command.Flags().Changed("codex-sandbox") {
				config.CodexSandbox = sandbox
			}
			if command.Flags().Changed("codex-approval") {
				config.CodexApproval = approval
			}
			if command.Flags().Changed("claude-permission") {
				config.ClaudePermission = permission
			}
			if command.Flags().Changed("allowed-tool") {
				config.AllowedTools = append([]string(nil), allowedTools...)
			}
			if command.Flags().Changed("enabled") {
				config.Enabled = enabled
			}
			if err := validateNonSecretConfig(config); err != nil {
				return err
			}
			if err := saveConfig(path, config); err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, map[string]any{
				"configured": true, "config_path": path, "provider": config.Provider,
				"agent_id": config.AgentID, "workspace": config.Workspace, "enabled": config.Enabled,
				"secrets_written": false,
			})
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "codex or claude")
	command.Flags().StringVar(&agentID, "agent-id", "", "existing OpenLinker Agent UUID")
	command.Flags().StringVar(&workspace, "workspace", "", "provider workspace")
	command.Flags().StringVar(&platformURL, "url", "", "public OpenLinker platform URL")
	command.Flags().StringVar(&state, "state-dir", "", "private persistent Agent state directory")
	command.Flags().StringVar(&bin, "provider-bin", "", "provider CLI binary")
	command.Flags().StringVar(&model, "model", "", "provider model")
	command.Flags().StringVar(&transport, "transport", "", "auto, websocket, or pull")
	command.Flags().Int64Var(&capacity, "capacity", 1, "maximum concurrent Runs")
	command.Flags().IntVar(&timeout, "timeout", 1800, "provider execution timeout in seconds")
	command.Flags().BoolVar(&sessionReuse, "session-reuse", true, "reuse provider sessions by Core conversation")
	command.Flags().BoolVar(&webSearch, "web-search", false, "allow provider web search")
	command.Flags().StringVar(&codexBaseURL, "codex-base-url", "", "Codex OpenAI-compatible API Base URL")
	command.Flags().StringVar(&sandbox, "codex-sandbox", "read-only", "Codex sandbox mode: read-only, workspace-write, or danger-full-access for externally isolated runtimes")
	command.Flags().StringVar(&approval, "codex-approval", "never", "Codex approval mode")
	command.Flags().StringVar(&permission, "claude-permission", "dontAsk", "Claude permission mode")
	command.Flags().Var(&allowedTools, "allowed-tool", "Claude allowed tool; repeatable")
	command.Flags().BoolVar(&enabled, "enabled", false, "persist Agent mode enable state")
	return command
}

func newServeCommand(service *Service) *cobra.Command {
	var provider string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Run the reliable OpenLinker Runtime Worker in the foreground",
		RunE: func(command *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return service.Run(ctx, provider)
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "override provider: codex or claude")
	return command
}

func newStatusCommand(ioStreams shared.IO, service *Service) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show redacted Agent mode status",
		RunE: func(command *cobra.Command, args []string) error {
			status := service.Status()
			config, _, err := loadConfig(ioStreams.Getenv)
			if err != nil {
				return err
			}
			status.Enabled = config.Enabled
			if status.State == "stopped" {
				if stored, ok := storedStatus(config, ioStreams.Getenv); ok {
					status = stored
					status.Enabled = config.Enabled
				}
			}
			return shared.WriteJSON(ioStreams.Stdout, status)
		},
	}
}

type Diagnostic struct {
	OK      bool              `json:"ok"`
	Checks  map[string]string `json:"checks"`
	Config  string            `json:"config_path"`
	Message string            `json:"message,omitempty"`
}

func newDoctorCommand(ioStreams shared.IO) *cobra.Command {
	var provider string
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check Agent mode without exposing credentials",
		RunE: func(command *cobra.Command, args []string) error {
			diagnostic := Diagnose(ioStreams.Getenv, provider)
			return shared.WriteJSON(ioStreams.Stdout, diagnostic)
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "override provider: codex or claude")
	return command
}

func Diagnose(getenv func(string) string, providerOverride string) Diagnostic {
	config, path, err := loadConfig(getenv)
	result := Diagnostic{OK: true, Checks: map[string]string{}, Config: path}
	if err != nil {
		result.OK, result.Message = false, boundedStatusMessage(err)
		return result
	}
	config.Provider = firstNonEmpty(providerOverride, envValue(getenv, "OPENLINKER_PROVIDER"), config.Provider)
	config.AgentID = firstNonEmpty(envValue(getenv, "OPENLINKER_AGENT_ID"), config.AgentID)
	config.Workspace = firstNonEmpty(envValue(getenv, "OPENLINKER_WORKSPACE"), config.Workspace)
	config.OpenLinkerURL = firstNonEmpty(envValue(getenv, "OPENLINKER_URL"), envValue(getenv, "OPENLINKER_API_BASE"), config.OpenLinkerURL)
	runtimeOptionsErr := applyRuntimeEnvironment(&config, getenv)
	if runtimeOptionsErr == nil {
		runtimeOptionsErr = validateProviderPolicy(config)
	}
	check := func(name string, ok bool, success, failure string) {
		if ok {
			result.Checks[name] = success
		} else {
			result.Checks[name], result.OK = failure, false
		}
	}
	check("provider", config.Provider == "codex" || config.Provider == "claude", config.Provider, "missing_or_invalid")
	check("agent_id", validUUID(config.AgentID), "valid", "missing_or_invalid")
	check("openlinker_url", config.OpenLinkerURL != "", "present", "missing")
	check("runtime_options", runtimeOptionsErr == nil, "valid", "invalid")
	workspaceInfo, workspaceErr := os.Stat(config.Workspace)
	check("workspace", workspaceErr == nil && workspaceInfo.IsDir(), "directory", "missing_or_invalid")
	_, tokenSource, tokenErr := resolveSecret(getenv, "OPENLINKER_AGENT_TOKEN", "OPENLINKER_AGENT_TOKEN_FILE", true)
	check("agent_token", tokenErr == nil, tokenSource, "missing_or_invalid")
	if config.Provider == "codex" || config.Provider == "claude" {
		binEnv, fallback := "OPENLINKER_CODEX_BIN", "codex"
		key, keyFile := "CODEX_API_KEY", "CODEX_API_KEY_FILE"
		if config.Provider == "claude" {
			binEnv, fallback, key, keyFile = "OPENLINKER_CLAUDE_BIN", "claude", "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE"
		}
		bin := firstNonEmpty(envValue(getenv, binEnv), config.ProviderBin, fallback)
		_, binErr := exec.LookPath(bin)
		check("provider_cli", binErr == nil, "present", "missing")
		_, authSource, authErr := resolveSecret(getenv, key, keyFile, false)
		check("provider_auth", authErr == nil, authSource, "invalid")
	}
	state, stateErr := stateDir(config, getenv)
	check("state_dir", stateErr == nil && state != "", "available", "invalid")
	result.Checks["runtime_security"] = "token_only"
	return result
}

func validateNonSecretConfig(config Config) error {
	if config.Provider != "codex" && config.Provider != "claude" {
		return errors.New("--provider must be codex or claude")
	}
	if !validUUID(config.AgentID) {
		return errors.New("--agent-id must be a lowercase UUID")
	}
	info, err := os.Stat(config.Workspace)
	if err != nil || !info.IsDir() {
		return errors.New("--workspace must be an existing directory")
	}
	if config.Capacity < 1 || config.Capacity > 1024 {
		return errors.New("--capacity must be between 1 and 1024")
	}
	if config.TimeoutSeconds < 1 {
		return errors.New("--timeout must be positive")
	}
	switch config.Transport {
	case "auto", "websocket", "ws", "pull", "http":
	default:
		return errors.New("--transport must be auto, websocket/ws, or pull/http")
	}
	return validateProviderPolicy(config)
}

func validateCodexBaseURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" {
		return errors.New("Codex Base URL must be an absolute HTTP(S) URL with a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Codex Base URL must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("Codex Base URL must not contain credentials, a query, or a fragment")
	}
	return nil
}

func validateProviderPolicy(config Config) error {
	if err := validateCodexBaseURL(config.CodexBaseURL); err != nil {
		return err
	}
	switch config.CodexSandbox {
	case "", "read-only", "workspace-write", "danger-full-access":
	default:
		return errors.New("Codex sandbox must be read-only, workspace-write, or danger-full-access")
	}
	switch config.CodexApproval {
	case "", "never", "untrusted", "on-request":
	default:
		return errors.New("Codex approval must be never, untrusted, or on-request")
	}
	switch config.ClaudePermission {
	case "", "acceptEdits", "auto", "dontAsk", "manual", "plan":
	default:
		return errors.New("Claude permission must be acceptEdits, auto, dontAsk, manual, or plan")
	}
	for _, tool := range config.AllowedTools {
		if strings.TrimSpace(tool) == "" || strings.ContainsAny(tool, "\r\n\x00") {
			return errors.New("Claude allowed tools must be non-empty single-line values")
		}
	}
	return nil
}

func SetEnabled(getenv func(string) string, enabled bool) (Config, string, error) {
	config, path, err := loadConfig(getenv)
	if err != nil {
		return Config{}, path, err
	}
	config.Enabled = enabled
	if err := saveConfig(path, config); err != nil {
		return Config{}, path, err
	}
	return config, path, nil
}

func storedStatus(config Config, getenv func(string) string) (Status, bool) {
	dir, err := stateDir(config, getenv)
	if err != nil {
		return Status{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		return Status{}, false
	}
	var status Status
	if decodeStrictJSON(raw, &status) != nil {
		return Status{}, false
	}
	return status, true
}

func ContextWithSignals(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
