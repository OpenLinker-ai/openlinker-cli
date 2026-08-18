package browserprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"
)

const (
	OpsObserverContractID = "openlinker.browser.ops-observer.v1"

	MaxOpsObserverRequestBytes = 16 << 10
	MinOpsObserverTTL          = time.Minute
	MaxOpsObserverTTL          = 30 * time.Minute
	DefaultOpsObserverTTL      = 10 * time.Minute
	MaxOpsObserverDeadline     = 2 * time.Second
	MaxOpsObserverCapture      = 500 * time.Millisecond
	MaxOpsObserverTitleBytes   = 512
	MaxOpsObserverURLBytes     = 4096
)

type OpsObserverOperation string

const (
	OpsObserverStatusOperation OpsObserverOperation = "observe_status"
	OpsObserverFrameOperation  OpsObserverOperation = "observe_frame"
)

type OpsObserverErrorCode string

const (
	OpsObserverDisabled      OpsObserverErrorCode = "OPS_VIEWER_DISABLED"
	OpsObserverUnauthorized  OpsObserverErrorCode = "OPS_VIEWER_UNAUTHORIZED"
	OpsObserverProtocolError OpsObserverErrorCode = "OPS_VIEWER_PROTOCOL_INVALID"
	OpsObserverRunNotActive  OpsObserverErrorCode = "RUN_NOT_ACTIVE"
	OpsObserverAlreadyActive OpsObserverErrorCode = "OPS_VIEWER_ALREADY_ACTIVE"
	OpsObserverBusyError     OpsObserverErrorCode = "OPS_VIEWER_BUSY"
	OpsObserverInternalError OpsObserverErrorCode = "OPS_VIEWER_INTERNAL"
)

type OpsObserverRequest struct {
	ContractID        string               `json:"contract_id"`
	ChannelCredential string               `json:"channel_credential"`
	RequestID         string               `json:"request_id"`
	ObserverLeaseID   string               `json:"observer_lease_id"`
	RunID             string               `json:"run_id"`
	Operation         OpsObserverOperation `json:"operation"`
	Deadline          time.Time            `json:"deadline"`
	LeaseExpiresAt    time.Time            `json:"lease_expires_at"`
}

type OpsObserverObservation struct {
	RunID                string     `json:"run_id"`
	Controller           Controller `json:"controller"`
	SessionEpoch         uint64     `json:"session_epoch"`
	ControlEpoch         uint64     `json:"control_epoch"`
	BrowserSessionSHA256 string     `json:"browser_session_sha256"`
	AttachmentSHA256     string     `json:"attachment_sha256"`
	SelectedBackend      string     `json:"selected_backend"`
	ProfileGeneration    uint64     `json:"profile_generation"`
	FrameSequence        uint64     `json:"frame_sequence"`
	CapturedAt           time.Time  `json:"captured_at"`
	PageURL              string     `json:"page_url,omitempty"`
	PageTitle            string     `json:"page_title,omitempty"`
	// ActionInFlight reports whether a provider-issued Browser action was
	// executing on the Engine when this observation was captured. It stays
	// optional so an older Runtime that cannot report it is still accepted;
	// gates that need the evidence must require an explicit true.
	ActionInFlight *bool        `json:"action_in_flight,omitempty"`
	Frame          *ViewerFrame `json:"frame,omitempty"`
}

type OpsObserverError struct {
	Code    OpsObserverErrorCode `json:"code"`
	Message string               `json:"message"`
}

func (err *OpsObserverError) Error() string {
	if err == nil {
		return ""
	}
	return string(err.Code) + ": " + err.Message
}

type OpsObserverResponse struct {
	ContractID  string                  `json:"contract_id"`
	RequestID   string                  `json:"request_id,omitempty"`
	Status      string                  `json:"status"`
	Observation *OpsObserverObservation `json:"observation,omitempty"`
	Error       *OpsObserverError       `json:"error,omitempty"`
}

func (request OpsObserverRequest) Validate(now time.Time) *OpsObserverError {
	if request.ContractID != OpsObserverContractID {
		return NewOpsObserverError(OpsObserverProtocolError, "unsupported Ops Observer contract")
	}
	if len(request.ChannelCredential) < 32 || len(request.ChannelCredential) > 512 {
		return NewOpsObserverError(OpsObserverProtocolError, "Ops Observer credential shape is invalid")
	}
	if !validUUID(request.RequestID) || !validUUID(request.ObserverLeaseID) ||
		!validUUID(request.RunID) {
		return NewOpsObserverError(OpsObserverProtocolError, "Ops Observer identity is invalid")
	}
	switch request.Operation {
	case OpsObserverStatusOperation, OpsObserverFrameOperation:
	default:
		return NewOpsObserverError(OpsObserverProtocolError, "Ops Observer operation is invalid")
	}
	now = now.UTC()
	if request.Deadline.IsZero() || !request.Deadline.After(now) ||
		request.Deadline.After(now.Add(MaxOpsObserverDeadline)) {
		return NewOpsObserverError(OpsObserverProtocolError, "Ops Observer deadline is invalid")
	}
	if request.LeaseExpiresAt.IsZero() || !request.LeaseExpiresAt.After(now) ||
		request.LeaseExpiresAt.After(now.Add(MaxOpsObserverTTL)) {
		return NewOpsObserverError(OpsObserverProtocolError, "Ops Observer lease expiry is invalid")
	}
	return nil
}

