package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
)

const (
	defaultAPIVersion        = "2023-06-01"
	defaultBetaVersion       = "computer-use-2025-11-24"
	defaultToolType          = "computer_20251124"
	defaultMaxTokens         = 4096
	defaultMaxHistoryBytes   = 12 << 20
	maxProviderResponseBytes = 16 << 20
	maxToolCallsPerTurn      = 8
)

type Config struct {
	Endpoint           string
	LocalAuthorization string
	Model              string
	APIVersion         string
	BetaVersion        string
	ToolType           string
	DisplayWidth       int
	DisplayHeight      int
	MaxTokens          int
	MaxHistoryBytes    int
	Stream             bool
	MaxAttempts        int
	RetryDelay         time.Duration
	Client             *http.Client
}

type Adapter struct {
	config Config
}

type Session struct {
	messages []message
}

func (session *Session) String() string {
	return "Anthropic browser session"
}

func (session *Session) GoString() string {
	return session.String()
}

func (config Config) String() string {
	return "Anthropic browser adapter configuration"
}

func (config Config) GoString() string {
	return config.String()
}

func (adapter *Adapter) String() string {
	return "Anthropic browser adapter"
}

func (adapter *Adapter) GoString() string {
	return adapter.String()
}

func (adapter *Adapter) Probe(ctx context.Context) *browserprotocol.Failure {
	if adapter == nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProviderCapability,
			"Anthropic computer-use adapter is not configured",
			false,
		)
	}
	response, failure := adapter.send(ctx, messageRequest{
		Model:     adapter.config.Model,
		MaxTokens: adapter.config.MaxTokens,
		Tools: []computerTool{{
			Type:            adapter.config.ToolType,
			Name:            "computer",
			DisplayWidthPX:  adapter.config.DisplayWidth,
			DisplayHeightPX: adapter.config.DisplayHeight,
		}},
		Messages: []message{{
			Role:    "user",
			Content: "Return one screenshot computer action so the caller can verify computer-use capability. Do not answer with text.",
		}},
		Stream: adapter.config.Stream,
	})
	if failure != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProviderCapability,
			"Anthropic computer-use capability probe failed",
			failure.Recoverable,
		)
	}
	calls, _, parseFailure := parseResponse(response)
	if parseFailure != nil || len(calls) == 0 {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProviderCapability,
			"Anthropic Provider did not return a computer call",
			false,
		)
	}
	for _, call := range calls {
		var action providerAction
		if json.Unmarshal(call.Input, &action) == nil &&
			action.Action == "screenshot" {
			return nil
		}
	}
	return browserprotocol.NewFailure(
		browserprotocol.ErrorProviderCapability,
		"Anthropic Provider did not return the required screenshot action",
		false,
	)
}

func (session *Session) Reset() {
	if session != nil {
		session.messages = nil
	}
}

