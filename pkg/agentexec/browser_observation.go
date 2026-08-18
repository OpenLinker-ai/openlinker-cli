package agentexec

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const (
	observationSocketEnvironment     = "OPENLINKER_BROWSER_OBSERVER_SOCKET"
	observationCredentialEnvironment = "OPENLINKER_BROWSER_OBSERVER_CREDENTIAL_FILE"
	defaultObservationSocket         = "/browser-control/openlinker.browser.observer.sock"
	defaultObservationCredential     = "/browser-control/observer-credential"
)

func observationSocketPath() string {
	if value := strings.TrimSpace(os.Getenv(observationSocketEnvironment)); value != "" {
		return value
	}
	return defaultObservationSocket
}

func observationCredentialPath() string {
	if value := strings.TrimSpace(os.Getenv(observationCredentialEnvironment)); value != "" {
		return value
	}
	return defaultObservationCredential
}

// browserObservation serves authenticated read-only observation.
//
// It keeps its own lease, lifecycle and state rather than sharing the
// human-control structures: observation and takeover are separate capabilities
// with separate authorization, and merging their state here is how "being able
// to watch" quietly becomes "being able to drive".
type browserObservation struct {
	lease      *browserRunLease
	extensions *openlinker.RuntimeExtensions

	mu       sync.Mutex
	leaseID  string
	cancel   context.CancelFunc
	eventSeq uint64
}

func newBrowserObservation(
	lease *browserRunLease,
	extensions *openlinker.RuntimeExtensions,
) *browserObservation {
	return &browserObservation{lease: lease, extensions: extensions}
}

func (observation *browserObservation) handleCommand(
	parent context.Context,
	raw json.RawMessage,
	attemptIdentity openlinker.RuntimeAttemptIdentity,
) {
	if observation == nil {
		return
	}
	var command browserprotocol.ObserverBridgeCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		return
	}
	if failure := command.Validate(); failure != nil {
		return
	}
	if command.AttemptIdentity.RunID != attemptIdentity.RunID ||
		command.AttemptIdentity.AttemptID != attemptIdentity.AttemptID {
		return
	}
	switch command.Action {
	case browserprotocol.ObserverBridgeStop:
		observation.stop()
	case browserprotocol.ObserverBridgeStart:
		observation.start(parent, command)
	}
}

func (observation *browserObservation) start(
	parent context.Context,
	command browserprotocol.ObserverBridgeCommand,
) {
	observation.mu.Lock()
	if observation.leaseID != "" {
		observation.mu.Unlock()
		observation.emitError(command, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverAlreadyActive,
			"an observation is already active",
		))
		return
	}
	ctx, cancel := context.WithDeadline(parent, command.LeaseExpiresAt)
	observation.leaseID = command.LeaseID
	observation.cancel = cancel
	observation.mu.Unlock()

	go observation.stream(ctx, command)
}

