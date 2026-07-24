package anthropic

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
)

type providerAction struct {
	Action          string   `json:"action"`
	Text            string   `json:"text"`
	Key             string   `json:"key"`
	Coordinate      []int    `json:"coordinate"`
	ScrollDirection string   `json:"scroll_direction"`
	ScrollAmount    *int     `json:"scroll_amount"`
	Duration        *float64 `json:"duration"`
}

func translateAction(
	raw json.RawMessage,
) ([]browserprotocol.Action, *browserprotocol.Failure) {
	var action providerAction
	if len(raw) == 0 || len(raw) > 64<<10 || json.Unmarshal(raw, &action) != nil {
		return nil, browserprovider.ProviderOutputFailure(
			"Anthropic returned an invalid computer action",
		)
	}
	switch action.Action {
	case "left_click":
		if action.Key != "" || len(action.Coordinate) != 2 {
			return nil, browserprotocol.NewFailure(
				browserprotocol.ErrorActionRejected,
				"Anthropic requested an unsupported click action",
				false,
			)
		}
		x, y := action.Coordinate[0], action.Coordinate[1]
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionClick, X: &x, Y: &y,
		})
	case "type":
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionTypeNonSecret, Text: action.Text,
		})
	case "key":
		key, ok := normalizeKey(action.Text)
		if !ok {
			return nil, browserprotocol.NewFailure(
				browserprotocol.ErrorActionRejected,
				"Anthropic requested a key that is not allowed",
				false,
			)
		}
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionKeypress, Key: key,
		})
	case "scroll":
		if len(action.Coordinate) != 0 ||
			action.ScrollAmount == nil ||
			*action.ScrollAmount < 1 ||
			*action.ScrollAmount > 327 {
			return nil, browserprovider.ProviderOutputFailure(
				"Anthropic scroll action is invalid",
			)
		}
		delta := *action.ScrollAmount * 100
		var deltaX, deltaY int
		switch action.ScrollDirection {
		case "up":
			deltaY = -delta
		case "down":
			deltaY = delta
		case "left":
			deltaX = -delta
		case "right":
			deltaX = delta
		default:
			return nil, browserprovider.ProviderOutputFailure(
				"Anthropic scroll direction is invalid",
			)
		}
		return validatedActions(browserprotocol.Action{
			Kind: browserprotocol.ActionScroll, DeltaX: &deltaX, DeltaY: &deltaY,
		})
	case "wait":
		if action.Duration == nil ||
			math.IsNaN(*action.Duration) ||
			math.IsInf(*action.Duration, 0) {
			return nil, browserprovider.ProviderOutputFailure(
				"Anthropic wait duration is invalid",
			)
		}
		duration := int(math.Round(*action.Duration * 1000))
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
			"Anthropic requested an unsupported computer action",
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
				"Anthropic computer action failed validation",
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