func New(config Config) (*Adapter, error) {
	endpoint, err := validateLoopbackEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	config.Endpoint = endpoint
	config.LocalAuthorization = strings.TrimSpace(config.LocalAuthorization)
	config.Model = strings.TrimSpace(config.Model)
	if len(config.LocalAuthorization) < 32 || len(config.LocalAuthorization) > 512 {
		return nil, errors.New("Anthropic local broker authorization is invalid")
	}
	if config.Model == "" || len(config.Model) > 256 {
		return nil, errors.New("Anthropic computer-use model is invalid")
	}
	if config.APIVersion == "" {
		config.APIVersion = defaultAPIVersion
	}
	if config.BetaVersion == "" {
		config.BetaVersion = defaultBetaVersion
	}
	if config.ToolType == "" {
		config.ToolType = defaultToolType
	}
	if !validVersionToken(config.APIVersion) ||
		!validVersionToken(config.BetaVersion) ||
		!validVersionToken(config.ToolType) {
		return nil, errors.New("Anthropic computer-use protocol version is invalid")
	}
	if config.DisplayWidth == 0 {
		config.DisplayWidth = 1280
	}
	if config.DisplayHeight == 0 {
		config.DisplayHeight = 720
	}
	if config.DisplayWidth < 320 || config.DisplayWidth > 4096 ||
		config.DisplayHeight < 240 || config.DisplayHeight > 4096 {
		return nil, errors.New("Anthropic computer-use display size is invalid")
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = defaultMaxTokens
	}
	if config.MaxTokens < 256 || config.MaxTokens > 64<<10 {
		return nil, errors.New("Anthropic max_tokens is invalid")
	}
	if config.MaxHistoryBytes == 0 {
		config.MaxHistoryBytes = defaultMaxHistoryBytes
	}
	if config.MaxHistoryBytes < 1<<20 || config.MaxHistoryBytes > 64<<20 {
		return nil, errors.New("Anthropic history limit is invalid")
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 2
	}
	if config.MaxAttempts < 1 || config.MaxAttempts > 3 {
		return nil, errors.New("Anthropic Provider attempt limit is invalid")
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = 100 * time.Millisecond
	}
	if config.RetryDelay < 0 || config.RetryDelay > 5*time.Second {
		return nil, errors.New("Anthropic Provider retry delay is invalid")
	}
	if config.Client == nil {
		config.Client = defaultLocalClient()
	}
	client := *config.Client
	client.CheckRedirect = func(
		_ *http.Request,
		_ []*http.Request,
	) error {
		return http.ErrUseLastResponse
	}
	config.Client = &client
	return &Adapter{config: config}, nil
}

