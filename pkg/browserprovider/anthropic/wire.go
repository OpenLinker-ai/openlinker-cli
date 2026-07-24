package anthropic

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
)

type messageRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	Tools     []computerTool `json:"tools"`
	Messages  []message      `json:"messages"`
	Stream    bool           `json:"stream"`
}

type computerTool struct {
	Type            string `json:"type"`
	Name            string `json:"name"`
	DisplayWidthPX  int    `json:"display_width_px"`
	DisplayHeightPX int    `json:"display_height_px"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type messageResponse struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Role       string            `json:"role"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
}

type contentBlockHeader struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

type parsedToolCall struct {
	ID    string
	Input json.RawMessage
}

type toolResultBlock struct {
	Type      string              `json:"type"`
	ToolUseID string              `json:"tool_use_id"`
	Content   []toolResultContent `json:"content"`
}

type toolResultContent struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

func validateMessageResponse(
	response messageResponse,
) *browserprotocol.Failure {
	if response.ID == "" || len(response.ID) > 512 ||
		(response.Type != "" && response.Type != "message") ||
		(response.Role != "" && response.Role != "assistant") {
		return browserprovider.ProviderOutputFailure(
			"Anthropic response envelope is invalid",
		)
	}
	if len(response.Content) == 0 || len(response.Content) > 128 {
		return browserprovider.ProviderOutputFailure(
			"Anthropic response content is empty or too large",
		)
	}
	return nil
}

func parseResponse(
	response messageResponse,
) ([]parsedToolCall, string, *browserprotocol.Failure) {
	if failure := validateMessageResponse(response); failure != nil {
		return nil, "", failure
	}
	var calls []parsedToolCall
	var text strings.Builder
	for _, raw := range response.Content {
		var header contentBlockHeader
		if len(raw) == 0 || len(raw) > 8<<20 || json.Unmarshal(raw, &header) != nil {
			return nil, "", browserprovider.ProviderOutputFailure(
				"Anthropic content block is invalid",
			)
		}
		switch header.Type {
		case "tool_use":
			if header.ID == "" || len(header.ID) > 512 ||
				header.Name != "computer" || len(header.Input) == 0 {
				return nil, "", browserprovider.ProviderOutputFailure(
					"Anthropic computer tool call is invalid",
				)
			}
			calls = append(calls, parsedToolCall{ID: header.ID, Input: header.Input})
		case "text":
			if text.Len()+len(header.Text) > browserprovider.MaxFinalTextBytes {
				return nil, "", browserprotocol.NewFailure(
					browserprotocol.ErrorOutputTooLarge,
					"Anthropic final text exceeds the output limit",
					false,
				)
			}
			text.WriteString(header.Text)
		}
	}
	if len(calls) == 0 && strings.TrimSpace(text.String()) == "" {
		return nil, "", browserprovider.ProviderOutputFailure(
			"Anthropic response contains neither a computer call nor final text",
		)
	}
	return calls, strings.TrimSpace(text.String()), nil
}

type streamCollector struct {
	response messageResponse
	blocks   map[int]*streamBlock
	stopped  bool
}

type streamBlock struct {
	blockType string
	id        string
	name      string
	text      strings.Builder
	input     strings.Builder
}

func newStreamCollector() *streamCollector {
	return &streamCollector{blocks: make(map[int]*streamBlock)}
}

func (collector *streamCollector) consume(eventName string, raw []byte) error {
	var event struct {
		Type         string             `json:"type"`
		Message      messageResponse    `json:"message"`
		Index        int                `json:"index"`
		ContentBlock contentBlockHeader `json:"content_block"`
		Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Error json.RawMessage `json:"error"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &event) != nil {
		return errors.New("invalid Anthropic stream event")
	}
	eventType := event.Type
	if eventType == "" {
		eventType = eventName
	}
	switch eventType {
	case "ping":
		return nil
	case "message_start":
		collector.response.ID = event.Message.ID
		collector.response.Type = event.Message.Type
		collector.response.Role = event.Message.Role
	case "content_block_start":
		if event.Index < 0 || event.Index > 127 || collector.blocks[event.Index] != nil {
			return errors.New("invalid Anthropic stream content index")
		}
		block := &streamBlock{
			blockType: event.ContentBlock.Type,
			id:        event.ContentBlock.ID,
			name:      event.ContentBlock.Name,
		}
		if event.ContentBlock.Text != "" {
			block.text.WriteString(event.ContentBlock.Text)
		}
		input := strings.TrimSpace(string(event.ContentBlock.Input))
		if input != "" && input != "{}" && input != "null" {
			block.input.WriteString(input)
		}
		collector.blocks[event.Index] = block
	case "content_block_delta":
		block := collector.blocks[event.Index]
		if block == nil {
			return errors.New("Anthropic stream delta has no content block")
		}
		switch event.Delta.Type {
		case "text_delta":
			block.text.WriteString(event.Delta.Text)
		case "input_json_delta":
			block.input.WriteString(event.Delta.PartialJSON)
		default:
			return errors.New("unsupported Anthropic stream delta")
		}
	case "content_block_stop":
		return nil
	case "message_delta":
		collector.response.StopReason = event.Delta.StopReason
	case "message_stop":
		collector.stopped = true
	case "error":
		return errors.New("Anthropic stream failed")
	default:
		return errors.New("unsupported Anthropic stream event")
	}
	return nil
}

func (collector *streamCollector) finish() (messageResponse, error) {
	if !collector.stopped {
		return messageResponse{}, errors.New("Anthropic stream did not stop")
	}
	for index := 0; index < len(collector.blocks); index++ {
		block := collector.blocks[index]
		if block == nil {
			return messageResponse{}, errors.New("Anthropic stream content is sparse")
		}
		var value any
		switch block.blockType {
		case "text":
			value = map[string]any{"type": "text", "text": block.text.String()}
		case "tool_use":
			var input any
			if block.input.Len() == 0 || json.Unmarshal([]byte(block.input.String()), &input) != nil {
				return messageResponse{}, errors.New("Anthropic stream tool input is invalid")
			}
			value = map[string]any{
				"type": "tool_use", "id": block.id, "name": block.name, "input": input,
			}
		default:
			return messageResponse{}, errors.New(
				"Anthropic stream content type is unsupported at index " + strconv.Itoa(index),
			)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return messageResponse{}, err
		}
		collector.response.Content = append(collector.response.Content, raw)
	}
	if failure := validateMessageResponse(collector.response); failure != nil {
		return messageResponse{}, failure
	}
	return collector.response, nil
}
