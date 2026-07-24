package browserprotocol

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	ContractID = "openlinker.browser.v1"

	MaxRequestBytes    = 256 << 10
	MaxResponseBytes   = 8 << 20
	MaxScreenshotBytes = 4 << 20
	MaxAXTreeBytes     = 1 << 20
	MaxDOMDiffBytes    = 1 << 20
	MaxActionDeadline  = 60 * time.Second
)

type ActionKind string

const (
	ActionNavigate      ActionKind = "navigate"
	ActionClick         ActionKind = "click"
	ActionTypeNonSecret ActionKind = "type_non_secret"
	ActionScroll        ActionKind = "scroll"
	ActionKeypress      ActionKind = "keypress"
	ActionSelect        ActionKind = "select"
	ActionWait          ActionKind = "wait"
	ActionBack          ActionKind = "back"
	ActionForward       ActionKind = "forward"
	ActionScreenshot    ActionKind = "screenshot"
)

type ErrorCode string

const (
	ErrorProtocolInvalid         ErrorCode = "BROWSER_PROTOCOL_INVALID"
	ErrorRequestTooLarge         ErrorCode = "BROWSER_REQUEST_TOO_LARGE"
	ErrorOutputTooLarge          ErrorCode = "BROWSER_OUTPUT_TOO_LARGE"
	ErrorUnauthorized            ErrorCode = "BROWSER_UNAUTHORIZED"
	ErrorIdentityMismatch        ErrorCode = "BROWSER_IDENTITY_MISMATCH"
	ErrorStaleControlEpoch       ErrorCode = "BROWSER_STALE_CONTROL_EPOCH"
	ErrorRequestReplayed         ErrorCode = "BROWSER_REQUEST_REPLAYED"
	ErrorDeadlineExceeded        ErrorCode = "BROWSER_DEADLINE_EXCEEDED"
	ErrorProviderCapability      ErrorCode = "BROWSER_PROVIDER_CAPABILITY_UNSUPPORTED"
	ErrorRuntimeUnavailable      ErrorCode = "BROWSER_RUNTIME_UNAVAILABLE"
	ErrorEgressUnavailable       ErrorCode = "BROWSER_EGRESS_UNAVAILABLE"
	ErrorTargetBlocked           ErrorCode = "BROWSER_TARGET_BLOCKED"
	ErrorProfileLocked           ErrorCode = "BROWSER_PROFILE_LOCKED"
	ErrorProfileCorrupt          ErrorCode = "BROWSER_PROFILE_CORRUPT"
	ErrorConversationRecovery    ErrorCode = "BROWSER_CONVERSATION_RECOVERY_FAILED"
	ErrorUserActionRequired      ErrorCode = "BROWSER_USER_ACTION_REQUIRED"
	ErrorHighImpactActionBlocked ErrorCode = "BROWSER_HIGH_IMPACT_ACTION_BLOCKED"
	ErrorViewerUnavailable       ErrorCode = "BROWSER_VIEWER_UNAVAILABLE"
	ErrorActionLimitExceeded     ErrorCode = "BROWSER_ACTION_LIMIT_EXCEEDED"
	ErrorCanceled                ErrorCode = "BROWSER_CANCELED"
	ErrorActionRejected          ErrorCode = "BROWSER_ACTION_REJECTED"
	ErrorOutputInvalid           ErrorCode = "BROWSER_OUTPUT_INVALID"
	ErrorInternal                ErrorCode = "BROWSER_INTERNAL"
)

type Identity struct {
	RunID             string `json:"run_id"`
	AgentID           string `json:"agent_id"`
	PrincipalScopeID  string `json:"principal_scope_id"`
	ConversationID    string `json:"conversation_id"`
	BrowserGeneration uint64 `json:"browser_generation"`
	AttachmentID      string `json:"attachment_id"`
	ControlEpoch      uint64 `json:"control_epoch"`
}

type Request struct {
	ContractID        string    `json:"contract_id"`
	ChannelCredential string    `json:"channel_credential"`
	RequestID         string    `json:"request_id"`
	Deadline          time.Time `json:"deadline"`
	Identity          Identity  `json:"identity"`
	Action            Action    `json:"action"`
}

type Action struct {
	Kind       ActionKind `json:"kind"`
	URL        string     `json:"url,omitempty"`
	X          *int       `json:"x,omitempty"`
	Y          *int       `json:"y,omitempty"`
	DeltaX     *int       `json:"delta_x,omitempty"`
	DeltaY     *int       `json:"delta_y,omitempty"`
	Text       string     `json:"text,omitempty"`
	Key        string     `json:"key,omitempty"`
	Value      string     `json:"value,omitempty"`
	DurationMS *int       `json:"duration_ms,omitempty"`
}

type Screenshot struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

type Observation struct {
	PageStateID string          `json:"page_state_id"`
	Screenshot  *Screenshot     `json:"screenshot,omitempty"`
	AXTree      json.RawMessage `json:"ax_tree,omitempty"`
	DOMDiff     json.RawMessage `json:"dom_diff,omitempty"`
	Origin      string          `json:"origin,omitempty"`
	Title       string          `json:"title,omitempty"`
}

type Failure struct {
	Code        ErrorCode `json:"code"`
	Message     string    `json:"message"`
	Recoverable bool      `json:"recoverable"`
}

func (failure *Failure) Error() string {
	if failure == nil {
		return ""
	}
	return string(failure.Code) + ": " + failure.Message
}

type Response struct {
	ContractID  string       `json:"contract_id"`
	RequestID   string       `json:"request_id,omitempty"`
	Status      string       `json:"status"`
	Observation *Observation `json:"observation,omitempty"`
	Error       *Failure     `json:"error,omitempty"`
}

func SuccessResponse(requestID string, observation Observation) Response {
	return Response{
		ContractID:  ContractID,
		RequestID:   requestID,
		Status:      "ok",
		Observation: &observation,
	}
}

func ErrorResponse(requestID string, failure *Failure) Response {
	if failure == nil {
		failure = NewFailure(ErrorInternal, "browser runtime failed", false)
	}
	return Response{
		ContractID: ContractID,
		RequestID:  boundedString(requestID, 128),
		Status:     "error",
		Error:      failure,
	}
}

func NewFailure(code ErrorCode, message string, recoverable bool) *Failure {
	return &Failure{
		Code:        code,
		Message:     boundedString(strings.TrimSpace(message), 500),
		Recoverable: recoverable,
	}
}