func (adapter *Adapter) Run(
	ctx context.Context,
	input browserprovider.RunInput,
	session *Session,
	executor browserprovider.Executor,
) (browserprovider.Result, *browserprotocol.Failure) {
	if adapter == nil || executor == nil {
		return browserprovider.Result{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"Anthropic browser adapter is not configured",
			true,
		)
	}
	normalized, failure := browserprovider.NormalizeRunInput(input)
	if failure != nil {
		return browserprovider.Result{}, failure
	}
	if session == nil {
		session = &Session{}
	}
	messages := append(cloneMessages(session.messages), message{
		Role: "user", Content: normalized.Task,
	})
	if failure := adapter.validateHistory(messages); failure != nil {
		return browserprovider.Result{}, failure
	}

	result := browserprovider.Result{}
	for result.Turns < normalized.MaxTurns {
		if err := ctx.Err(); err != nil {
			return browserprovider.Result{}, browserprovider.ContextFailure(err)
		}
		result.Turns++
		response, requestFailure := adapter.send(ctx, messageRequest{
			Model:     adapter.config.Model,
			MaxTokens: adapter.config.MaxTokens,
			Tools: []computerTool{{
				Type:            adapter.config.ToolType,
				Name:            "computer",
				DisplayWidthPX:  adapter.config.DisplayWidth,
				DisplayHeightPX: adapter.config.DisplayHeight,
			}},
			Messages: messages,
			Stream:   adapter.config.Stream,
		})
		if requestFailure != nil {
			return browserprovider.Result{}, requestFailure
		}
		assistantMessage := message{Role: "assistant", Content: response.Content}
		candidate := append(cloneMessages(messages), assistantMessage)
		if failure := adapter.validateHistory(candidate); failure != nil {
			return browserprovider.Result{}, failure
		}
		messages = candidate

		toolCalls, finalText, parseFailure := parseResponse(response)
		if parseFailure != nil {
			return browserprovider.Result{}, parseFailure
		}
		if len(toolCalls) == 0 {
			result.FinalText = finalText
			session.messages = messages
			return result, nil
		}
		if response.StopReason != "" && response.StopReason != "tool_use" {
			return browserprovider.Result{}, browserprovider.ProviderOutputFailure(
				"Anthropic returned tool use with an inconsistent stop reason",
			)
		}
		if len(toolCalls) > maxToolCallsPerTurn {
			return browserprovider.Result{}, browserprovider.ProviderOutputFailure(
				"Anthropic returned too many computer calls",
			)
		}
		toolResults := make([]toolResultBlock, 0, len(toolCalls))
		for _, call := range toolCalls {
			if result.Actions >= normalized.MaxActions {
				return browserprovider.Result{}, browserprotocol.NewFailure(
					browserprotocol.ErrorActionLimitExceeded,
					"Anthropic browser action limit was exceeded",
					false,
				)
			}
			actions, translateFailure := translateAction(call.Input)
			if translateFailure != nil {
				return browserprovider.Result{}, translateFailure
			}
			var observation browserprotocol.Observation
			for _, action := range actions {
				if result.Actions >= normalized.MaxActions {
					return browserprovider.Result{}, browserprotocol.NewFailure(
						browserprotocol.ErrorActionLimitExceeded,
						"Anthropic browser action limit was exceeded",
						false,
					)
				}
				observation, failure = executor.Execute(ctx, action)
				if failure != nil {
					return browserprovider.Result{}, failure
				}
				result.Actions++
				if normalized.OnProgress != nil {
					normalized.OnProgress(ctx, browserprovider.Progress{
						Action: action, Observation: observation,
					})
				}
			}
			if observation.Screenshot == nil {
				if result.Actions >= normalized.MaxActions {
					return browserprovider.Result{}, browserprotocol.NewFailure(
						browserprotocol.ErrorActionLimitExceeded,
						"Anthropic browser action limit was exceeded",
						false,
					)
				}
				action := browserprotocol.Action{Kind: browserprotocol.ActionScreenshot}
				observation, failure = executor.Execute(ctx, action)
				if failure != nil {
					return browserprovider.Result{}, failure
				}
				result.Actions++
				if normalized.OnProgress != nil {
					normalized.OnProgress(ctx, browserprovider.Progress{
						Action: action, Observation: observation,
					})
				}
			}
			mimeType, data, screenshotFailure := browserprovider.ScreenshotBase64(observation)
			if screenshotFailure != nil {
				return browserprovider.Result{}, screenshotFailure
			}
			toolResults = append(toolResults, toolResultBlock{
				Type:      "tool_result",
				ToolUseID: call.ID,
				Content: []toolResultContent{{
					Type: "image",
					Source: &imageSource{
						Type: "base64", MediaType: mimeType, Data: data,
					},
				}},
			})
		}
		candidate = append(cloneMessages(messages), message{
			Role: "user", Content: toolResults,
		})
		if failure := adapter.validateHistory(candidate); failure != nil {
			return browserprovider.Result{}, failure
		}
		messages = candidate
	}
	return browserprovider.Result{}, browserprotocol.NewFailure(
		browserprotocol.ErrorActionLimitExceeded,
		"Anthropic browser turn limit was exceeded",
		false,
	)
}

func (adapter *Adapter) validateHistory(
	messages []message,
) *browserprotocol.Failure {
	raw, err := json.Marshal(messages)
	if err != nil || len(raw) > adapter.config.MaxHistoryBytes {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"Anthropic browser conversation history cannot be recovered safely",
			false,
		)
	}
	if len(messages) > 512 {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"Anthropic browser conversation history exceeds its message limit",
			false,
		)
	}
	return nil
}