func (observation OpsObserverObservation) Validate(operation OpsObserverOperation) *OpsObserverError {
	if !validUUID(observation.RunID) || observation.SessionEpoch == 0 ||
		observation.ControlEpoch == 0 ||
		!validSHA256Hex(observation.BrowserSessionSHA256) ||
		!validSHA256Hex(observation.AttachmentSHA256) ||
		observation.FrameSequence == 0 || observation.CapturedAt.IsZero() {
		return NewOpsObserverError(OpsObserverInternalError, "Ops Observer identity evidence is invalid")
	}
	switch observation.Controller {
	case ControllerAgent, ControllerNone, ControllerHuman:
	default:
		return NewOpsObserverError(OpsObserverInternalError, "Ops Observer controller evidence is invalid")
	}
	switch observation.SelectedBackend {
	case "official_chrome_extension", "isolated_chromium":
	default:
		return NewOpsObserverError(OpsObserverInternalError, "Ops Observer backend evidence is invalid")
	}
	if len(observation.PageURL) > MaxOpsObserverURLBytes ||
		len(observation.PageTitle) > MaxOpsObserverTitleBytes ||
		strings.ContainsRune(observation.PageTitle, '\x00') ||
		!validObserverPageURL(observation.PageURL) {
		return NewOpsObserverError(OpsObserverInternalError, "Ops Observer page evidence is invalid")
	}
	if operation == OpsObserverFrameOperation {
		if observation.Frame == nil {
			return NewOpsObserverError(OpsObserverInternalError, "Ops Observer frame is missing")
		}
		if failure := observation.Frame.Validate(); failure != nil {
			return NewOpsObserverError(OpsObserverInternalError, "Ops Observer frame is invalid")
		}
	} else if operation == OpsObserverStatusOperation && observation.Frame != nil {
		return NewOpsObserverError(OpsObserverInternalError, "Ops Observer status contains a frame")
	}
	return nil
}

func OpsObserverSuccessResponse(requestID string, observation OpsObserverObservation) OpsObserverResponse {
	return OpsObserverResponse{
		ContractID:  OpsObserverContractID,
		RequestID:   requestID,
		Status:      "ok",
		Observation: &observation,
	}
}

func OpsObserverBusyResponse(requestID string) OpsObserverResponse {
	return OpsObserverResponse{
		ContractID: OpsObserverContractID,
		RequestID:  requestID,
		Status:     "busy",
	}
}

func OpsObserverErrorResponse(requestID string, observerErr *OpsObserverError) OpsObserverResponse {
	if observerErr == nil {
		observerErr = NewOpsObserverError(OpsObserverInternalError, "Ops Observer failed")
	}
	return OpsObserverResponse{
		ContractID: OpsObserverContractID,
		RequestID:  boundedString(requestID, 128),
		Status:     "error",
		Error:      observerErr,
	}
}

func NewOpsObserverError(code OpsObserverErrorCode, message string) *OpsObserverError {
	return &OpsObserverError{
		Code:    code,
		Message: boundedString(strings.TrimSpace(message), 300),
	}
}

func DecodeOpsObserverRequest(raw []byte) (OpsObserverRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request OpsObserverRequest
	if err := decoder.Decode(&request); err != nil {
		return OpsObserverRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return OpsObserverRequest{}, errors.New("unexpected trailing Ops Observer JSON value")
		}
		return OpsObserverRequest{}, err
	}
	return request, nil
}

func DecodeOpsObserverResponse(raw []byte) (OpsObserverResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response OpsObserverResponse
	if err := decoder.Decode(&response); err != nil {
		return OpsObserverResponse{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return OpsObserverResponse{}, errors.New("unexpected trailing Ops Observer response JSON value")
		}
		return OpsObserverResponse{}, err
	}
	if response.ContractID != OpsObserverContractID || !validUUID(response.RequestID) {
		return OpsObserverResponse{}, errors.New("Ops Observer response identity is invalid")
	}
	switch response.Status {
	case "ok":
		if response.Observation == nil || response.Error != nil {
			return OpsObserverResponse{}, errors.New("Ops Observer success response is invalid")
		}
	case "busy":
		if response.Observation != nil || response.Error != nil {
			return OpsObserverResponse{}, errors.New("Ops Observer busy response is invalid")
		}
	case "error":
		if response.Observation != nil || response.Error == nil || response.Error.Code == "" ||
			response.Error.Message == "" {
			return OpsObserverResponse{}, errors.New("Ops Observer error response is invalid")
		}
	default:
		return OpsObserverResponse{}, errors.New("Ops Observer response status is invalid")
	}
	return response, nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validObserverPageURL(raw string) bool {
	if raw == "about:blank" {
		return true
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed != nil &&
		(parsed.Scheme == "https" || parsed.Scheme == "http") &&
		parsed.Host != "" && parsed.RawQuery == "" && parsed.Fragment == "" &&
		parsed.User == nil
}
