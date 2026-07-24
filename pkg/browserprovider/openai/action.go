package openai

import (
	"encoding/json"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
)

type providerAction struct {
	Type    string   `json:"type"`
	Button  string   `json:"button"`
	X       *int     `json:"x"`
	Y       *int     `json:"y"`
	ScrollX *int     `json:"scroll_x"`
	ScrollY *int     `json:"scroll_y"`
	Text    string   `json:"text"`
	Keys    []string `json:"keys"`
}

func translateAction(
	raw json.RawMessage,
) ([]browserprotocol.Action, *browserprotocol.Failure) {
	var action providerAction
	if len(raw) == 0 || len(raw) > 64<<10 || json.Unmarshal(raw, &action) != nil {
		return nil, browserprovider.ProviderOutputFailure(
			"OpenAI returned an invalid computer action",
		)
	}
	switch action.Type {
	case "click":
		if action.Button != "" && action.Button != "left" {
			return nil, browserprotocol.NewFailure(
				browserprotocol.ErrorActionRejected,
				"OpenAI requested a non-left mouse action",
				false,
			)
		}
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionClick, X: action.X, Y: action.Y,
		})
	case "type":
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionTypeNonSecret, Text: action.Text,
		})
	case "scroll":
		return validatedActions(browserprotocol.Action{
			Kind:   browserprotocol.ActionScroll,
			DeltaX: action.ScrollX,
			DeltaY: action.ScrollY,
		})
	case "keypress":
		if len(action.Keys) == 0 || len(action.Keys) > 8 {
			return nil, browserprovider.ProviderOutputFailure(
				"OpenAI keypress action is invalid",
			)
		}
		translated := make([]browserprotocol.Action, 0, len(action.Keys))
		for _, key := range action.Keys {
			normalized, ok := normalizeKey(key)
			if !ok {
				return nil, browserprotocol.NewFailure(
					browserprotocol.ErrorActionRejected,
					"OpenAI requested a key that is not allowed",
					false,
				)
			}
			translated = append(translated, browserprotocol.Action{
				Kind: browserprotocol.ActionKeypress, Key: normalized,
			})
		}
		return validatedActions(translated...)
	case "wait":
		duration := 2000
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionWait, DurationMS: &duration,
		})
	case "screenshot":
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionScreenshot,
		})
	default:
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorActionRejected,
			"OpenAI requested an unsupported computer action",
			false,
		)
	}
}

func validatedActions(
	actions ...browserprotocol.Action,
) ([]browserprotocol.Action, *browserprotocol.Failure) {
	for _, action := range actions {
		if failure := action.Validate(); failure != nil {
			return nil, browserprovider.ProviderOutputFailure(
				"OpenAI computer action failed validation",
			)
		}
	}
	return actions, nil
}

func normalizeKey(value string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "ENTER", "RETURN":
		return "Enter", true
	case "TAB":
		return "Tab", true
	case "ESC", "ESCAPE":
		return "Escape", true
	case "ARROWUP", "UP":
		return "ArrowUp", true
	case "ARROWDOWN", "DOWN":
		return "ArrowDown", true
	case "ARROWLEFT", "LEFT":
		return "ArrowLeft", true
	case "ARROWRIGHT", "RIGHT":
		return "ArrowRight", true
	case "PAGEUP":
		return "PageUp", true
	case "PAGEDOWN":
		return "PageDown", true
	case "HOME":
		return "Home", true
	case "END":
		return "End", true
	case "BACKSPACE":
		return "Backspace", true
	case "DELETE":
		return "Delete", true
	case "SPACE", " ":
		return "Space", true
	default:
		return "", false
	}
}