func (adapter *Adapter) send(
	ctx context.Context,
	payload messageRequest,
) (messageResponse, *browserprotocol.Failure) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return messageResponse{}, browserprovider.ProviderOutputFailure(
			"Anthropic request could not be encoded",
		)
	}
	for attempt := 1; attempt <= adapter.config.MaxAttempts; attempt++ {
		request, requestErr := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			adapter.config.Endpoint,
			bytes.NewReader(raw),
		)
		if requestErr != nil {
			return messageResponse{}, browserprovider.ContextFailure(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+adapter.config.LocalAuthorization)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", responseAccept(payload.Stream))
		request.Header.Set("anthropic-version", adapter.config.APIVersion)
		request.Header.Set("anthropic-beta", adapter.config.BetaVersion)
		response, requestErr := adapter.config.Client.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return messageResponse{}, browserprovider.ContextFailure(ctx.Err())
			}
			if attempt < adapter.config.MaxAttempts {
				if failure := waitRetry(ctx, adapter.config.RetryDelay); failure != nil {
					return messageResponse{}, failure
				}
				continue
			}
			return messageResponse{}, browserprovider.ContextFailure(requestErr)
		}
		envelope, retry, responseFailure := decodeHTTPResponse(response, payload.Stream)
		if responseFailure == nil {
			return envelope, nil
		}
		if !retry || attempt == adapter.config.MaxAttempts {
			return messageResponse{}, responseFailure
		}
		if failure := waitRetry(ctx, adapter.config.RetryDelay); failure != nil {
			return messageResponse{}, failure
		}
	}
	return messageResponse{}, browserprovider.ContextFailure(
		errors.New("Anthropic request failed"),
	)
}

func decodeHTTPResponse(
	response *http.Response,
	stream bool,
) (messageResponse, bool, *browserprotocol.Failure) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		retry := response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode == http.StatusRequestTimeout ||
			response.StatusCode >= 500
		return messageResponse{}, retry, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"Anthropic computer-use request was rejected",
			retry,
		)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if stream || strings.Contains(contentType, "text/event-stream") {
		envelope, err := decodeSSE(response.Body)
		if err != nil {
			return messageResponse{}, false, browserprovider.ProviderOutputFailure(
				"Anthropic stream response is invalid",
			)
		}
		return envelope, false, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if err != nil || len(raw) > maxProviderResponseBytes {
		return messageResponse{}, false, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputTooLarge,
			"Anthropic response exceeds the output limit",
			false,
		)
	}
	var envelope messageResponse
	if json.Unmarshal(raw, &envelope) != nil {
		return messageResponse{}, false, browserprovider.ProviderOutputFailure(
			"Anthropic response is invalid",
		)
	}
	if failure := validateMessageResponse(envelope); failure != nil {
		return messageResponse{}, false, failure
	}
	return envelope, false, nil
}

func decodeSSE(reader io.Reader) (messageResponse, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, maxProviderResponseBytes+1))
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	collector := newStreamCollector()
	var eventName string
	var data strings.Builder
	total := 0
	flush := func() error {
		if data.Len() == 0 {
			eventName = ""
			return nil
		}
		err := collector.consume(eventName, []byte(strings.TrimSpace(data.String())))
		data.Reset()
		eventName = ""
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxProviderResponseBytes {
			return messageResponse{}, errors.New("Anthropic stream exceeded limit")
		}
		if line == "" {
			if err := flush(); err != nil {
				return messageResponse{}, err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return messageResponse{}, err
	}
	if err := flush(); err != nil {
		return messageResponse{}, err
	}
	return collector.finish()
}

func validateLoopbackEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" {
		return "", errors.New("Anthropic broker endpoint must be a loopback HTTP URL")
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return "", errors.New("Anthropic broker endpoint must use a loopback address")
	}
	if port := parsed.Port(); port == "" {
		return "", errors.New("Anthropic broker endpoint must include a port")
	} else if value, parseErr := strconv.Atoi(port); parseErr != nil || value < 1 || value > 65535 {
		return "", errors.New("Anthropic broker endpoint port is invalid")
	}
	if net.ParseIP(host) == nil {
		return "", errors.New("Anthropic broker endpoint address is invalid")
	}
	return parsed.String(), nil
}

func validVersionToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func responseAccept(stream bool) string {
	if stream {
		return "text/event-stream"
	}
	return "application/json"
}

func waitRetry(ctx context.Context, delay time.Duration) *browserprotocol.Failure {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return browserprovider.ContextFailure(ctx.Err())
	case <-timer.C:
		return nil
	}
}

func cloneMessages(messages []message) []message {
	return append([]message(nil), messages...)
}

func defaultLocalClient() *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}
