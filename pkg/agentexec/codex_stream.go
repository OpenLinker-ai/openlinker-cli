package agentexec

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

type codexJSONLObserver struct {
	mu      sync.Mutex
	pending []byte
	emit    func(string, any) error
}

func newCodexJSONLObserver(emit func(string, any) error) *codexJSONLObserver {
	return &codexJSONLObserver{emit: emit}
}

func (observer *codexJSONLObserver) Write(value []byte) (int, error) {
	if observer == nil || len(value) == 0 {
		return len(value), nil
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.pending = append(observer.pending, value...)
	observer.drainLines(false)
	return len(value), nil
}

func (observer *codexJSONLObserver) Flush() {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.drainLines(true)
}

func (observer *codexJSONLObserver) drainLines(flush bool) {
	for {
		index := bytes.IndexByte(observer.pending, '\n')
		if index < 0 {
			if flush && len(observer.pending) > 0 {
				observer.observeLine(observer.pending)
				observer.pending = nil
			}
			return
		}
		line := observer.pending[:index]
		observer.pending = observer.pending[index+1:]
		observer.observeLine(line)
	}
}

func (observer *codexJSONLObserver) observeLine(line []byte) {
	if observer.emit == nil {
		return
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var event map[string]any
	if json.Unmarshal(line, &event) != nil {
		return
	}
	payload, ok := codexProgressPayload(event)
	if !ok {
		return
	}
	// Provider progress is best-effort. Losing a display-only event must not
	// terminate a healthy provider process.
	_ = observer.emit("run.status.changed", payload)
}

func codexProgressPayload(event map[string]any) (map[string]any, bool) {
	eventType, _ := event["type"].(string)
	if eventType != "item.started" && eventType != "item.completed" {
		return nil, false
	}
	item, ok := event["item"].(map[string]any)
	if !ok {
		return nil, false
	}
	toolKind := normalizedCodexToolKind(item["type"])
	if toolKind == "" {
		return nil, false
	}
	phase := "started"
	if eventType == "item.completed" {
		phase = "completed"
		if status, _ := item["status"].(string); strings.EqualFold(strings.TrimSpace(status), "failed") {
			phase = "failed"
		}
	}
	return map[string]any{
		"status":    "provider_tool_" + phase,
		"provider":  "codex",
		"phase":     phase,
		"tool_kind": toolKind,
	}, true
}

func normalizedCodexToolKind(value any) string {
	switch strings.ToLower(strings.TrimSpace(stringValue(value))) {
	case "web_search":
		return "web_search"
	case "command_execution":
		return "command"
	case "mcp_tool_call":
		return "mcp_tool"
	case "computer_tool_call":
		return "browser"
	default:
		return ""
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
