package openai

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
	maxProviderResponseBytes = 16 << 20
	maxComputerCallsPerTurn  = 8
	maxActionsPerCall        = 32
)

type Config struct {
	Endpoint           string
	LocalAuthorization string
	Model              string
	Stream             bool
	MaxAttempts        int
	RetryDelay         time.Duration
	Client             *http.Client
}

type Adapter struct {
	config Config
}

type Session struct {
	previousResponseID string
}

func (session *Session) String() string {
	return "OpenAI browser session"
}

func (session *Session) GoString() string {
	return session.String()
}

func (config Config) String() string {
	return "OpenAI browser adapter configuration"
}

func (config Config) GoString() string {
	return config.String()
}

func (adapter *Adapter) String() string {
	return "OpenAI browser adapter"
}

func (adapter *Adapter) GoString() string {
	return adapter.String()
}

func (adapter *Adapter) Probe(ctx context.Context) *browserprotocol.Failure {
	if adapter == nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProviderCapability,
			"OpenAI computer-use adapter is not configured",
			false,
		)
	}
	response, failure := adapter.send(ctx, responseRequest{
		Model:  adapter.config.Model,
		Tools:  []computerTool{{Type: "computer"}},
		Input:  "Return one screenshot computer action so the caller can verify computer-use capability. Do not answer with text.",
		Stream: adapter.config.Stream,
	})
	if failure != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProviderCapability,
			"OpenAI computer-use capability probe failed",
			failure.Recoverable,
		)
	}
	calls, _, parseFailure := parseResponse(response)
	if parseFailure != nil || len(calls) == 0 {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProviderCapability,
			"OpenAI Provider did not return a computer call",
			false,
		)
	}
	for _, call := range calls {
		if len(call.Actions) == 0 {
			continue
		}
		var action providerAction
		if json.Unmarshal(call.Actions[0], &action) == nil &&
			action.Type == "screenshot" {
			return nil
		}
	}
	return browserprotocol.NewFailure(
		browserprotocol.ErrorProviderCapability,
		"OpenAI Provider did not return the required screenshot action",
		false,
	)
}

