package agentexec

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func runtimeAttemptIdentityFor(runID, attemptID string) openlinker.RuntimeAttemptIdentity {
	return openlinker.RuntimeAttemptIdentity{RunID: runID, AttemptID: attemptID}
}

func observationCommand(action browserprotocol.ObserverBridgeAction) browserprotocol.ObserverBridgeCommand {
	now := time.Now().UTC()
	return browserprotocol.ObserverBridgeCommand{
		AttemptIdentity: browserprotocol.ObserverBridgeIdentity{
			RunID:            "11111111-1111-4111-8111-111111111111",
			AttemptID:        "22222222-2222-4222-8222-222222222222",
			SessionEpoch:     4,
			AttachmentID:     "attachment-a",
			RuntimeSessionID: "33333333-3333-4333-8333-333333333333",
		},
		CommandID:       "44444444-4444-4444-8444-444444444444",
		Action:          action,
		LeaseID:         "55555555-5555-4555-8555-555555555555",
		LeaseExpiresAt:  now.Add(5 * time.Minute),
		DeadlineAt:      now.Add(time.Minute),
		FrameIntervalMS: browserprotocol.ObserverBridgeDefaultFrameIntervalMS,
	}
}

// A frame captured after the Runtime moved on belongs to a different Run. The
// comparison has to cover every field the command named, so each one is drifted
// independently.
func TestObservedIdentityRequiresEveryFieldToMatch(t *testing.T) {
	t.Parallel()
	command := observationCommand(browserprotocol.ObserverBridgeStart)
	actual := browserprotocol.Identity{
		RunID:        command.AttemptIdentity.RunID,
		SessionEpoch: command.AttemptIdentity.SessionEpoch,
		AttachmentID: command.AttemptIdentity.AttachmentID,
	}
	if !observedIdentityMatches(command.AttemptIdentity, actual) {
		t.Fatal("an unchanged identity was treated as drifted")
	}
	for name, mutate := range map[string]func(*browserprotocol.Identity){
		"run":        func(i *browserprotocol.Identity) { i.RunID = "66666666-6666-4666-8666-666666666666" },
		"epoch":      func(i *browserprotocol.Identity) { i.SessionEpoch++ },
		"attachment": func(i *browserprotocol.Identity) { i.AttachmentID = "attachment-b" },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := actual
			mutate(&drifted)
			if observedIdentityMatches(command.AttemptIdentity, drifted) {
				t.Fatalf("%s drift was accepted as the same Attempt", name)
			}
		})
	}
}

// A command naming another Attempt must be ignored outright rather than starting
// an observation bound to the wrong Run.
func TestObservationRejectsForeignAttemptCommands(t *testing.T) {
	t.Parallel()
	observation := newBrowserObservation(nil, nil)
	command := observationCommand(browserprotocol.ObserverBridgeStart)
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	observation.handleCommand(t.Context(), payload, runtimeAttemptIdentityFor(
		"77777777-7777-4777-8777-777777777777",
		command.AttemptIdentity.AttemptID,
	))
	observation.mu.Lock()
	leaseID := observation.leaseID
	observation.mu.Unlock()
	if leaseID != "" {
		t.Fatal("a command for another Run started an observation")
	}
}

// stop must be safe before anything started and must clear the lease, so a
// Run-terminal or disconnect path can always call it.
func TestObservationStopIsIdempotent(t *testing.T) {
	t.Parallel()
	observation := newBrowserObservation(nil, nil)
	observation.stop()
	observation.mu.Lock()
	observation.leaseID = "55555555-5555-4555-8555-555555555555"
	observation.mu.Unlock()
	observation.stop()
	observation.stop()
	observation.mu.Lock()
	defer observation.mu.Unlock()
	if observation.leaseID != "" || observation.cancel != nil {
		t.Fatal("stop left observation state behind")
	}
}

// Malformed or invalid commands must not start anything; the bridge fails closed
// rather than observing with defaults it invented.
func TestObservationIgnoresInvalidCommands(t *testing.T) {
	t.Parallel()
	identity := runtimeAttemptIdentityFor(
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	)
	for name, payload := range map[string][]byte{
		"not json":     []byte("{"),
		"empty object": []byte("{}"),
		"bad interval": observationPayload(t, func(c *browserprotocol.ObserverBridgeCommand) { c.FrameIntervalMS = 1 }),
		"no deadline":  observationPayload(t, func(c *browserprotocol.ObserverBridgeCommand) { c.DeadlineAt = time.Time{} }),
		"bad action":   observationPayload(t, func(c *browserprotocol.ObserverBridgeCommand) { c.Action = "observe" }),
	} {
		t.Run(name, func(t *testing.T) {
			observation := newBrowserObservation(nil, nil)
			observation.handleCommand(t.Context(), payload, identity)
			observation.mu.Lock()
			defer observation.mu.Unlock()
			if observation.leaseID != "" {
				t.Fatalf("%s started an observation", name)
			}
		})
	}
}

func observationPayload(
	t *testing.T,
	mutate func(*browserprotocol.ObserverBridgeCommand),
) []byte {
	t.Helper()
	command := observationCommand(browserprotocol.ObserverBridgeStart)
	mutate(&command)
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// The frame carries the identity the Runtime backfilled at capture time, which
// is stronger evidence than the Worker's own snapshot: it describes the Attempt
// the pixels actually came from.
func TestObservationRejectsFramesFromAnotherAttempt(t *testing.T) {
	t.Parallel()
	command := observationCommand(browserprotocol.ObserverBridgeStart)
	matching := browserprotocol.OpsObserverObservation{
		RunID:            command.AttemptIdentity.RunID,
		SessionEpoch:     command.AttemptIdentity.SessionEpoch,
		AttachmentSHA256: observedAttachmentDigest(command),
	}
	if capturedFrameIsForeign(command, matching) {
		t.Fatal("a matching capture was treated as foreign")
	}
	for name, mutate := range map[string]func(*browserprotocol.OpsObserverObservation){
		"run":   func(o *browserprotocol.OpsObserverObservation) { o.RunID = "66666666-6666-4666-8666-666666666666" },
		"epoch": func(o *browserprotocol.OpsObserverObservation) { o.SessionEpoch++ },
		// An attachment can rotate between the snapshot and the capture, so this
		// is the drift a Run-and-epoch-only check would have let through.
		"attachment": func(o *browserprotocol.OpsObserverObservation) {
			rotated := observationCommand(browserprotocol.ObserverBridgeStart)
			rotated.AttemptIdentity.AttachmentID = "attachment-rotated"
			o.AttachmentSHA256 = observedAttachmentDigest(rotated)
		},
	} {
		t.Run(name, func(t *testing.T) {
			drifted := matching
			mutate(&drifted)
			if !capturedFrameIsForeign(command, drifted) {
				t.Fatalf("a capture with a drifted %s was accepted", name)
			}
		})
	}
}
