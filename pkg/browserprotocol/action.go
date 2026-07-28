package browserprotocol

import "github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol/internal/browsercontract"

func (action Action) Validate() *Failure {
	if failure := action.Observation.Validate(); failure != nil {
		return failure
	}
	switch action.Kind {
	case ActionNavigate:
		if failure := action.requireOnly("url"); failure != nil {
			return failure
		}
		return validateNavigateURL(action.URL)
	case ActionClick:
		if failure := action.requireOnly("x", "y"); failure != nil {
			return failure
		}
		return validateCoordinates(action.X, action.Y)
	case ActionTypeNonSecret:
		if failure := action.requireOnly("text"); failure != nil {
			return failure
		}
		if action.Text == "" || len(action.Text) > 16<<10 {
			return NewFailure(ErrorProtocolInvalid, "type_non_secret text is empty or too large", false)
		}
		return nil
	case ActionScroll:
		if failure := action.allowOnly("delta_x", "delta_y"); failure != nil {
			return failure
		}
		if action.DeltaX == nil && action.DeltaY == nil {
			return NewFailure(ErrorProtocolInvalid, "scroll requires a delta", false)
		}
		deltaX, deltaY := valueOrZero(action.DeltaX), valueOrZero(action.DeltaY)
		if deltaX == 0 && deltaY == 0 {
			return NewFailure(ErrorProtocolInvalid, "scroll delta must be non-zero", false)
		}
		if abs(deltaX) > 32768 || abs(deltaY) > 32768 {
			return NewFailure(ErrorProtocolInvalid, "scroll delta is out of range", false)
		}
		return nil
	case ActionKeypress:
		if failure := action.requireOnly("key"); failure != nil {
			return failure
		}
		if !allowedKey(action.Key) {
			return NewFailure(ErrorProtocolInvalid, "keypress key is not allowed", false)
		}
		return nil
	case ActionSelect:
		return NewFailure(
			ErrorActionRejected,
			"Phase 1 does not allow select controls",
			false,
		)
	case ActionWait:
		if failure := action.requireOnly("duration_ms"); failure != nil {
			return failure
		}
		if action.DurationMS == nil || *action.DurationMS < 1 || *action.DurationMS > 5000 {
			return NewFailure(ErrorProtocolInvalid, "wait duration must be between 1 and 5000 milliseconds", false)
		}
		return nil
	case ActionBack, ActionForward, ActionScreenshot, ActionCheckpoint, ActionClose,
		ActionPreflight:
		return action.requireOnly()
	case ActionBatch:
		if failure := action.requireOnly("actions"); failure != nil {
			return failure
		}
		if len(action.Actions) < 2 || len(action.Actions) > 8 {
			return NewFailure(ErrorProtocolInvalid, "browser batch requires two to eight actions", false)
		}
		for _, nested := range action.Actions {
			if nested.Observation != ObservationDefault {
				return NewFailure(ErrorProtocolInvalid, "browser batch controls the final observation", false)
			}
			switch nested.Kind {
			case ActionScroll, ActionWait, ActionScreenshot:
			default:
				return NewFailure(ErrorProtocolInvalid, "browser batch contains a disallowed action", false)
			}
			if failure := nested.Validate(); failure != nil {
				return failure
			}
		}
		return nil
	default:
		return NewFailure(ErrorProtocolInvalid, "browser action is not allowed", false)
	}
}

func (action Action) requireOnly(fields ...string) *Failure {
	if failure := action.allowOnly(fields...); failure != nil {
		return failure
	}
	present := action.presentFields()
	for _, field := range fields {
		if !present[field] {
			return NewFailure(ErrorProtocolInvalid, "browser action is missing a required field", false)
		}
	}
	return nil
}

func (action Action) allowOnly(fields ...string) *Failure {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	for field, set := range action.presentFields() {
		if set && !allowed[field] {
			return NewFailure(ErrorProtocolInvalid, "browser action contains an unexpected field", false)
		}
	}
	return nil
}

func (action Action) presentFields() map[string]bool {
	return map[string]bool{
		"url":         action.URL != "",
		"x":           action.X != nil,
		"y":           action.Y != nil,
		"delta_x":     action.DeltaX != nil,
		"delta_y":     action.DeltaY != nil,
		"text":        action.Text != "",
		"key":         action.Key != "",
		"value":       action.Value != "",
		"duration_ms": action.DurationMS != nil,
		"actions":     len(action.Actions) != 0,
	}
}

func validateCoordinates(x, y *int) *Failure {
	if x == nil || y == nil {
		return NewFailure(ErrorProtocolInvalid, "browser action requires coordinates", false)
	}
	if *x < 0 || *y < 0 ||
		*x >= browsercontract.ViewportWidth ||
		*y >= browsercontract.ViewportHeight {
		return NewFailure(ErrorProtocolInvalid, "browser action coordinates are out of range", false)
	}
	return nil
}

func allowedKey(value string) bool {
	switch value {
	case "Enter", "Tab", "Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
		"PageUp", "PageDown", "Home", "End", "Backspace", "Delete":
		return true
	default:
		return false
	}
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
