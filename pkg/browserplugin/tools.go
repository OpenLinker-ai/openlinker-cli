package browserplugin

import (
	"errors"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const maxBatchActions = 8

func browserToolDefinitions() []toolDefinition {
	stringProperty := func(description string) map[string]any {
		return map[string]any{
			"type":        "string",
			"description": description,
		}
	}
	integerProperty := func(description string) map[string]any {
		return map[string]any{
			"type":        "integer",
			"description": description,
		}
	}
	actionProperties := map[string]any{
		"kind": map[string]any{
			"type": "string",
			"enum": []string{
				"navigate",
				"click",
				"type_non_secret",
				"scroll",
				"keypress",
				"select",
				"wait",
				"back",
				"forward",
				"screenshot",
			},
		},
		"url":         stringProperty("Public HTTP(S) URL for navigate"),
		"x":           integerProperty("Viewport X coordinate"),
		"y":           integerProperty("Viewport Y coordinate"),
		"delta_x":     integerProperty("Horizontal scroll delta"),
		"delta_y":     integerProperty("Vertical scroll delta"),
		"text":        stringProperty("Non-secret text; credentials are forbidden"),
		"key":         stringProperty("Allowed keyboard key"),
		"value":       stringProperty("Select option value"),
		"duration_ms": integerProperty("Wait duration in milliseconds"),
	}
	return []toolDefinition{{
		Name:  "browser_session",
		Title: "Use isolated Browser session",
		Description: "Observe or operate the container-isolated Browser attached to the current client conversation. " +
			"Identity is supplied by the trusted worker, not tool arguments. Never enter credentials or perform high-impact actions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{
					"type": "string",
					"enum": []string{
						"observe",
						"act",
						"checkpoint",
						"close",
					},
				},
				"actions": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": maxBatchActions,
					"items": map[string]any{
						"type":                 "object",
						"properties":           actionProperties,
						"required":             []string{"kind"},
						"additionalProperties": false,
					},
					"description": "Required only for act. Multi-action batches may contain only scroll, wait, and screenshot.",
				},
			},
			"required":             []string{"operation"},
			"additionalProperties": false,
		},
		Annotations: map[string]any{
			"readOnlyHint":    false,
			"destructiveHint": false,
			"idempotentHint":  false,
			"openWorldHint":   true,
		},
	}}
}

func validateToolArguments(arguments toolArguments) error {
	switch arguments.Operation {
	case "observe", "checkpoint", "close":
		if len(arguments.Actions) != 0 {
			return errors.New("Browser operation does not accept actions")
		}
		return nil
	case "act":
		if len(arguments.Actions) < 1 || len(arguments.Actions) > maxBatchActions {
			return errors.New("Browser act requires one to eight actions")
		}
	default:
		return errors.New("Browser operation is invalid")
	}
	for _, action := range arguments.Actions {
		if failure := action.Validate(); failure != nil {
			return failure
		}
	}
	if len(arguments.Actions) > 1 {
		for _, action := range arguments.Actions {
			switch action.Kind {
			case browserprotocol.ActionScroll,
				browserprotocol.ActionWait,
				browserprotocol.ActionScreenshot:
			default:
				return errors.New(
					"multi-action Browser batches may contain only scroll, wait, and screenshot",
				)
			}
		}
	}
	return nil
}