func (session *Session) Reset() {
	if session != nil {
		session.previousResponseID = ""
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
		return nil, errors.New("OpenAI local broker authorization is invalid")
	}
	if config.Model == "" || len(config.Model) > 256 {
		return nil, errors.New("OpenAI computer-use model is invalid")
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 2
	}
	if config.MaxAttempts < 1 || config.MaxAttempts > 3 {
		return nil, errors.New("OpenAI Provider attempt limit is invalid")
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = 100 * time.Millisecond
	}
	if config.RetryDelay < 0 || config.RetryDelay > 5*time.Second {
		return nil, errors.New("OpenAI Provider retry delay is invalid")
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
			"OpenAI browser adapter is not configured",
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

	chainID := session.previousResponseID
	request := responseRequest{
		Model:              adapter.config.Model,
		Tools:              []computerTool{{Type: "computer"}},
		Input:              normalized.Task,
		PreviousResponseID: chainID,
		Stream:             adapter.config.Stream,
	}
	result := browserprovider.Result{}
	for result.Turns < normalized.MaxTurns {
		if err := ctx.Err(); err != nil {
			return browserprovider.Result{}, browserprovider.ContextFailure(err)
		}
		result.Turns++
		response, requestFailure := adapter.send(ctx, request)
		if requestFailure != nil {
			return browserprovider.Result{}, requestFailure
		}
		chainID = response.ID
		calls, finalText, parseFailure := parseResponse(response)
		if parseFailure != nil {
			return browserprovider.Result{}, parseFailure
		}
		if len(calls) == 0 {
			result.FinalText = finalText
			session.previousResponseID = chainID
			return result, nil
		}
		if len(calls) > maxComputerCallsPerTurn {
			return browserprovider.Result{}, browserprovider.ProviderOutputFailure(
				"OpenAI returned too many computer calls",
			)
		}

		outputs := make([]computerCallOutput, 0, len(calls))
		for _, call := range calls {
			if len(call.PendingSafetyChecks) > 0 {
				return browserprovider.Result{}, browserprotocol.NewFailure(
					browserprotocol.ErrorUserActionRequired,
					"OpenAI requested a safety acknowledgement that requires user action",
					false,
				)
			}
			if len(call.Actions) == 0 || len(call.Actions) > maxActionsPerCall {
				return browserprovider.Result{}, browserprovider.ProviderOutputFailure(
					"OpenAI computer call has an invalid action count",
				)
			}
			var observation browserprotocol.Observation
			for _, rawAction := range call.Actions {
				if result.Actions >= normalized.MaxActions {
					return browserprovider.Result{}, browserprotocol.NewFailure(
						browserprotocol.ErrorActionLimitExceeded,
						"OpenAI browser action limit was exceeded",
						false,
					)
				}
				actions, translateFailure := translateAction(rawAction)
				if translateFailure != nil {
					return browserprovider.Result{}, translateFailure
				}
				for _, action := range actions {
					if result.Actions >= normalized.MaxActions {
						return browserprovider.Result{}, browserprotocol.NewFailure(
							browserprotocol.ErrorActionLimitExceeded,
							"OpenAI browser action limit was exceeded",
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
			}
			if observation.Screenshot == nil {
				if result.Actions >= normalized.MaxActions {
					return browserprovider.Result{}, browserprotocol.NewFailure(
						browserprotocol.ErrorActionLimitExceeded,
						"OpenAI browser action limit was exceeded",
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
			dataURL, screenshotFailure := browserprovider.ScreenshotDataURL(observation)
			if screenshotFailure != nil {
				return browserprovider.Result{}, screenshotFailure
			}
			outputs = append(outputs, computerCallOutput{
				Type:   "computer_call_output",
				CallID: call.CallID,
				Output: computerScreenshot{
					Type: "computer_screenshot", ImageURL: dataURL, Detail: "original",
				},
			})
		}
		request = responseRequest{
			Model:              adapter.config.Model,
			Tools:              []computerTool{{Type: "computer"}},
			Input:              outputs,
			PreviousResponseID: chainID,
			Stream:             adapter.config.Stream,
		}
	}
	return browserprovider.Result{}, browserprotocol.NewFailure(
		browserprotocol.ErrorActionLimitExceeded,
		"OpenAI browser turn limit was exceeded",
		false,
	)
}

func (adapter *Adapter) send(
	ctx context.Context,
	payload responseRequest,
) (responseEnvelope, *browserprotocol.Failure) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return responseEnvelope{}, browserprovider.ProviderOutputFailure(
			"OpenAI request could not be encoded",
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
			return responseEnvelope{}, browserprovider.ContextFailure(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+adapter.config.LocalAuthorization)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", responseAccept(payload.Stream))
		response, requestErr := adapter.config.Client.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return responseEnvelope{}, browserprovider.ContextFailure(ctx.Err())
			}
			if attempt < adapter.config.MaxAttempts {
				if failure := waitRetry(ctx, adapter.config.RetryDelay); failure != nil {
					return responseEnvelope{}, failure
				}
				continue
			}
			return responseEnvelope{}, browserprovider.ContextFailure(requestErr)
		}
		envelope, retry, responseFailure := decodeHTTPResponse(response, payload.Stream)
		if responseFailure == nil {
			return envelope, nil
		}
		if !retry || attempt == adapter.config.MaxAttempts {
			return responseEnvelope{}, responseFailure
		}
		if failure := waitRetry(ctx, adapter.config.RetryDelay); failure != nil {
			return responseEnvelope{}, failure
		}
	}
	return responseEnvelope{}, browserprovider.ContextFailure(errors.New("OpenAI request failed"))
}

func decodeHTTPResponse(
	response *http.Response,
	stream bool,
) (responseEnvelope, bool, *browserprotocol.Failure) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		retry := response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode == http.StatusRequestTimeout ||
			response.StatusCode >= 500
		return responseEnvelope{}, retry, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"OpenAI computer-use request was rejected",
			retry,
		)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if stream || strings.Contains(contentType, "text/event-stream") {
		envelope, err := decodeSSE(response.Body)
		if err != nil {
			return responseEnvelope{}, false, browserprovider.ProviderOutputFailure(
				"OpenAI stream response is invalid",
			)
		}
		return envelope, false, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if err != nil || len(raw) > maxProviderResponseBytes {
		return responseEnvelope{}, false, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputTooLarge,
			"OpenAI response exceeds the output limit",
			false,
		)
	}
	var envelope responseEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return responseEnvelope{}, false, browserprovider.ProviderOutputFailure(
			"OpenAI response is invalid",
		)
	}
	if failure := validateResponseEnvelope(envelope); failure != nil {
		return responseEnvelope{}, false, failure
	}
	return envelope, false, nil
}

func decodeSSE(reader io.Reader) (responseEnvelope, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, maxProviderResponseBytes+1))
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var eventName string
	var data strings.Builder
	var completed *responseEnvelope
	total := 0
	flush := func() error {
		if data.Len() == 0 {
			eventName = ""
			return nil
		}
		raw := strings.TrimSpace(data.String())
		if raw == "[DONE]" {
			data.Reset()
			eventName = ""
			return nil
		}
		var event struct {
			Type     string           `json:"type"`
			Response responseEnvelope `json:"response"`
			Error    json.RawMessage  `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return err
		}
		eventType := event.Type
		if eventType == "" {
			eventType = eventName
		}
		switch eventType {
		case "response.completed":
			value := event.Response
			completed = &value
		case "error", "response.failed", "response.incomplete":
			return errors.New("OpenAI stream failed")
		}
		data.Reset()
		eventName = ""
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxProviderResponseBytes {
			return responseEnvelope{}, errors.New("OpenAI stream exceeded limit")
		}
		if line == "" {
			if err := flush(); err != nil {
				return responseEnvelope{}, err
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
		return responseEnvelope{}, err
	}
	if err := flush(); err != nil {
		return responseEnvelope{}, err
	}
	if completed == nil {
		return responseEnvelope{}, errors.New("OpenAI stream did not complete")
	}
	if failure := validateResponseEnvelope(*completed); failure != nil {
		return responseEnvelope{}, failure
	}
	return *completed, nil
}

func validateLoopbackEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" {
		return "", errors.New("OpenAI broker endpoint must be a loopback HTTP URL")
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return "", errors.New("OpenAI broker endpoint must use a loopback address")
	}
	if port := parsed.Port(); port == "" {
		return "", errors.New("OpenAI broker endpoint must include a port")
	} else if value, parseErr := strconv.Atoi(port); parseErr != nil || value < 1 || value > 65535 {
		return "", errors.New("OpenAI broker endpoint port is invalid")
	}
	if net.ParseIP(host) == nil {
		return "", errors.New("OpenAI broker endpoint address is invalid")
	}
	return parsed.String(), nil
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
