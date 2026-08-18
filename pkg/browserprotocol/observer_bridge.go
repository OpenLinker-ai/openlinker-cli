package browserprotocol

import (
	"strings"
	"time"
)

// The authenticated observation extension. A Runtime command has no reply
// channel, so every outcome the caller needs -- started, a frame, a normal stop
// and any error -- travels back on the event stream instead.
const (
	ObserverBridgeFeature      = "browser_authenticated_observation.v1"
	ObserverBridgeCommandType  = "browser.observer.command"
	ObserverBridgeEventType    = "browser.observer.event"
	ObserverBridgeEventAckType = "browser.observer.event.ack"
)

type ObserverBridgeAction string

const (
	ObserverBridgeStart ObserverBridgeAction = "start"
	ObserverBridgeStop  ObserverBridgeAction = "stop"
)

type ObserverBridgeEventKind string

const (
	ObserverBridgeStarted ObserverBridgeEventKind = "started"
	ObserverBridgeFrame   ObserverBridgeEventKind = "frame"
	ObserverBridgeStopped ObserverBridgeEventKind = "stopped"
	ObserverBridgeError   ObserverBridgeEventKind = "error"
)

const (
	// A single unacknowledged event. A wider window would need a rule for the
	// half-drained state after a failure; one event means an unacknowledged
	// event is always the last one sent.
	ObserverBridgeWindow = 1
	// Matches the interval the existing local Viewer already renders at.
	ObserverBridgeDefaultFrameIntervalMS = 500
	ObserverBridgeMinFrameIntervalMS     = 100
	ObserverBridgeMaxFrameIntervalMS     = 5_000
)

// ObserverBridgeIdentity is the Attempt-scoped identity a command carries and
// every event must still match. Comparing it per frame is what stops a frame
// captured after the Runtime moved to another Run from being reported under the
// previous one.
type ObserverBridgeIdentity struct {
	RunID            string `json:"run_id"`
	AttemptID        string `json:"attempt_id"`
	SessionEpoch     uint64 `json:"session_epoch"`
	AttachmentID     string `json:"attachment_id"`
	RuntimeSessionID string `json:"runtime_session_id"`
}

func (identity ObserverBridgeIdentity) Equal(other ObserverBridgeIdentity) bool {
	return identity == other
}

func (identity ObserverBridgeIdentity) validate() bool {
	return validUUID(identity.RunID) && validUUID(identity.AttemptID) &&
		identity.SessionEpoch > 0 && identity.AttachmentID != "" &&
		validUUID(identity.RuntimeSessionID)
}

type ObserverBridgeCommand struct {
	AttemptIdentity ObserverBridgeIdentity `json:"attempt_identity"`
	CommandID       string                 `json:"command_id"`
	Action          ObserverBridgeAction   `json:"action"`
	LeaseID         string                 `json:"lease_id"`
	LeaseExpiresAt  time.Time              `json:"lease_expires_at"`
	DeadlineAt      time.Time              `json:"deadline_at"`
	FrameIntervalMS int                    `json:"frame_interval_ms"`
}

type ObserverBridgeEvent struct {
	AttemptIdentity ObserverBridgeIdentity  `json:"attempt_identity"`
	CommandID       string                  `json:"command_id"`
	LeaseID         string                  `json:"lease_id"`
	EventSeq        uint64                  `json:"event_seq"`
	Kind            ObserverBridgeEventKind `json:"kind"`
	CapturedAt      *time.Time              `json:"captured_at,omitempty"`
	Frame           *ViewerFrame            `json:"frame,omitempty"`
	Error           *OpsObserverError       `json:"error,omitempty"`
}

type ObserverBridgeEventAck struct {
	AttemptIdentity ObserverBridgeIdentity `json:"attempt_identity"`
	LeaseID         string                 `json:"lease_id"`
	EventSeq        uint64                 `json:"event_seq"`
}

func (command ObserverBridgeCommand) Validate() *OpsObserverError {
	if !command.AttemptIdentity.validate() || !validUUID(command.CommandID) ||
		!validUUID(command.LeaseID) {
		return NewOpsObserverError(OpsObserverProtocolError, "Observer command identity is invalid")
	}
	switch command.Action {
	case ObserverBridgeStart:
		if command.LeaseExpiresAt.IsZero() || command.DeadlineAt.IsZero() {
			return NewOpsObserverError(OpsObserverProtocolError, "Observer start requires bounded deadlines")
		}
		if command.FrameIntervalMS < ObserverBridgeMinFrameIntervalMS ||
			command.FrameIntervalMS > ObserverBridgeMaxFrameIntervalMS {
			return NewOpsObserverError(OpsObserverProtocolError, "Observer frame interval is out of range")
		}
	case ObserverBridgeStop:
	default:
		return NewOpsObserverError(OpsObserverProtocolError, "Observer command action is invalid")
	}
	return nil
}

func (event ObserverBridgeEvent) Validate() *OpsObserverError {
	if !event.AttemptIdentity.validate() || !validUUID(event.CommandID) ||
		!validUUID(event.LeaseID) || event.EventSeq == 0 {
		return NewOpsObserverError(OpsObserverProtocolError, "Observer event identity is invalid")
	}
	switch event.Kind {
	case ObserverBridgeStarted, ObserverBridgeStopped:
		if event.Frame != nil || event.Error != nil {
			return NewOpsObserverError(OpsObserverProtocolError, "Observer lifecycle event carries a payload")
		}
	case ObserverBridgeFrame:
		if event.Frame == nil || event.CapturedAt == nil || event.CapturedAt.IsZero() {
			return NewOpsObserverError(OpsObserverProtocolError, "Observer frame event is incomplete")
		}
		if failure := event.Frame.Validate(); failure != nil {
			return NewOpsObserverError(OpsObserverInternalError, "Observer frame is invalid")
		}
	case ObserverBridgeError:
		if event.Error == nil || strings.TrimSpace(string(event.Error.Code)) == "" {
			return NewOpsObserverError(OpsObserverProtocolError, "Observer error event has no code")
		}
	default:
		return NewOpsObserverError(OpsObserverProtocolError, "Observer event kind is invalid")
	}
	return nil
}

// Matches reports whether an ack settles the event it claims to. The lease and
// sequence both have to line up, so an ack for a previous lease cannot advance
// the current window.
func (ack ObserverBridgeEventAck) Matches(event ObserverBridgeEvent) bool {
	return ack.LeaseID == event.LeaseID && ack.EventSeq == event.EventSeq &&
		ack.AttemptIdentity.Equal(event.AttemptIdentity)
}
