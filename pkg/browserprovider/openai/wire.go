package openai

import (
	"encoding/json"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
)

type responseRequest struct {
	Model              string         `json:"model"`
	Tools              []computerTool `json:"tools"`
	Input              any            `json:"input"`
	PreviousResponseID string         `json:"previous_response_id,omitempty"`
	Stream             bool           `json:"stream"`
}

type computerTool struct {
	Type string `json:"type"`
}

type computerCallOutput struct {
	Type   string             `json:"type"`
	CallID string             `json:"call_id"`
	Output computerScreenshot `json:"output"`
}

type computerScreenshot struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
	Detail   string `json:"detail"`
}

type responseEnvelope struct {
	ID     string           `json:"id"`
	Status string           `json:"status"`
	Output []responseOutput `json:"output"`
}

type responseOutput struct {
	Type                string            `json:"type"`
	CallID              string            `json:"call_id"`
	Actions             []json.RawMessage `json:"actions"`
	PendingSafetyChecks []safetyCheck     `json:"pending_safety_checks"`
	Content             []messagePart     `json:"content"`
}

type safetyCheck struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type messagePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type parsedComputerCall struct {
	CallID              string
	Actions             []json.RawMessage
	PendingSafetyChecks []safetyCheck
}

func validateResponseEnvelope(
	response responseEnvelope,
) *browserprotocol.Failure {
	if response.ID == "" || len(response.ID) > 512 {
		return browserprovider.ProviderOutputFailure(
			"OpenAI response identifier is missing or invalid",
		)
	}
	switch response.Status {
	case "", "completed":
	default:
		return browserprovider.ProviderOutputFailure(
			"OpenAI response did not complete successfully",
		)
	}
	if len(response.Output) > 128 {
		return browserprovider.ProviderOutputFailure(
			"OpenAI response contains too many output items",
		)
	}
	return nil
}

func parseResponse(
	response responseEnvelope,
) ([]parsedComputerCall, string, *browserprotocol.Failure) {
	if failure := validateResponseEnvelope(response); failure != nil {
		return nil, "", failure
	}
	var calls []parsedComputerCall
	var text strings.Builder
	for _, item := range response.Output {
		switch item.Type {
		case "computer_call":
			if item.CallID == "" || len(item.CallID) > 512 {
				return nil, "", browserprovider.ProviderOutputFailure(
					"OpenAI computer call identifier is invalid",
				)
			}
			calls = append(calls, parsedComputerCall{
				CallID:              item.CallID,
				Actions:             item.Actions,
				PendingSafetyChecks: item.PendingSafetyChecks,
			})
		case "message":
			for _, part := range item.Content {
				if part.Type != "output_text" {
					continue
				}
				if text.Len()+len(part.Text) > browserprovider.MaxFinalTextBytes {
					return nil, "", browserprotocol.NewFailure(
						browserprotocol.ErrorOutputTooLarge,
						"OpenAI final text exceeds the output limit",
						false,
					)
				}
				text.WriteString(part.Text)
			}
		}
	}
	if len(calls) == 0 && strings.TrimSpace(text.String()) == "" {
		return nil, "", browserprovider.ProviderOutputFailure(
			"OpenAI response contains neither a computer call nor final text",
		)
	}
	return calls, strings.TrimSpace(text.String()), nil
}
