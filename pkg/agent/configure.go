package agent

import (
	"path/filepath"
	"strings"
)

type ConfigureOptions struct {
	Provider         string
	AgentID          string
	Workspace        string
	OpenLinkerURL    string
	StateDir         string
	ProviderBin      string
	Model            string
	Transport        string
	Capacity         int64
	TimeoutSeconds   int
	SessionReuse     *bool
	WebSearch        *bool
	CodexSandbox     string
	CodexApproval    string
	ClaudePermission string
	AllowedTools     []string
}

func ConfigureNonSecret(getenv func(string) string, options ConfigureOptions) (Config, string, error) {
	config, path, err := loadConfig(getenv)
	if err != nil {
		return Config{}, path, err
	}
	if value := strings.TrimSpace(options.Provider); value != "" {
		config.Provider = strings.ToLower(value)
	}
	if value := strings.TrimSpace(options.AgentID); value != "" {
		config.AgentID = value
	}
	if value := strings.TrimSpace(options.Workspace); value != "" {
		config.Workspace, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.OpenLinkerURL); value != "" {
		config.OpenLinkerURL = value
	}
	if value := strings.TrimSpace(options.StateDir); value != "" {
		config.StateDir, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.ProviderBin); value != "" {
		config.ProviderBin = value
	}
	if value := strings.TrimSpace(options.Model); value != "" {
		config.Model = value
	}
	if value := strings.TrimSpace(options.Transport); value != "" {
		config.Transport = strings.ToLower(value)
	}
	if options.Capacity != 0 {
		config.Capacity = options.Capacity
	}
	if options.TimeoutSeconds != 0 {
		config.TimeoutSeconds = options.TimeoutSeconds
	}
	if options.SessionReuse != nil {
		config.SessionReuse = *options.SessionReuse
	}
	if options.WebSearch != nil {
		config.WebSearch = *options.WebSearch
	}
	if value := strings.TrimSpace(options.CodexSandbox); value != "" {
		config.CodexSandbox = value
	}
	if value := strings.TrimSpace(options.CodexApproval); value != "" {
		config.CodexApproval = value
	}
	if value := strings.TrimSpace(options.ClaudePermission); value != "" {
		config.ClaudePermission = value
	}
	if options.AllowedTools != nil {
		config.AllowedTools = append([]string(nil), options.AllowedTools...)
	}
	if err := validateNonSecretConfig(config); err != nil {
		return Config{}, path, err
	}
	if err := saveConfig(path, config); err != nil {
		return Config{}, path, err
	}
	return config, path, nil
}

func ModeEnabled(getenv func(string) string) bool {
	config, _, err := loadConfig(getenv)
	return err == nil && config.Enabled
}
