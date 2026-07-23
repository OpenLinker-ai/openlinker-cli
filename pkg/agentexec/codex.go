package agentexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type CodexProvider struct{ Config ProviderConfig }

func (provider CodexProvider) Run(ctx context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	if run.Emit != nil {
		_ = run.Emit("run.message.delta", map[string]any{"text": "Codex is processing the task."})
	}
	config := provider.Config
	bin := strings.TrimSpace(config.Bin)
	if bin == "" {
		bin = "codex"
	}
	workspace := strings.TrimSpace(config.Workspace)
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	sandbox := strings.TrimSpace(config.Sandbox)
	if sandbox == "" {
		sandbox = "read-only"
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sessionKey := conversationSessionKey(run)
	sessionPath := sessionStorePath(config.SessionStore, "codex", workspace)
	sessionID := ""
	if config.SessionReuse && sessionKey != "" {
		unlock := lockSession("codex", workspace, sessionKey)
		defer unlock()
		sessionID = loadSessionID(sessionPath, "codex", workspace, sessionKey)
	}
	resumed := sessionID != ""
	recovered := false
	var stdoutText, stderrText string
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		args := codexArguments(config, workspace, sandbox, sessionID, sessionKey != "")
		stdoutText, stderrText, err = runCodexCommand(requestCtx, cancel, bin, args, workspace, buildPrompt("Codex", run, sessionID == ""), config)
		if err == nil {
			break
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return openlinker.RuntimeResult{}, fmt.Errorf("Codex timed out after %s", timeout)
		}
		if sessionID != "" && attempt == 0 && missingProviderSession(stderrText) {
			if deleteErr := deleteSessionID(sessionPath, "codex", workspace, sessionKey); deleteErr != nil {
				return openlinker.RuntimeResult{}, fmt.Errorf("Codex session recovery failed: %w", deleteErr)
			}
			sessionID = ""
			recovered = true
			continue
		}
		return openlinker.RuntimeResult{}, fmt.Errorf("Codex failed: %w: %s", err, boundedText(stderrText, 500, "no diagnostic output"))
	}
	if config.SessionReuse && sessionKey != "" {
		if observed := extractCodexSessionID(stdoutText + "\n" + stderrText); observed != "" {
			if err := saveSessionID(sessionPath, "codex", workspace, sessionKey, observed); err != nil {
				return openlinker.RuntimeResult{}, sessionPersistenceError("Codex", err)
			}
		}
	}
	summary := strings.TrimSpace(stdoutText)
	if sessionKey != "" {
		// Persistent Codex executions use JSONL so the thread ID can be saved.
		// Parse the final agent message from the same bounded stdout stream. This
		// also keeps the official container's Runtime and Provider UIDs isolated:
		// no cross-UID temporary output file is required.
		summary = extractCodexFinalMessage(stdoutText)
	}
	if summary == "" {
		return openlinker.RuntimeResult{}, errors.New("Codex completed without a final message")
	}
	result := map[string]any{
		"handled_by": "codex", "codex_sandbox": sandbox,
		"codex_model": modelLabel(config.Model), "summary": summary,
	}
	if config.SessionReuse && sessionKey != "" {
		result["codex_session_reuse"] = true
		result["codex_session_key_hash"] = sessionKeyHash("codex", workspace, sessionKey)
		result["codex_session_resumed"] = resumed
		result["codex_session_recovered"] = recovered
	}
	return openlinker.RuntimeResult{
		Status: "success", Output: result,
		Events: []openlinker.RuntimeEvent{{EventType: "run.message.delta", Payload: map[string]any{"text": summary}}},
	}, nil
}

func codexArguments(config ProviderConfig, workspace, sandbox, sessionID string, persistent bool) []string {
	args := []string{}
	if value := strings.TrimSpace(config.CodexApproval); value != "" {
		args = append(args, "--ask-for-approval", value)
	}
	if config.WebSearch {
		args = append(args, "--search")
	} else {
		args = append(args, "-c", `web_search="disabled"`)
	}
	if value := strings.TrimSpace(config.CodexBaseURL); value != "" {
		// OpenAI-compatible routers commonly implement the HTTP Responses API
		// without the optional Responses WebSocket transport. Keep the built-in
		// OpenAI provider untouched and describe the router as a native custom
		// provider so Codex does not attempt an unsupported WebSocket upgrade.
		args = append(args,
			"-c", `model_provider="openlinker_proxy"`,
			"-c", `model_providers.openlinker_proxy.name="OpenLinker-compatible provider"`,
			"-c", fmt.Sprintf("model_providers.openlinker_proxy.base_url=%q", value),
			"-c", `model_providers.openlinker_proxy.env_key="CODEX_API_KEY"`,
			"-c", `model_providers.openlinker_proxy.wire_api="responses"`,
			"-c", `model_providers.openlinker_proxy.supports_websockets=false`,
		)
	}
	if sessionID != "" {
		args = append(args, "-C", workspace, "--sandbox", sandbox, "exec", "resume", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "--json")
		if config.Model != "" {
			args = append(args, "--model", config.Model)
		}
		return append(args, sessionID, "-")
	}
	args = append(args, "exec", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "-C", workspace, "--sandbox", sandbox, "--color", "never")
	if persistent {
		args = append(args, "--json")
	} else {
		args = append(args, "--ephemeral")
	}
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}
	return append(args, "-")
}

func runCodexCommand(ctx context.Context, cancel context.CancelFunc, bin string, args []string, workspace, prompt string, config ProviderConfig) (string, string, error) {
	command := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- operator-configured official provider binary, no shell.
	configureProviderProcess(command)
	command.Dir = workspace
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	allowlist := append([]string{"CODEX_API_KEY", "CODEX_HOME", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "CODEX_CA_CERTIFICATE", "SSL_CERT_FILE"}, config.EnvAllowlist...)
	command.Env = sanitizedEnvironment(environment, allowlist)
	command.Stdin = strings.NewReader(prompt)
	stdout := newLimitedOutputBuffer(cancel)
	stderr := newLimitedOutputBuffer(cancel)
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	if limitErr := outputLimitError("Codex", stdout, stderr); limitErr != nil {
		return stdout.String(), stderr.String(), limitErr
	}
	return stdout.String(), stderr.String(), err
}

func extractCodexSessionID(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxProviderOutputBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event any
		if json.Unmarshal([]byte(line), &event) == nil {
			if id := findCodexSessionID(event); id != "" {
				return id
			}
		}
	}
	return ""
}

func extractCodexFinalMessage(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxProviderOutputBytes)
	final := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event any
		if json.Unmarshal([]byte(line), &event) == nil {
			if message := findCodexFinalMessage(event); message != "" {
				final = message
			}
		}
	}
	return strings.TrimSpace(final)
}

func findCodexFinalMessage(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if eventType, _ := typed["type"].(string); eventType == "agent_message" {
			if message, _ := typed["text"].(string); strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
		}
		for _, nested := range typed {
			if message := findCodexFinalMessage(nested); message != "" {
				return message
			}
		}
	case []any:
		for _, nested := range typed {
			if message := findCodexFinalMessage(nested); message != "" {
				return message
			}
		}
	}
	return ""
}

func findCodexSessionID(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"thread_id", "session_id", "conversation_id"} {
			if id, ok := typed[key].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
		for _, nested := range typed {
			if id := findCodexSessionID(nested); id != "" {
				return id
			}
		}
	case []any:
		for _, nested := range typed {
			if id := findCodexSessionID(nested); id != "" {
				return id
			}
		}
	}
	return ""
}

func modelLabel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "default"
	}
	return strings.TrimSpace(model)
}
