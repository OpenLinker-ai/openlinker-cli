package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// Browser-ready proves the attachment preflight, but the provider can spend
	// several seconds preparing its first Browser action before the Engine has a
	// live capture surface. Keep that normal gap bounded without stretching it
	// to the observation lease TTL.
	browserObservationActivationGrace = 30 * time.Second
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
	if command.AttemptIdentity.RuntimeIdentity() != attemptIdentity {
		return
	}
	switch command.Action {
	case browserprotocol.ObserverBridgeStop:
		observation.stopLease(command.LeaseID)
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
		// Reported without the lease guard: emitEvent only publishes for the
		// lease it owns, so routing a busy refusal through it would drop the
		// very message telling Core the start failed, leaving a phantom active
		// record until its TTL.
		observation.emitUnguarded(command, browserprotocol.NewOpsObserverError(
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

// stop ends the current observation. stopLease ends only the named one: a
// stream that exits late must not clear a lease a newer start already took, or
// the new observation would be torn down by its predecessor's teardown.
func (observation *browserObservation) stop() {
	observation.stopLease("")
}

func (observation *browserObservation) stopLease(leaseID string) {
	observation.mu.Lock()
	if leaseID != "" && observation.leaseID != leaseID {
		observation.mu.Unlock()
		return
	}
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
	defer observation.stopLease(command.LeaseID)

	// Claiming the bridge lease is what makes the local Viewer and this
	// observation contend for the one Runtime lease. A refusal here means someone
	// is already watching, and it has to reach the caller rather than look like a
	// silent stall.
	streamConfig := browserclient.OpsObserverConfig{
		SocketPath:     observationSocketPath(),
		CredentialFile: observationCredentialPath(),
		RunID:          command.AttemptIdentity.RunID,
		TTL:            time.Until(command.LeaseExpiresAt),
	}
	stream, err := browserclient.NewOpsObserverStream(ctx, streamConfig)
	if err != nil {
		observation.emitError(command, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverDisabled,
			"the observation bridge is unavailable",
		))
		return
	}
	defer func() { _ = stream.Close() }()

	if !observation.emitEvent(ctx, command, browserprotocol.ObserverBridgeEvent{
		Kind: browserprotocol.ObserverBridgeStarted,
	}) {
		return
	}
	activatedAt := time.Now()

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
			if stream == nil {
				streamConfig.TTL = time.Until(command.LeaseExpiresAt)
				stream, err = browserclient.NewOpsObserverStream(ctx, streamConfig)
				if err != nil {
					observation.emitError(command, browserprotocol.NewOpsObserverError(
						browserprotocol.OpsObserverDisabled,
						"the observation bridge is unavailable",
					))
					return
				}
			}
			identity, err := observation.lease.identitySnapshot()
			if err != nil {
				observation.emitError(command, browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverRunNotActive,
					"the observed Run is no longer active",
				))
				return
			}
			if !commandNamesLocalAttempt(command, identity) {
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
				// Busy is transient. RUN_NOT_ACTIVE can also be transient just
				// after ready while the selected Engine publishes its first live
				// snapshot. The Worker's own lease and the command identity remain
				// the authority during this short grace; every retry rechecks both.
				// Error responses that are transient close the Runtime-side stream,
				// so reconnect before the next tick instead of reading that closed
				// socket and converting the activation race into OPS_VIEWER_INTERNAL.
				if discardTransientObservationStream(
					stream,
					observeErr,
					time.Since(activatedAt),
				) {
					stream = nil
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
			if capturedFrameIsForeign(identity, *response.Observation) {
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

type observationStreamCloser interface {
	Close() error
}

func discardTransientObservationStream(
	stream observationStreamCloser,
	failure *browserprotocol.OpsObserverError,
	sinceActivation time.Duration,
) bool {
	if !transientObservationError(failure, sinceActivation) {
		return false
	}
	if stream != nil {
		_ = stream.Close()
	}
	return true
}

func transientObservationError(
	failure *browserprotocol.OpsObserverError,
	sinceActivation time.Duration,
) bool {
	if failure == nil {
		return false
	}
	if failure.Code == browserprotocol.OpsObserverBusyError {
		return true
	}
	return failure.Code == browserprotocol.OpsObserverRunNotActive &&
		sinceActivation >= 0 &&
		sinceActivation < browserObservationActivationGrace
}

// capturedFrameIsForeign compares the identity the Runtime backfilled at capture
// time against the one the command named. That evidence is stronger than the
// Worker's own snapshot because it describes the Attempt the pixels came from.
// capturedFrameIsForeign compares the capture against the Worker's own live
// identity. The Runtime can rotate an attachment between the snapshot and the
// capture, so without the attachment digest a frame from the new one would be
// reported under the old.
func capturedFrameIsForeign(
	local browserprotocol.Identity,
	observed browserprotocol.OpsObserverObservation,
) bool {
	return observed.RunID != local.RunID ||
		observed.SessionEpoch != local.SessionEpoch ||
		observed.AttachmentSHA256 != capturedAttachmentDigest(local)
}

// capturedAttachmentDigest mirrors the Runtime's opsIdentitySHA256, which hashes
// the raw attachment with no domain prefix. The Worker holds the raw value
// locally, so it can derive this even though Core never sees it.
func capturedAttachmentDigest(identity browserprotocol.Identity) string {
	digest := sha256.Sum256([]byte(identity.AttachmentID))
	return hex.EncodeToString(digest[:])
}

// commandNamesLocalAttempt checks that the Attempt Core asked about is the one
// this Worker currently holds.
//
// Core knows only the domain-separated hashes the ready lifecycle event
// publishes, so the comparison is done in that space: the Worker rehashes its
// own identity the same way. This is the check that stops Core from starting an
// observation against an Attempt this Runtime has already moved on from.
func commandNamesLocalAttempt(
	command browserprotocol.ObserverBridgeCommand,
	local browserprotocol.Identity,
) bool {
	expected := command.AttemptIdentity
	return expected.RunID == local.RunID &&
		expected.SessionEpoch == local.SessionEpoch &&
		expected.BrowserSessionSHA256 == browserIdentityEvidenceSHA256(
			browserSessionEvidenceDomain,
			local.BrowserSessionID,
		) &&
		expected.AttachmentSHA256 == browserIdentityEvidenceSHA256(
			browserAttachmentEvidenceDomain,
			local.AttachmentID,
		)
}

// emitUnguarded publishes an event that must reach Core even when this handler
// does not own the lease, which is the case for a busy refusal.
func (observation *browserObservation) emitUnguarded(
	command browserprotocol.ObserverBridgeCommand,
	failure *browserprotocol.OpsObserverError,
) {
	ctx, cancel := context.WithTimeout(context.Background(), browserprotocol.MaxOpsObserverDeadline)
	defer cancel()
	event := browserprotocol.ObserverBridgeEvent{
		AttemptIdentity: command.AttemptIdentity,
		CommandID:       command.CommandID,
		LeaseID:         command.LeaseID,
		EventSeq:        1,
		Kind:            browserprotocol.ObserverBridgeError,
		ErrorCode:       string(failure.Code),
	}
	if event.Validate() != nil {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = observation.extensions.Publish(ctx, openlinker.RuntimeExtensionRequest{
		Type:    browserprotocol.ObserverBridgeEventType,
		Payload: payload,
	})
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
