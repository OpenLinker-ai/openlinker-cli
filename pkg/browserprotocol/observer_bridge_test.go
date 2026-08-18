package browserprotocol

import (
	"testing"
	"time"
)

func validViewerFrame() *ViewerFrame {
	return &ViewerFrame{
		MIMEType: "image/jpeg",
		Data:     []byte{0xff, 0xd8, 0xff, 0xd9},
		Width:    BrowserViewportWidth,
		Height:   BrowserViewportHeight,
	}
}

func bridgeIdentity() ObserverBridgeIdentity {
	return ObserverBridgeIdentity{
		RunID:            "11111111-1111-4111-8111-111111111111",
		AttemptID:        "22222222-2222-4222-8222-222222222222",
		SessionEpoch:     3,
		AttachmentID:     "attachment-a",
		RuntimeSessionID: "33333333-3333-4333-8333-333333333333",
	}
}

func bridgeEvent(kind ObserverBridgeEventKind) ObserverBridgeEvent {
	captured := time.Now().UTC()
	event := ObserverBridgeEvent{
		AttemptIdentity: bridgeIdentity(),
		CommandID:       "44444444-4444-4444-8444-444444444444",
		LeaseID:         "55555555-5555-4555-8555-555555555555",
		EventSeq:        1,
		Kind:            kind,
	}
	switch kind {
	case ObserverBridgeFrame:
		event.CapturedAt = &captured
		event.Frame = validViewerFrame()
	case ObserverBridgeError:
		event.ErrorCode = string(OpsObserverProtocolError)
	}
	return event
}

// Any identity drift means the Runtime moved on between the command and the
// capture, so the frame belongs to a different Run and must not be attributed to
// this observation.
func TestObserverBridgeIdentityRejectsEveryFieldDrift(t *testing.T) {
	t.Parallel()
	base := bridgeIdentity()
	for name, mutate := range map[string]func(*ObserverBridgeIdentity){
		"run":        func(i *ObserverBridgeIdentity) { i.RunID = "66666666-6666-4666-8666-666666666666" },
		"attempt":    func(i *ObserverBridgeIdentity) { i.AttemptID = "77777777-7777-4777-8777-777777777777" },
		"epoch":      func(i *ObserverBridgeIdentity) { i.SessionEpoch = base.SessionEpoch + 1 },
		"attachment": func(i *ObserverBridgeIdentity) { i.AttachmentID = "attachment-b" },
		"session":    func(i *ObserverBridgeIdentity) { i.RuntimeSessionID = "88888888-8888-4888-8888-888888888888" },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := base
			mutate(&drifted)
			if base.Equal(drifted) {
				t.Fatalf("%s drift was treated as the same identity", name)
			}
		})
	}
	if !base.Equal(bridgeIdentity()) {
		t.Fatal("an unchanged identity must compare equal")
	}
}

func TestObserverBridgeCommandValidation(t *testing.T) {
	t.Parallel()
	valid := ObserverBridgeCommand{
		AttemptIdentity: bridgeIdentity(),
		CommandID:       "44444444-4444-4444-8444-444444444444",
		Action:          ObserverBridgeStart,
		LeaseID:         "55555555-5555-4555-8555-555555555555",
		LeaseExpiresAt:  time.Now().UTC().Add(time.Minute),
		DeadlineAt:      time.Now().UTC().Add(time.Minute),
		FrameIntervalMS: ObserverBridgeDefaultFrameIntervalMS,
	}
	if failure := valid.Validate(); failure != nil {
		t.Fatalf("valid start command rejected: %v", failure)
	}

	unbounded := valid
	unbounded.DeadlineAt = time.Time{}
	if unbounded.Validate() == nil {
		t.Fatal("a start without a deadline must be refused")
	}
	for _, interval := range []int{
		ObserverBridgeMinFrameIntervalMS - 1,
		ObserverBridgeMaxFrameIntervalMS + 1,
	} {
		outOfRange := valid
		outOfRange.FrameIntervalMS = interval
		if outOfRange.Validate() == nil {
			t.Fatalf("frame interval %d must be refused", interval)
		}
	}

	// Stop carries no schedule, so it must not inherit the start requirements.
	stop := valid
	stop.Action = ObserverBridgeStop
	stop.LeaseExpiresAt = time.Time{}
	stop.DeadlineAt = time.Time{}
	stop.FrameIntervalMS = 0
	if failure := stop.Validate(); failure != nil {
		t.Fatalf("stop command rejected: %v", failure)
	}
}

func TestObserverBridgeEventValidationPerKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []ObserverBridgeEventKind{
		ObserverBridgeStarted, ObserverBridgeFrame, ObserverBridgeStopped, ObserverBridgeError,
	} {
		if failure := bridgeEvent(kind).Validate(); failure != nil {
			t.Fatalf("%s event rejected: %v", kind, failure)
		}
	}

	// A lifecycle event must stay empty; otherwise a frame could ride a kind the
	// consumer does not inspect for content.
	loaded := bridgeEvent(ObserverBridgeStarted)
	loaded.Frame = validViewerFrame()
	if loaded.Validate() == nil {
		t.Fatal("a started event carrying a frame must be refused")
	}

	incomplete := bridgeEvent(ObserverBridgeFrame)
	incomplete.CapturedAt = nil
	if incomplete.Validate() == nil {
		t.Fatal("a frame event without a capture time must be refused")
	}

	silent := bridgeEvent(ObserverBridgeError)
	silent.ErrorCode = ""
	if silent.Validate() == nil {
		t.Fatal("an error event without an error must be refused")
	}
}

// The window is a single unacknowledged event, so an ack that does not name the
// exact lease and sequence must not settle it.
func TestObserverBridgeAckMustNameTheExactEvent(t *testing.T) {
	t.Parallel()
	event := bridgeEvent(ObserverBridgeFrame)
	exact := ObserverBridgeEventAck{
		AttemptIdentity: event.AttemptIdentity,
		LeaseID:         event.LeaseID,
		EventSeq:        event.EventSeq,
	}
	if !exact.Matches(event) {
		t.Fatal("the exact ack did not settle its event")
	}
	for name, mutate := range map[string]func(*ObserverBridgeEventAck){
		"stale sequence": func(a *ObserverBridgeEventAck) { a.EventSeq = event.EventSeq + 1 },
		"other lease":    func(a *ObserverBridgeEventAck) { a.LeaseID = "99999999-9999-4999-8999-999999999999" },
		"other attempt":  func(a *ObserverBridgeEventAck) { a.AttemptIdentity.AttemptID = "99999999-9999-4999-8999-999999999999" },
	} {
		t.Run(name, func(t *testing.T) {
			ack := exact
			mutate(&ack)
			if ack.Matches(event) {
				t.Fatalf("%s settled an event it does not name", name)
			}
		})
	}
}
