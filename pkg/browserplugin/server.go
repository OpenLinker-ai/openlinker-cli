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
	Host          string
	IO            shared.IO
	ClientFactory func() (Executor, error)

	clientMu sync.Mutex
	client   Executor
	actionMu sync.Mutex
	stateMu  sync.RWMutex
	closed   bool
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
	Operation string                   `json:"operation"`
	Actions   []browserprotocol.Action `json:"actions,omitempty"`
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
			response.Result = browserErrorResult(err)
		} else {
			response.Result = result
		}
	default:
		response.Error = &rpcError{Code: -32601, Message: "Method not found"}
	}
	return response
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
	if arguments.Operation == "observe" ||
		arguments.Operation == "checkpoint" {
		actions = []browserprotocol.Action{{
			Kind: browserprotocol.ActionScreenshot,
		}}
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
	content := []contentBlock{{
		Type: "text",
		Text: "Browser observation returned in structured content.",
	}}
	if observation.Screenshot != nil {
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
	return toolResult{
		Content: []contentBlock{{
			Type: "text",
			Text: fmt.Sprintf("%s: %s", code, message),
		}},
		StructuredContent: map[string]any{
			"status":  "error",
			"code":    code,
			"message": message,
		},
		IsError: true,
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
