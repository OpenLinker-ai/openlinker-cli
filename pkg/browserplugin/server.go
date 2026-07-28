package browserplugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/buildinfo"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

const protocolVersion = "2025-06-18"

type Executor interface {
	Execute(
		context.Context,
		browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure)
}

type Server struct {
	Host             string
	IO               shared.IO
	ClientFactory    func() (Executor, error)
	EvidenceSupplier func() (EvidenceSnapshot, error)

	clientMu        sync.Mutex
	client          Executor
	actionMu        sync.Mutex
	stateMu         sync.RWMutex
	closed          bool
	evidenceMu      sync.Mutex
	evidenceKey     string
	evidenceEmitted bool
}

type EvidenceSnapshot struct {
	Environment      browserprotocol.EnvironmentEvidence
	BrowserSessionID string
	SessionEpoch     uint64
	ControlEpoch     uint64
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments map[string]any  `json:"arguments,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

type toolResult struct {
	Content           []contentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type toolArguments struct {
	Operation   string                          `json:"operation"`
	Observation browserprotocol.ObservationMode `json:"observation,omitempty"`
	Actions     []browserprotocol.Action        `json:"actions,omitempty"`
}

type scanResult struct {
	line []byte
	err  error
}

type completedRequest struct {
	key      string
	response rpcResponse
}

type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
}

func (server *Server) Serve(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
) error {
	server.Host = strings.ToLower(strings.TrimSpace(server.Host))
	if server.Host != "codex" && server.Host != "claude" {
		return errors.New("Browser plugin host must be codex or claude")
	}
	if server.IO.Getenv == nil {
		server.IO.Getenv = func(string) string { return "" }
	}
	serveCtx, stop := context.WithCancel(ctx)
	defer stop()
	abandon := make(chan struct{})
	defer close(abandon)
	messages := scanMessages(serveCtx, input)
	responses := make(chan completedRequest, 64)
	cancels := map[string]context.CancelFunc{}
	pending := 0
	inputOpen := true
	encoder := json.NewEncoder(output)
	var outputMu sync.Mutex
	for inputOpen || pending > 0 {
		select {
		case <-ctx.Done():
			stop()
			cancelRequests(cancels)
			return nil
		case scanned, ok := <-messages:
			if !ok {
				inputOpen = false
				messages = nil
				stop()
				cancelRequests(cancels)
				continue
			}
			if scanned.err != nil {
				stop()
				cancelRequests(cancels)
				return scanned.err
			}
			line := bytes.TrimSpace(scanned.line)
			if len(line) == 0 {
				continue
			}
			var request rpcRequest
			if err := json.Unmarshal(line, &request); err != nil {
				outputMu.Lock()
				err = encoder.Encode(rpcResponse{
					JSONRPC: "2.0",
					ID:      json.RawMessage("null"),
					Error:   &rpcError{Code: -32700, Message: "Parse error"},
				})
				outputMu.Unlock()
				if err != nil {
					return err
				}
				continue
			}
			if len(request.ID) == 0 {
				if request.Method == "notifications/cancelled" {
					cancelRequest(request.Params, cancels)
				}
				continue
			}
			key := requestKey(request.ID)
			if _, exists := cancels[key]; exists {
				outputMu.Lock()
				err := encoder.Encode(rpcResponse{
					JSONRPC: "2.0",
					ID:      request.ID,
					Error:   &rpcError{Code: -32600, Message: "Duplicate request id"},
				})
				outputMu.Unlock()
				if err != nil {
					return err
				}
				continue
			}
			requestCtx, cancel := context.WithCancel(serveCtx)
			cancels[key] = cancel
			pending++
			go func() {
				completed := completedRequest{
					key:      key,
					response: server.handle(requestCtx, request),
				}
				select {
				case responses <- completed:
				case <-abandon:
				}
			}()
		case completed := <-responses:
			if cancel, ok := cancels[completed.key]; ok {
				cancel()
				delete(cancels, completed.key)
				pending--
			}
			outputMu.Lock()
			err := encoder.Encode(completed.response)
			outputMu.Unlock()
			if err != nil {
				stop()
				cancelRequests(cancels)
				return err
			}
		}
	}
	return nil
}

func (server *Server) handle(
	ctx context.Context,
	request rpcRequest,
) rpcResponse {
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "openlinker-browser-" + server.Host,
				"version": buildinfo.Version,
			},
			"instructions": "Use the Browser tool for container-isolated browser interaction. Never enter credentials or perform high-impact actions.",
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": browserToolDefinitions()}
	case "tools/call":
		var params toolCallParams
		if err := decodeStrict(request.Params, &params); err != nil ||
			params.Name != "browser_session" {
			response.Error = &rpcError{
				Code:    -32602,
				Message: "Invalid tools/call parameters",
			}
			return response
		}
		result, err := server.callBrowser(ctx, params.Arguments)
		if err != nil {
			result = browserErrorResult(err)
		}
		if evidence, ok := server.takeAttachmentEvidence(); ok {
			addAttachmentEvidence(&result, evidence)
		}
		response.Result = result
	default:
		response.Error = &rpcError{Code: -32601, Message: "Method not found"}
	}
	return response
}

func (server *Server) takeAttachmentEvidence() (
	browserprotocol.EnvironmentEvidence,
	bool,
) {
	if server.EvidenceSupplier == nil {
		return browserprotocol.EnvironmentEvidence{}, false
	}
	snapshot, err := server.EvidenceSupplier()
	if err != nil ||
		snapshot.Environment.Validate() != nil ||
		snapshot.BrowserSessionID == "" ||
		snapshot.SessionEpoch == 0 ||
		snapshot.ControlEpoch == 0 {
		return browserprotocol.EnvironmentEvidence{}, false
	}
	key := fmt.Sprintf(
		"%s:%d:%d",
		snapshot.BrowserSessionID,
		snapshot.SessionEpoch,
		snapshot.ControlEpoch,
	)
	server.evidenceMu.Lock()
	defer server.evidenceMu.Unlock()
	if server.evidenceKey != key {
		server.evidenceKey = key
		server.evidenceEmitted = false
	}
	if server.evidenceEmitted {
		return browserprotocol.EnvironmentEvidence{}, false
	}
	server.evidenceEmitted = true
	return snapshot.Environment, true
}

func addAttachmentEvidence(
	result *toolResult,
	evidence browserprotocol.EnvironmentEvidence,
) {
	if result == nil {
		return
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return
	}
	structured["attachment_evidence"] = map[string]any{
		"browser_engine":        evidence.BrowserEngine,
		"browser_distribution":  evidence.BrowserDistribution,
		"browser_major_version": evidence.BrowserMajorVersion,
		"browser_locale":        evidence.BrowserLocale,
		"browser_timezone":      evidence.BrowserTimezone,
		"font_contract_version": evidence.FontContractVersion,
		"font_manifest_sha256":  evidence.FontManifestSHA256,
	}
}

func (server *Server) callBrowser(
	ctx context.Context,
	rawArguments map[string]any,
) (toolResult, error) {
	var arguments toolArguments
	raw, err := json.Marshal(rawArguments)
	if err != nil || decodeStrict(raw, &arguments) != nil {
		return toolResult{}, errors.New("Browser tool arguments are invalid")
	}
	if err := validateToolArguments(arguments); err != nil {
		return toolResult{}, err
	}
	server.actionMu.Lock()
	defer server.actionMu.Unlock()
	server.stateMu.RLock()
	closed := server.closed
	server.stateMu.RUnlock()
	if closed {
		return toolResult{}, errors.New("Browser attachment is closed")
	}
	if arguments.Operation == "close" {
		client, err := server.browserClient()
		if err != nil {
			return toolResult{}, err
		}
		if _, failure := client.Execute(ctx, browserprotocol.Action{
			Kind: browserprotocol.ActionClose,
		}); failure != nil {
			return toolResult{}, failure
		}
		server.stateMu.Lock()
		server.closed = true
		server.stateMu.Unlock()
		return toolResult{
			Content: []contentBlock{{
				Type: "text",
				Text: "Browser attachment closed for this client session.",
			}},
			StructuredContent: map[string]any{
				"operation": "close",
				"status":    "closed",
			},
		}, nil
	}
	client, err := server.browserClient()
	if err != nil {
		return toolResult{}, err
	}
	actions := arguments.Actions
	if arguments.Operation == "observe" {
		actions = []browserprotocol.Action{{
			Kind:        browserprotocol.ActionScreenshot,
			Observation: effectiveToolObservation(arguments.Observation),
		}}
	} else if arguments.Operation == "checkpoint" {
		actions = []browserprotocol.Action{{
			Kind:        browserprotocol.ActionCheckpoint,
			Observation: browserprotocol.ObservationNone,
		}}
	} else {
		actions = append([]browserprotocol.Action(nil), actions...)
		if len(actions) > 1 {
			actions = []browserprotocol.Action{{
				Kind:        browserprotocol.ActionBatch,
				Observation: effectiveToolObservation(arguments.Observation),
				Actions:     actions,
			}}
		} else {
			actions[0].Observation = effectiveToolObservation(
				arguments.Observation,
			)
		}
	}
	var observation browserprotocol.Observation
	for _, action := range actions {
		var failure *browserprotocol.Failure
		observation, failure = client.Execute(ctx, action)
		if failure != nil {
			return toolResult{}, failure
		}
	}
	return observationResult(arguments.Operation, observation), nil
}

func effectiveToolObservation(
	mode browserprotocol.ObservationMode,
) browserprotocol.ObservationMode {
	return mode.Effective()
}

func (server *Server) browserClient() (Executor, error) {
	server.clientMu.Lock()
	defer server.clientMu.Unlock()
	if server.client != nil {
		return server.client, nil
	}
	factory := server.ClientFactory
	if factory == nil {
		factory = func() (Executor, error) {
			return browserclient.NewFromEnv(server.IO.Getenv)
		}
	}
	client, err := factory()
	if err != nil {
		return nil, err
	}
	server.client = client
	return server.client, nil
}

func observationResult(
	operation string,
	observation browserprotocol.Observation,
) toolResult {
	structured := map[string]any{
		"operation":     operation,
		"status":        "ok",
		"page_state_id": observation.PageStateID,
	}
	if observation.Viewport != nil {
		structured["viewport"] = map[string]any{
			"width":  observation.Viewport.Width,
			"height": observation.Viewport.Height,
		}
	}
	if observation.NavigationGeneration > 0 {
		structured["navigation_generation"] = observation.NavigationGeneration
	}
	if observation.Origin != "" {
		structured["origin"] = observation.Origin
	}
	if observation.Title != "" {
		structured["title"] = observation.Title
	}
	if len(observation.AXTree) > 0 {
		var value any
		if json.Unmarshal(observation.AXTree, &value) == nil {
			structured["ax_tree"] = value
		}
	}
	if len(observation.DOMDiff) > 0 {
		var value any
		if json.Unmarshal(observation.DOMDiff, &value) == nil {
			structured["dom_diff"] = value
		}
	}
	if observation.AXTreeTimedOut {
		structured["ax_tree_timed_out"] = true
	}
	if observation.DOMDiffTimedOut {
		structured["dom_diff_timed_out"] = true
	}
	if observation.ClickEffect != "" {
		structured["click_effect"] = observation.ClickEffect
	}
	if observation.TargetCategory != "" {
		structured["target_category"] = observation.TargetCategory
	}
	if observation.SiteOutcome != "" {
		structured["site_outcome"] = observation.SiteOutcome
	}
	if observation.ClassifierRulesVersion != "" {
		structured["classifier_rules_version"] =
			observation.ClassifierRulesVersion
	}
	if observation.ChallengeReleaseUnavailable {
		structured["challenge_release_unavailable"] = true
	}
	content := []contentBlock{{
		Type: "text",
		Text: "Browser observation returned in structured content.",
	}}
	if observation.Screenshot != nil {
		structured["screenshot"] = map[string]any{
			"mime_type": observation.Screenshot.MIMEType,
			"width":     observation.Screenshot.Width,
			"height":    observation.Screenshot.Height,
		}
		content = append(content, contentBlock{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(observation.Screenshot.Data),
			MIMEType: observation.Screenshot.MIMEType,
		})
	}
	return toolResult{Content: content, StructuredContent: structured}
}

func browserErrorResult(err error) toolResult {
	code := browserprotocol.ErrorInternal
	message := "Browser tool failed"
	var failure *browserprotocol.Failure
	if errors.As(err, &failure) && failure != nil {
		code = failure.Code
		message = failure.Message
	}
	structured := map[string]any{
		"status":  "error",
		"code":    code,
		"message": message,
	}
	if failure != nil && failure.ActionIndex != nil {
		structured["failed_action_index"] = *failure.ActionIndex
		structured["completed_actions"] = *failure.ActionIndex
	}
	if failure != nil && failure.TargetCategory != "" {
		structured["target_category"] = failure.TargetCategory
		structured["page_state_id"] = failure.PageStateID
		structured["navigation_generation"] = failure.NavigationGeneration
	}
	if failure != nil && failure.BlockedClickNavigationAttemptsRemaining != nil {
		structured["blocked_click_navigation_attempts_remaining"] =
			*failure.BlockedClickNavigationAttemptsRemaining
	}
	if failure != nil && failure.BlockedClickRunAttemptsRemaining != nil {
		structured["blocked_click_run_attempts_remaining"] =
			*failure.BlockedClickRunAttemptsRemaining
	}
	if failure != nil && failure.SiteOutcome != "" {
		structured["site_outcome"] = failure.SiteOutcome
	}
	if failure != nil && failure.RetryAfterMS != nil {
		structured["retry_after_ms"] = *failure.RetryAfterMS
	}
	if failure != nil && failure.ClassifierRulesVersion != "" {
		structured["classifier_rules_version"] =
			failure.ClassifierRulesVersion
	}
	if failure != nil && failure.ConsecutiveAccessDenials != nil {
		structured["consecutive_access_denials"] =
			*failure.ConsecutiveAccessDenials
	}
	if failure != nil && failure.OriginBlockedForAttachment {
		structured["origin_blocked_for_attachment"] = true
	}
	if failure != nil && failure.ChallengeReleaseUnavailable {
		structured["challenge_release_unavailable"] = true
	}
	if failure != nil && failure.HumanControlAvailable {
		structured["human_control_available"] = true
	}
	return toolResult{
		Content: []contentBlock{{
			Type: "text",
			Text: fmt.Sprintf("%s: %s", code, message),
		}},
		StructuredContent: structured,
		IsError:           true,
	}
}

func scanMessages(ctx context.Context, input io.Reader) <-chan scanResult {
	results := make(chan scanResult)
	go func() {
		defer close(results)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case results <- scanResult{line: line}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case results <- scanResult{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return results
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func requestKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

func cancelRequest(
	raw json.RawMessage,
	cancels map[string]context.CancelFunc,
) {
	var params cancelledParams
	if json.Unmarshal(raw, &params) != nil || len(params.RequestID) == 0 {
		return
	}
	if cancel := cancels[requestKey(params.RequestID)]; cancel != nil {
		cancel()
	}
}

func cancelRequests(cancels map[string]context.CancelFunc) {
	for _, cancel := range cancels {
		cancel()
	}
}
