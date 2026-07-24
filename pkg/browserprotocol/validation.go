package browserprotocol

import (
	"encoding/json"
	"strings"
	"time"
)

func (request Request) Validate(now time.Time) *Failure {
	if request.ContractID != ContractID {
		return NewFailure(ErrorProtocolInvalid, "unsupported browser contract", false)
	}
	if !validUUID(request.RequestID) {
		return NewFailure(ErrorProtocolInvalid, "request_id must be a UUID", false)
	}
	if failure := request.Identity.Validate(); failure != nil {
		return failure
	}
	now = now.UTC()
	if request.Deadline.IsZero() || !request.Deadline.After(now) {
		return NewFailure(ErrorDeadlineExceeded, "browser action deadline has elapsed", true)
	}
	if request.Deadline.After(now.Add(MaxActionDeadline)) {
		return NewFailure(ErrorProtocolInvalid, "browser action deadline exceeds the allowed horizon", false)
	}
	return request.Action.Validate()
}

func (identity Identity) Validate() *Failure {
	for label, value := range map[string]string{
		"run_id":          identity.RunID,
		"agent_id":        identity.AgentID,
		"conversation_id": identity.ConversationID,
		"attachment_id":   identity.AttachmentID,
	} {
		if !validUUID(value) {
			return NewFailure(ErrorProtocolInvalid, label+" must be a UUID", false)
		}
	}
	if !validOpaqueID(identity.PrincipalScopeID, 256) {
		return NewFailure(ErrorProtocolInvalid, "principal_scope_id is invalid", false)
	}
	if identity.BrowserGeneration == 0 {
		return NewFailure(ErrorProtocolInvalid, "browser_generation must be positive", false)
	}
	if identity.ControlEpoch == 0 {
		return NewFailure(ErrorProtocolInvalid, "control_epoch must be positive", false)
	}
	return nil
}

func (observation Observation) Validate() *Failure {
	if observation.PageStateID == "" || len(observation.PageStateID) > 256 {
		return NewFailure(ErrorOutputInvalid, "page_state_id is empty or too large", false)
	}
	if observation.Screenshot != nil {
		if observation.Screenshot.MIMEType != "image/png" && observation.Screenshot.MIMEType != "image/jpeg" {
			return NewFailure(ErrorOutputInvalid, "screenshot MIME type is not allowed", false)
		}
		if len(observation.Screenshot.Data) == 0 || len(observation.Screenshot.Data) > MaxScreenshotBytes {
			return NewFailure(ErrorOutputTooLarge, "screenshot exceeds the output limit", false)
		}
	}
	for label, raw := range map[string]json.RawMessage{"ax_tree": observation.AXTree, "dom_diff": observation.DOMDiff} {
		if len(raw) == 0 {
			continue
		}
		limit := MaxAXTreeBytes
		if label == "dom_diff" {
			limit = MaxDOMDiffBytes
		}
		if len(raw) > limit {
			return NewFailure(ErrorOutputTooLarge, label+" exceeds the output limit", false)
		}
		if !json.Valid(raw) {
			return NewFailure(ErrorOutputInvalid, label+" is not valid JSON", false)
		}
	}
	if len(observation.Origin) > 512 || len(observation.Title) > 2048 {
		return NewFailure(ErrorOutputTooLarge, "page metadata exceeds the output limit", false)
	}
	return nil
}

func ValidateFailure(failure *Failure) *Failure {
	if failure == nil {
		return NewFailure(ErrorOutputInvalid, "browser failure is missing", false)
	}
	switch failure.Code {
	case ErrorProtocolInvalid,
		ErrorRequestTooLarge,
		ErrorOutputTooLarge,
		ErrorUnauthorized,
		ErrorIdentityMismatch,
		ErrorStaleControlEpoch,
		ErrorRequestReplayed,
		ErrorDeadlineExceeded,
		ErrorRuntimeUnavailable,
		ErrorEgressUnavailable,
		ErrorTargetBlocked,
		ErrorProfileLocked,
		ErrorProfileCorrupt,
		ErrorConversationRecovery,
		ErrorUserActionRequired,
		ErrorHighImpactActionBlocked,
		ErrorViewerUnavailable,
		ErrorActionLimitExceeded,
		ErrorCanceled,
		ErrorActionRejected,
		ErrorOutputInvalid,
		ErrorInternal:
	default:
		return NewFailure(ErrorOutputInvalid, "browser failure code is invalid", false)
	}
	if failure.Message == "" ||
		strings.TrimSpace(failure.Message) != failure.Message ||
		len(failure.Message) > 500 {
		return NewFailure(ErrorOutputInvalid, "browser failure message is invalid", false)
	}
	return nil
}