func (observation *browserObservation) stop() {
	observation.mu.Lock()
	cancel := observation.cancel
	observation.cancel = nil
	observation.leaseID = ""
	observation.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// stream captures at the requested interval until the lease ends. Every frame is
// checked against the identity the command named: if the Runtime has moved to
// another Attempt in between, the frame belongs to a different Run and reporting
// it here would attribute another user's page to this observation.
func (observation *browserObservation) stream(
	ctx context.Context,
	command browserprotocol.ObserverBridgeCommand,
) {
	defer observation.stop()

	// Claiming the bridge lease is what makes the local Viewer and this
	// observation contend for the one Runtime lease. A refusal here means someone
	// is already watching, and it has to reach the caller rather than look like a
	// silent stall.
	stream, err := browserclient.NewOpsObserverStream(ctx, browserclient.OpsObserverConfig{
		SocketPath:     observationSocketPath(),
		CredentialFile: observationCredentialPath(),
		RunID:          command.AttemptIdentity.RunID,
		TTL:            time.Until(command.LeaseExpiresAt),
	})
	if err != nil {
		observation.emitError(command, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverDisabled,
			"the observation bridge is unavailable",
		))
		return
	}
	defer stream.Close()

	if !observation.emitEvent(ctx, command, browserprotocol.ObserverBridgeEvent{
		Kind: browserprotocol.ObserverBridgeStarted,
	}) {
		return
	}

	ticker := time.NewTicker(time.Duration(command.FrameIntervalMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// A best-effort stopped event; the lease is already closing either
			// way, so a failure to deliver it must not block teardown.
			observation.emitEvent(context.Background(), command, browserprotocol.ObserverBridgeEvent{
				Kind: browserprotocol.ObserverBridgeStopped,
			})
			return
		case <-ticker.C:
			identity, err := observation.lease.identitySnapshot()
			if err != nil {
				observation.emitError(command, browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverRunNotActive,
					"the observed Run is no longer active",
				))
				return
			}
			if !observedIdentityMatches(command.AttemptIdentity, identity) {
				observation.emitError(command, browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverRunNotActive,
					"the Runtime moved to another Attempt",
				))
				return
			}
			response, observeErr := stream.Observe(
				ctx,
				browserprotocol.OpsObserverFrameOperation,
			)
			if observeErr != nil {
				// Busy is transient: the Engine is occupied authorizing the
				// observer, so skip this tick rather than ending the lease.
				if observeErr.Code == browserprotocol.OpsObserverBusyError {
					continue
				}
				observation.emitError(command, observeErr)
				return
			}
			if response.Observation == nil || response.Observation.Frame == nil {
				continue
			}
			// The Runtime backfilled its own identity, so compare that rather
			// than the local snapshot: it is the identity the frame was actually
			// captured under.
			if capturedFrameIsForeign(command, *response.Observation) {
				observation.emitError(command, browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverRunNotActive,
					"the captured frame belongs to another Attempt",
				))
				return
			}
			captured := response.Observation.CapturedAt.UTC()
			if captured.IsZero() {
				captured = time.Now().UTC()
			}
			if !observation.emitEvent(ctx, command, browserprotocol.ObserverBridgeEvent{
				Kind:       browserprotocol.ObserverBridgeFrame,
				CapturedAt: &captured,
				Frame:      response.Observation.Frame,
			}) {
				return
			}
		}
	}
}

// capturedFrameIsForeign compares the identity the Runtime backfilled at capture
// time against the one the command named. That evidence is stronger than the
// Worker's own snapshot because it describes the Attempt the pixels came from.
func capturedFrameIsForeign(
	command browserprotocol.ObserverBridgeCommand,
	observed browserprotocol.OpsObserverObservation,
) bool {
	return observed.RunID != command.AttemptIdentity.RunID ||
		observed.SessionEpoch != command.AttemptIdentity.SessionEpoch
}

// observedIdentityMatches compares every field the command named. A partial
// comparison would let a frame captured after an attachment or epoch change be
// reported under the previous identity.
func observedIdentityMatches(
	expected browserprotocol.ObserverBridgeIdentity,
	actual browserprotocol.Identity,
) bool {
	return expected.RunID == actual.RunID &&
		expected.SessionEpoch == actual.SessionEpoch &&
		expected.AttachmentID == actual.AttachmentID
}

// emitEvent publishes one event and waits for its ack. The window is a single
// unacknowledged event, so this blocks until the ack arrives or the lease ends;
// an ack timeout stops the lease rather than leaving a paused state nobody owns.
func (observation *browserObservation) emitEvent(
	ctx context.Context,
	command browserprotocol.ObserverBridgeCommand,
	event browserprotocol.ObserverBridgeEvent,
) bool {
	observation.mu.Lock()
	if observation.leaseID != command.LeaseID {
		observation.mu.Unlock()
		return false
	}
	observation.eventSeq++
	event.EventSeq = observation.eventSeq
	observation.mu.Unlock()

	event.AttemptIdentity = command.AttemptIdentity
	event.CommandID = command.CommandID
	event.LeaseID = command.LeaseID
	if failure := event.Validate(); failure != nil {
		return false
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return false
	}
	reply, err := observation.extensions.Publish(ctx, openlinker.RuntimeExtensionRequest{
		Type:    browserprotocol.ObserverBridgeEventType,
		Payload: payload,
	})
	if err != nil || reply == nil {
		return false
	}
	var ack browserprotocol.ObserverBridgeEventAck
	if err := json.Unmarshal(reply.Payload, &ack); err != nil {
		return false
	}
	return ack.Matches(event)
}

func (observation *browserObservation) emitError(
	command browserprotocol.ObserverBridgeCommand,
	failure *browserprotocol.OpsObserverError,
) {
	if failure == nil {
		failure = browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"observation failed",
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserprotocol.MaxOpsObserverDeadline)
	defer cancel()
	observation.emitEvent(ctx, command, browserprotocol.ObserverBridgeEvent{
		Kind:      browserprotocol.ObserverBridgeError,
		ErrorCode: string(failure.Code),
	})
}
