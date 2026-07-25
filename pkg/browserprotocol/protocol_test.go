package browserprotocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRequestValidateAcceptsOnlyBoundedPhaseOneActions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	x, y, delta, wait := 10, 20, 100, 250
	actions := []Action{
		{Kind: ActionNavigate, URL: "https://example.com/path"},
		{Kind: ActionClick, X: &x, Y: &y},
		{Kind: ActionTypeNonSecret, Text: "public search text"},
		{Kind: ActionScroll, DeltaY: &delta},
		{Kind: ActionKeypress, Key: "Enter"},
		{Kind: ActionSelect, X: &x, Y: &y, Value: "option-a"},
		{Kind: ActionWait, DurationMS: &wait},
		{Kind: ActionBack},
		{Kind: ActionForward},
		{Kind: ActionScreenshot},
		{Kind: ActionCheckpoint},
		{Kind: ActionClose},
	}
	for _, action := range actions {
		action := action
		t.Run(string(action.Kind), func(t *testing.T) {
			request := validRequest(now)
			request.Action = action
			if failure := request.Validate(now); failure != nil {
				t.Fatalf("Validate() failure = %v", failure)
			}
		})
	}
}

func TestActionValidateRejectsArbitraryOrSmuggledCapabilities(t *testing.T) {
	t.Parallel()
	x, y := 10, 20
	cases := []Action{
		{Kind: "javascript", Text: "document.cookie"},
		{Kind: "shell", Text: "id"},
		{Kind: ActionClick, X: &x, Y: &y, Text: "unexpected"},
		{Kind: ActionTypeNonSecret, Text: ""},
		{Kind: ActionKeypress, Key: "a"},
		{Kind: ActionNavigate, URL: "file:///etc/passwd"},
		{Kind: ActionNavigate, URL: "https://user:secret@example.com/"},
	}
	for _, action := range cases {
		if failure := action.Validate(); failure == nil || failure.Code != ErrorProtocolInvalid {
			t.Errorf("Validate(%#v) failure = %#v, want %s", action, failure, ErrorProtocolInvalid)
		}
	}
}

func TestNavigateRejectsNonPublicLiteralAndLocalHostnames(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://100.64.0.1/",
		"http://169.254.169.254/",
		"http://192.0.2.1/",
		"http://[::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[2001:db8::1]/",
		"https://metadata.google.internal/",
		"https://service/",
	}
	for _, raw := range blocked {
		if failure := (Action{Kind: ActionNavigate, URL: raw}).Validate(); failure == nil {
			t.Errorf("Validate(%q) succeeded, want blocked", raw)
		}
	}
	for _, raw := range []string{"https://example.com/", "https://[2606:4700:4700::1111]/"} {
		if failure := (Action{Kind: ActionNavigate, URL: raw}).Validate(); failure != nil {
			t.Errorf("Validate(%q) failure = %v", raw, failure)
		}
	}
}

func TestRequestValidateRejectsExpiredAndUnboundedDeadlines(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	request := validRequest(now)
	request.Deadline = now
	if failure := request.Validate(now); failure == nil || failure.Code != ErrorDeadlineExceeded {
		t.Fatalf("expired deadline failure = %#v", failure)
	}
	request.Deadline = now.Add(MaxActionDeadline + time.Second)
	if failure := request.Validate(now); failure == nil || failure.Code != ErrorProtocolInvalid {
		t.Fatalf("unbounded deadline failure = %#v", failure)
	}
}

func TestObservationValidateEnforcesPayloadLimitsAndJSON(t *testing.T) {
	t.Parallel()
	valid := Observation{
		PageStateID: "state-1",
		Screenshot:  &Screenshot{MIMEType: "image/png", Data: []byte("png")},
		AXTree:      json.RawMessage(`{"role":"document"}`),
		DOMDiff:     json.RawMessage(`{"changed":[]}`),
		Origin:      "https://example.com",
		Title:       "Example",
	}
	if failure := valid.Validate(); failure != nil {
		t.Fatalf("valid observation failure = %v", failure)
	}
	invalidJSON := valid
	invalidJSON.AXTree = json.RawMessage(`{"broken"`)
	if failure := invalidJSON.Validate(); failure == nil || failure.Code != ErrorOutputInvalid {
		t.Fatalf("invalid JSON failure = %#v", failure)
	}
	large := valid
	large.Screenshot = &Screenshot{
		MIMEType: "image/png",
		Data:     bytes.Repeat([]byte{'x'}, MaxScreenshotBytes+1),
	}
	if failure := large.Validate(); failure == nil || failure.Code != ErrorOutputTooLarge {
		t.Fatalf("large screenshot failure = %#v", failure)
	}
}

func TestErrorResponseBoundsUntrustedRequestID(t *testing.T) {
	t.Parallel()
	response := ErrorResponse(strings.Repeat("x", 1024), NewFailure(ErrorProtocolInvalid, "bad", false))
	if len(response.RequestID) != 128 {
		t.Fatalf("request ID length = %d, want 128", len(response.RequestID))
	}
}

func validRequest(now time.Time) Request {
	return Request{
		ContractID:        ContractID,
		ChannelCredential: strings.Repeat("a", 64),
		RequestID:         "77777777-7777-4777-8777-777777777777",
		Deadline:          now.Add(30 * time.Second),
		Identity: Identity{
			RunID:            "11111111-1111-4111-8111-111111111111",
			AgentID:          "22222222-2222-4222-8222-222222222222",
			PrincipalScopeID: "scope_333333333333",
			BrowserSessionID: "44444444-4444-4444-8444-444444444444",
			SessionEpoch:     1,
			AttachmentID:     "55555555-5555-4555-8555-555555555555",
			ControlEpoch:     1,
		},
		Action: Action{Kind: ActionScreenshot},
	}
}
