package browserprotocol

import (
	"encoding/json"
	"strconv"
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

func (mode ObservationMode) Validate() *Failure {
	switch mode {
	case ObservationDefault,
		ObservationSemantic,
		ObservationScreenshot,
		ObservationBoth,
		ObservationNone:
		return nil
	default:
		return NewFailure(ErrorProtocolInvalid, "observation mode is invalid", false)
	}
}

func (mode ObservationMode) Effective() ObservationMode {
	if mode == ObservationDefault {
		return ObservationSemantic
	}
	return mode
}

func (identity Identity) Validate() *Failure {
	for label, value := range map[string]string{
		"run_id":             identity.RunID,
		"agent_id":           identity.AgentID,
		"browser_session_id": identity.BrowserSessionID,
		"attachment_id":      identity.AttachmentID,
	} {
		if !validUUID(value) {
			return NewFailure(ErrorProtocolInvalid, label+" must be a UUID", false)
		}
	}
	if !validOpaqueID(identity.PrincipalScopeID, 256) {
		return NewFailure(ErrorProtocolInvalid, "principal_scope_id is invalid", false)
	}
	if identity.SessionEpoch == 0 {
		return NewFailure(ErrorProtocolInvalid, "session_epoch must be positive", false)
	}
	if identity.ControlEpoch == 0 {
		return NewFailure(ErrorProtocolInvalid, "control_epoch must be positive", false)
	}
	switch identity.Controller {
	case "", ControllerAgent, ControllerNone, ControllerHuman:
		// The protocol/Engine batch lands before the Runtime ownership batch.
		// An omitted controller therefore preserves the pre-controller Agent
		// meaning until that dependent batch starts emitting it explicitly.
	default:
		return NewFailure(ErrorProtocolInvalid, "controller is invalid", false)
	}
	return nil
}

func (observation Observation) Validate() *Failure {
	return observation.ValidateEngine()
}

func (observation Observation) ValidateEngine() *Failure {
	if failure := observation.validateCommon(); failure != nil {
		return failure
	}
	legacyEvidence := observation.Viewport == nil &&
		observation.NavigationGeneration == 0 &&
		(observation.Screenshot == nil ||
			(observation.Screenshot.Width == 0 &&
				observation.Screenshot.Height == 0))
	if !legacyEvidence {
		if observation.Viewport == nil ||
			observation.Viewport.Width != BrowserViewportWidth ||
			observation.Viewport.Height != BrowserViewportHeight {
			return NewFailure(ErrorOutputInvalid, "browser viewport is invalid", false)
		}
		if observation.NavigationGeneration == 0 {
			return NewFailure(ErrorOutputInvalid, "navigation_generation must be positive", false)
		}
		if observation.Screenshot != nil &&
			(observation.Screenshot.Width != BrowserViewportWidth ||
				observation.Screenshot.Height != BrowserViewportHeight) {
			return NewFailure(ErrorOutputInvalid, "screenshot dimensions are invalid", false)
		}
	}
	switch {
	case observation.ClickEffect == "" && observation.TargetCategory == "":
	case observation.ClickEffect == ClickEffectActivated &&
		observation.TargetCategory == TargetCategoryLink:
	case observation.ClickEffect == ClickEffectFocused &&
		observation.TargetCategory == TargetCategoryTextInput:
	default:
		return NewFailure(ErrorOutputInvalid, "browser click effect is invalid", false)
	}
	if observation.Environment != nil {
		if failure := observation.Environment.Validate(); failure != nil {
			return failure
		}
	}
	if observation.SiteOutcome == "" {
		if observation.ClassifierRulesVersion != "" ||
			observation.ChallengeReleaseUnavailable {
			return NewFailure(ErrorOutputInvalid, "browser observation site evidence is invalid", false)
		}
	} else if observation.SiteOutcome != ErrorChallengeSuspected ||
		observation.ClassifierRulesVersion != ChallengeClassifierRulesVersion ||
		!observation.ChallengeReleaseUnavailable {
		return NewFailure(ErrorOutputInvalid, "browser observation site evidence is invalid", false)
	}
	return nil
}

func (observation Observation) ValidateClosed() *Failure {
	if observation.PageStateID == "" ||
		len(observation.PageStateID) > 256 ||
		!strings.HasPrefix(observation.PageStateID, "closed-") {
		return NewFailure(ErrorOutputInvalid, "closed page_state_id is invalid", false)
	}
	if observation.Viewport != nil ||
		observation.NavigationGeneration != 0 ||
		observation.Screenshot != nil ||
		len(observation.AXTree) != 0 ||
		len(observation.DOMDiff) != 0 ||
		observation.AXTreeTimedOut ||
		observation.DOMDiffTimedOut ||
		observation.Origin != "" ||
		observation.Title != "" ||
		observation.ClickEffect != "" ||
		observation.TargetCategory != "" ||
		observation.Environment != nil ||
		observation.SiteOutcome != "" ||
		observation.ClassifierRulesVersion != "" ||
		observation.ChallengeReleaseUnavailable {
		return NewFailure(ErrorOutputInvalid, "closed observation contains Engine fields", false)
	}
	return nil
}

func (observation Observation) validateCommon() *Failure {
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
		if observation.Screenshot.Width < 0 ||
			observation.Screenshot.Height < 0 ||
			(observation.Screenshot.Width == 0) !=
				(observation.Screenshot.Height == 0) {
			return NewFailure(ErrorOutputInvalid, "screenshot dimensions are invalid", false)
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
		ErrorEngineUnavailable,
		ErrorEgressUnavailable,
		ErrorTargetBlocked,
		ErrorProfileLocked,
		ErrorProfileCorrupt,
		ErrorProfileEnvironmentMismatch,
		ErrorProfileEngineDowngrade,
		ErrorProfileEngineUpgrade,
		ErrorUserActionRequired,
		ErrorHighImpactActionBlocked,
		ErrorAccessDenied,
		ErrorRateLimited,
		ErrorChallengeSuspected,
		ErrorChallengeRequired,
		ErrorOriginRateLimited,
		ErrorViewerUnavailable,
		ErrorActionLimitExceeded,
		ErrorClickRetryExhausted,
		ErrorCloseRetryExhausted,
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
	if failure.ActionIndex != nil &&
		(*failure.ActionIndex < 0 || *failure.ActionIndex >= 8) {
		return NewFailure(ErrorOutputInvalid, "browser failure action index is invalid", false)
	}
	hasClickContext := failure.TargetCategory != "" ||
		failure.PageStateID != "" ||
		failure.NavigationGeneration != 0 ||
		failure.BlockedClickNavigationAttemptsRemaining != nil ||
		failure.BlockedClickRunAttemptsRemaining != nil
	switch failure.Code {
	case ErrorHighImpactActionBlocked:
		if !validTargetCategory(failure.TargetCategory) ||
			failure.PageStateID == "" ||
			len(failure.PageStateID) > 256 ||
			failure.NavigationGeneration == 0 {
			return NewFailure(ErrorOutputInvalid, "browser blocked-click evidence is invalid", false)
		}
	case ErrorClickRetryExhausted:
		if !validTargetCategory(failure.TargetCategory) ||
			failure.PageStateID == "" ||
			len(failure.PageStateID) > 256 ||
			failure.NavigationGeneration == 0 ||
			failure.BlockedClickNavigationAttemptsRemaining == nil ||
			failure.BlockedClickRunAttemptsRemaining == nil {
			return NewFailure(ErrorOutputInvalid, "browser click retry evidence is invalid", false)
		}
	default:
		if hasClickContext {
			return NewFailure(ErrorOutputInvalid, "browser failure contains unexpected click evidence", false)
		}
	}
	if remaining := failure.BlockedClickNavigationAttemptsRemaining; remaining != nil &&
		(*remaining < 0 || *remaining > 3) {
		return NewFailure(ErrorOutputInvalid, "browser navigation click retry budget is invalid", false)
	}
	if remaining := failure.BlockedClickRunAttemptsRemaining; remaining != nil &&
		(*remaining < 0 || *remaining > 12) {
		return NewFailure(ErrorOutputInvalid, "browser Run click retry budget is invalid", false)
	}
	if evidenceFailure := validateSiteFailureEvidence(failure); evidenceFailure != nil {
		return evidenceFailure
	}
	if failure.HumanControlAvailable &&
		failure.Code != ErrorUserActionRequired &&
		failure.Code != ErrorChallengeRequired &&
		failure.Code != ErrorViewerUnavailable {
		return NewFailure(
			ErrorOutputInvalid,
			"browser failure contains unexpected human-control availability",
			false,
		)
	}
	return nil
}

func (evidence EnvironmentEvidence) Validate() *Failure {
	if evidence.BrowserEngine != "chromium" && evidence.BrowserEngine != "chrome" {
		return NewFailure(ErrorOutputInvalid, "browser engine evidence is invalid", false)
	}
	switch evidence.BrowserDistribution {
	case "playwright_chromium":
		if evidence.BrowserEngine != "chromium" {
			return NewFailure(ErrorOutputInvalid, "browser distribution evidence is invalid", false)
		}
	case "google_chrome", "chrome_for_testing":
		if evidence.BrowserEngine != "chrome" {
			return NewFailure(ErrorOutputInvalid, "browser distribution evidence is invalid", false)
		}
	default:
		return NewFailure(ErrorOutputInvalid, "browser distribution evidence is invalid", false)
	}
	if evidence.BrowserMajorVersion < 1 || evidence.BrowserMajorVersion > 1000 {
		return NewFailure(ErrorOutputInvalid, "browser version evidence is invalid", false)
	}
	versionParts := strings.Split(evidence.BrowserVersion, ".")
	if !validBrowserVersion(evidence.BrowserVersion) ||
		versionParts[0] != strconv.Itoa(evidence.BrowserMajorVersion) {
		return NewFailure(ErrorOutputInvalid, "browser version evidence is invalid", false)
	}
	if !validLocaleEvidence(evidence.BrowserLocale) ||
		!validTimezoneEvidence(evidence.BrowserTimezone) ||
		!validOpaqueID(evidence.FontContractVersion, 64) ||
		!validLowerHex(evidence.FontManifestSHA256, 64) {
		return NewFailure(ErrorOutputInvalid, "browser environment evidence is invalid", false)
	}
	return nil
}

func validBrowserVersion(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for index, part := range parts {
		if part == "" || len(part) > 8 || (len(part) > 1 && part[0] == '0') {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		if index == 0 && part == "0" {
			return false
		}
	}
	return true
}

func validateSiteFailureEvidence(failure *Failure) *Failure {
	if failure.SiteOutcome == "" {
		if failure.RetryAfterMS != nil ||
			failure.ClassifierRulesVersion != "" ||
			failure.ConsecutiveAccessDenials != nil ||
			failure.OriginBlockedForAttachment {
			return NewFailure(ErrorOutputInvalid, "browser site evidence has no outcome", false)
		}
		return nil
	}
	switch failure.SiteOutcome {
	case ErrorAccessDenied,
		ErrorRateLimited,
		ErrorChallengeSuspected,
		ErrorChallengeRequired,
		ErrorOriginRateLimited:
	default:
		return NewFailure(ErrorOutputInvalid, "browser site outcome is invalid", false)
	}
	if failure.Code != failure.SiteOutcome {
		return NewFailure(ErrorOutputInvalid, "browser site outcome does not match the failure", false)
	}
	if (failure.SiteOutcome == ErrorChallengeSuspected ||
		failure.SiteOutcome == ErrorChallengeRequired) !=
		(failure.ClassifierRulesVersion != "") {
		return NewFailure(ErrorOutputInvalid, "browser classifier version evidence is invalid", false)
	}
	if failure.ClassifierRulesVersion != "" &&
		failure.ClassifierRulesVersion != ChallengeClassifierRulesVersion {
		return NewFailure(ErrorOutputInvalid, "browser classifier version is unsupported", false)
	}
	if failure.RetryAfterMS != nil {
		if *failure.RetryAfterMS < 1 || *failure.RetryAfterMS > 15*60*1000 ||
			(failure.SiteOutcome != ErrorRateLimited &&
				failure.SiteOutcome != ErrorOriginRateLimited) {
			return NewFailure(ErrorOutputInvalid, "browser retry-after evidence is invalid", false)
		}
	}
	if failure.ConsecutiveAccessDenials != nil {
		if failure.SiteOutcome != ErrorAccessDenied ||
			*failure.ConsecutiveAccessDenials < 1 ||
			*failure.ConsecutiveAccessDenials > 3 {
			return NewFailure(ErrorOutputInvalid, "browser access-denial evidence is invalid", false)
		}
	}
	if failure.OriginBlockedForAttachment &&
		(failure.ConsecutiveAccessDenials == nil ||
			*failure.ConsecutiveAccessDenials != 3) {
		return NewFailure(ErrorOutputInvalid, "browser origin-block evidence is invalid", false)
	}
	return nil
}

func validLocaleEvidence(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func validTimezoneEvidence(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value ||
		strings.HasPrefix(value, "/") || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("_+-/", character) {
			continue
		}
		return false
	}
	return true
}

func validLowerHex(value string, expectedLength int) bool {
	if len(value) != expectedLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validTargetCategory(category TargetCategory) bool {
	switch category {
	case TargetCategoryLink,
		TargetCategoryTextInput,
		TargetCategoryButton,
		TargetCategoryCustom,
		TargetCategoryNone,
		TargetCategoryOther:
		return true
	default:
		return false
	}
}
