//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserplugin"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

const (
	defaultControlRoot = "/browser-control"
	waitTimeout        = 45 * time.Second
	actionTimeout      = 55 * time.Second
)

type result struct {
	Status             string `json:"status"`
	Mode               string `json:"mode"`
	Origin             string `json:"origin,omitempty"`
	Title              string `json:"title,omitempty"`
	ScreenshotSHA256   string `json:"screenshot_sha256,omitempty"`
	ContinuationOrigin string `json:"continuation_origin,omitempty"`
}

func main() {
	mode := flag.String(
		"mode",
		"full",
		"acceptance mode: full, mcp-evidence, or gateway-down",
	)
	publicURL := flag.String("public-url", "", "public HTTPS acceptance fixture URL")
	primeURL := flag.String(
		"prime-url",
		"",
		"optional public HTTPS URL used to establish fixture routing state",
	)
	rebindURL := flag.String(
		"rebind-url",
		"http://make-1.1.1.1-rebindfor2m-127.0.0.1-rr-set-1-ttl.1u.ms/",
		"controlled DNS rebinding URL; empty disables this probe",
	)
	expectedBrowserVersion := flag.String(
		"expected-browser-version",
		"",
		"locked Browser version expected from preflight",
	)
	expectedFontSHA256 := flag.String(
		"expected-font-sha256",
		"",
		"locked font manifest expected from preflight",
	)
	controlRoot := flag.String("control-root", defaultControlRoot, "Browser control volume")
	flag.Parse()

	output, err := run(
		strings.TrimSpace(*mode),
		strings.TrimSpace(*publicURL),
		strings.TrimSpace(*primeURL),
		strings.TrimSpace(*rebindURL),
		strings.TrimSpace(*expectedBrowserVersion),
		strings.TrimSpace(*expectedFontSHA256),
		filepath.Clean(strings.TrimSpace(*controlRoot)),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser image acceptance:", err)
		os.Exit(1)
	}
	raw, err := json.Marshal(output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser image acceptance: encode result")
		os.Exit(1)
	}
	fmt.Println(string(raw))
}

func run(
	mode,
	publicURL,
	primeURL,
	rebindURL,
	expectedBrowserVersion,
	expectedFontSHA256,
	controlRoot string,
) (result, error) {
	if mode != "full" && mode != "mcp-evidence" && mode != "gateway-down" {
		return result{}, errors.New("unsupported acceptance mode")
	}
	if mode != "mcp-evidence" {
		if failure := (browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  publicURL,
		}).Validate(); failure != nil {
			return result{}, errors.New("public fixture URL is invalid")
		}
	}
	if primeURL != "" {
		if failure := (browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  primeURL,
		}).Validate(); failure != nil {
			return result{}, errors.New("public fixture prime URL is invalid")
		}
	}
	if !filepath.IsAbs(controlRoot) || filepath.Clean(controlRoot) != controlRoot {
		return result{}, errors.New("control root must be an absolute clean path")
	}
	socketPath := filepath.Join(controlRoot, "openlinker.browser.sock")
	credentialPath := filepath.Join(controlRoot, "channel-credential")
	leasePath := filepath.Join(controlRoot, "leases", "active-lease.json")
	if err := waitForPrivateFile(credentialPath, waitTimeout); err != nil {
		return result{}, err
	}
	if err := waitForSocket(socketPath, waitTimeout); err != nil {
		return result{}, err
	}

	if mode == "gateway-down" {
		identity := identityFor(90, 90, 90, 1, 1)
		client, err := activateClient(socketPath, credentialPath, leasePath, identity)
		if err != nil {
			return result{}, err
		}
		_, failure := execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         publicURL,
		})
		if failure == nil ||
			failure.Code != browserprotocol.ErrorEgressUnavailable ||
			!failure.Recoverable {
			return result{}, fmt.Errorf(
				"Gateway-down navigation did not fail recoverably: %v",
				failure,
			)
		}
		return result{Status: "passed", Mode: mode}, nil
	}

	if mode == "mcp-evidence" {
		identity := identityFor(91, 91, 91, 1, 1)
		client, err := activateClient(
			socketPath,
			credentialPath,
			leasePath,
			identity,
		)
		if err != nil {
			return result{}, err
		}
		preflight, failure := execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			Observation: browserprotocol.ObservationSemantic,
		})
		if failure != nil {
			return result{}, fmt.Errorf("MCP evidence preflight failed: %w", failure)
		}
		if err := validateEnvironmentEvidence(
			preflight.Environment,
			expectedBrowserVersion,
			expectedFontSHA256,
		); err != nil {
			return result{}, err
		}
		if err := validateMCPEvidenceRecovery(
			client,
			*preflight.Environment,
			identity,
		); err != nil {
			return result{}, err
		}
		if _, failure := execute(client, browserprotocol.Action{
			Kind: browserprotocol.ActionClose,
		}); failure != nil {
			return result{}, fmt.Errorf("MCP evidence attachment close failed: %w", failure)
		}
		return result{Status: "passed", Mode: mode}, nil
	}

	firstIdentity := identityFor(1, 1, 1, 1, 1)
	first, err := activateClient(socketPath, credentialPath, leasePath, firstIdentity)
	if err != nil {
		return result{}, err
	}
	preflight, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionPreflight,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil {
		return result{}, fmt.Errorf("preflight failed: %w", failure)
	}
	if err := validateEnvironmentEvidence(
		preflight.Environment,
		expectedBrowserVersion,
		expectedFontSHA256,
	); err != nil {
		return result{}, err
	}
	if primeURL != "" {
		if _, failure := execute(first, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         primeURL,
		}); failure != nil {
			return result{}, fmt.Errorf("public fixture prime navigation failed: %w", failure)
		}
	}
	observation, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationBoth,
		URL:         publicURL,
	})
	if failure != nil {
		return result{}, fmt.Errorf("public navigation failed: %w", failure)
	}
	if observation.Screenshot == nil || len(observation.Screenshot.Data) == 0 {
		return result{}, errors.New("public navigation returned no screenshot")
	}
	if observation.Viewport == nil ||
		observation.Viewport.Width != browserprotocol.BrowserViewportWidth ||
		observation.Viewport.Height != browserprotocol.BrowserViewportHeight ||
		observation.NavigationGeneration == 0 ||
		observation.Screenshot.Width != browserprotocol.BrowserViewportWidth ||
		observation.Screenshot.Height != browserprotocol.BrowserViewportHeight {
		return result{}, fmt.Errorf(
			"public navigation dimensions are invalid: %#v",
			observation,
		)
	}
	decodedScreenshot, _, err := image.DecodeConfig(
		bytes.NewReader(observation.Screenshot.Data),
	)
	if err != nil ||
		decodedScreenshot.Width != observation.Screenshot.Width ||
		decodedScreenshot.Height != observation.Screenshot.Height {
		return result{}, fmt.Errorf(
			"public screenshot encoded dimensions are invalid: decoded=%dx%d reported=%dx%d error=%v",
			decodedScreenshot.Width,
			decodedScreenshot.Height,
			observation.Screenshot.Width,
			observation.Screenshot.Height,
			err,
		)
	}
	expectedOrigin, err := publicOrigin(publicURL)
	if err != nil || observation.Origin != expectedOrigin {
		return result{}, fmt.Errorf(
			"public navigation origin mismatch: got %q want %q",
			observation.Origin,
			expectedOrigin,
		)
	}
	if observation.Title != "OpenLinker Browser Acceptance" {
		return result{}, fmt.Errorf("public fixture title mismatch: %q", observation.Title)
	}
	screenshotDigest := sha256.Sum256(observation.Screenshot.Data)

	waitMS := 2500
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionWait,
		Observation: browserprotocol.ObservationSemantic,
		DurationMS:  &waitMS,
	})
	if failure != nil {
		return result{}, fmt.Errorf("fixture settle failed: %w", failure)
	}
	semantic, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	if err != nil {
		return result{}, errors.New("encode fixture semantics")
	}
	for _, marker := range []string{
		"post=blocked",
		"websocket=blocked",
		"service_worker=blocked",
		"webrtc_probe=complete",
		"webtransport_probe=complete",
	} {
		if !strings.Contains(string(semantic), marker) {
			return result{}, fmt.Errorf(
				"fixture did not prove %s (pending=%t allowed=%t)",
				marker,
				strings.Contains(
					string(semantic),
					strings.Replace(marker, "blocked", "pending", 1),
				),
				strings.Contains(
					string(semantic),
					strings.Replace(marker, "blocked", "allowed", 1),
				),
			)
		}
	}

	clickX, clickY := 240, 140
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationBoth,
		X:           &clickX,
		Y:           &clickY,
	})
	if failure != nil {
		return result{}, fmt.Errorf(
			"safe search focus failed: code=%s recoverable=%t category=%s state=%s generation=%d",
			failure.Code,
			failure.Recoverable,
			failure.TargetCategory,
			failure.PageStateID,
			failure.NavigationGeneration,
		)
	}
	if observation.ClickEffect != browserprotocol.ClickEffectFocused ||
		observation.TargetCategory != browserprotocol.TargetCategoryTextInput {
		return result{}, fmt.Errorf(
			"safe search click effect = %q/%q",
			observation.ClickEffect,
			observation.TargetCategory,
		)
	}
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "openlinker-browser",
	})
	if failure != nil {
		return result{}, fmt.Errorf("safe search typing failed: %w", failure)
	}
	interactionState, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	if err != nil {
		return result{}, errors.New("encode search interaction state")
	}
	for _, marker := range []string{
		"pointerdown=0",
		"mousedown=0",
		"click=0",
	} {
		if !strings.Contains(string(interactionState), marker) {
			return result{}, fmt.Errorf("focus-only input dispatched %s", marker)
		}
	}
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionKeypress,
		Observation: browserprotocol.ObservationSemantic,
		Key:         "Enter",
	})
	if failure != nil {
		return result{}, fmt.Errorf("safe GET search submit failed: %w", failure)
	}
	searchState, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	if err != nil ||
		observation.Title != "OpenLinker Search Result" ||
		!strings.Contains(string(searchState), "query=openlinker-browser") {
		return result{}, fmt.Errorf(
			"safe GET search result is invalid: title=%q state=%s",
			observation.Title,
			searchState,
		)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         publicURL,
	}); failure != nil {
		return result{}, fmt.Errorf("return from search result failed: %w", failure)
	}

	for _, target := range []string{
		"http://127.0.0.1/",
		"http://[::1]/",
		"http://10.0.0.1/",
		"http://172.16.0.1/",
		"http://192.168.0.1/",
		"http://[fc00::1]/",
		"http://[fe80::1]/",
		"http://2130706433/",
		"http://169.254.169.254/",
	} {
		_, failure := first.Execute(context.Background(), browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  target,
		})
		if failure == nil || failure.Code != browserprotocol.ErrorProtocolInvalid {
			return result{}, fmt.Errorf("private literal was not rejected: %s", target)
		}
	}
	for _, target := range []string{
		"http://127.0.0.1.nip.io/",
		"http://100.64.0.1.nip.io/",
		"http://169.254.169.254.nip.io/",
	} {
		if err := expectTargetBlocked(first, target); err != nil {
			return result{}, err
		}
	}
	if err := expectTargetBlocked(
		first,
		strings.TrimRight(publicURL, "/")+"/redirect-private",
	); err != nil {
		return result{}, err
	}
	if rebindURL != "" {
		if err := expectRebindingBlocked(first, rebindURL); err != nil {
			return result{}, err
		}
	}

	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         publicURL,
	}); failure != nil {
		return result{}, fmt.Errorf("return to fixture failed: %w", failure)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionCheckpoint,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("checkpoint failed: %w", failure)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("close failed: %w", failure)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	}); failure == nil ||
		(failure.Code != browserprotocol.ErrorIdentityMismatch &&
			failure.Code != browserprotocol.ErrorStaleControlEpoch) {
		return result{}, fmt.Errorf("closed attachment was reusable: %v", failure)
	}

	resumedIdentity := identityFor(2, 1, 2, 1, 2)
	resumed, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		resumedIdentity,
	)
	if err != nil {
		return result{}, err
	}
	resumedObservation, failure := execute(resumed, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil {
		return result{}, fmt.Errorf("same-Session continuation failed: %w", failure)
	}
	if resumedObservation.Origin != expectedOrigin ||
		resumedObservation.Title != "OpenLinker Browser Acceptance" {
		return result{}, fmt.Errorf(
			"same-Session continuation restored the wrong page: %q %q",
			resumedObservation.Origin,
			resumedObservation.Title,
		)
	}
	if _, failure := execute(resumed, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("resumed attachment close failed: %w", failure)
	}

	isolatedIdentity := identityFor(3, 2, 3, 1, 3)
	isolated, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		isolatedIdentity,
	)
	if err != nil {
		return result{}, err
	}
	isolatedObservation, failure := execute(isolated, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil {
		return result{}, fmt.Errorf("different-Session blank start failed: %w", failure)
	}
	if isolatedObservation.Origin != "" || isolatedObservation.Title != "" {
		return result{}, fmt.Errorf(
			"different Session inherited page state: %q %q",
			isolatedObservation.Origin,
			isolatedObservation.Title,
		)
	}
	if _, failure := execute(isolated, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("isolated attachment close failed: %w", failure)
	}
	if err := runReliabilityFixtures(
		socketPath,
		credentialPath,
		leasePath,
		publicURL,
	); err != nil {
		return result{}, err
	}

	return result{
		Status:             "passed",
		Mode:               mode,
		Origin:             observation.Origin,
		Title:              observation.Title,
		ScreenshotSHA256:   hex.EncodeToString(screenshotDigest[:]),
		ContinuationOrigin: resumedObservation.Origin,
	}, nil
}

func validateEnvironmentEvidence(
	evidence *browserprotocol.EnvironmentEvidence,
	expectedVersion,
	expectedFontSHA256 string,
) error {
	if expectedVersion == "" || expectedFontSHA256 == "" {
		return errors.New("locked Browser and font versions are required")
	}
	if evidence == nil {
		return errors.New("preflight returned no Browser environment evidence")
	}
	if evidence.BrowserEngine != "chromium" ||
		evidence.BrowserDistribution != "playwright_chromium" ||
		evidence.BrowserVersion != expectedVersion ||
		evidence.BrowserMajorVersion <= 0 ||
		!strings.HasPrefix(
			evidence.BrowserVersion,
			fmt.Sprintf("%d.", evidence.BrowserMajorVersion),
		) ||
		evidence.BrowserLocale != "en-US" ||
		evidence.BrowserTimezone != "UTC" ||
		evidence.FontContractVersion != "openlinker.browser.fonts.v1" ||
		evidence.FontManifestSHA256 != expectedFontSHA256 {
		return fmt.Errorf(
			"preflight Browser environment evidence is invalid: %#v",
			evidence,
		)
	}
	return nil
}

func runReliabilityFixtures(
	socketPath,
	credentialPath,
	leasePath,
	publicURL string,
) error {
	base := strings.TrimRight(publicURL, "/")
	suspectedIdentity := identityFor(10, 10, 10, 1, 10)
	suspected, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		suspectedIdentity,
	)
	if err != nil {
		return err
	}
	_, failure := execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/challenge-suspected",
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeSuspected,
		true,
	); err != nil {
		return fmt.Errorf("suspected challenge classification: %w", err)
	}
	clickX, clickY := 240, 140
	_, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           &clickX,
		Y:           &clickY,
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeSuspected,
		true,
	); err != nil {
		return fmt.Errorf("same-document history released challenge gate: %w", err)
	}
	suspectedObservation, failure := execute(
		suspected,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionScreenshot,
			Observation: browserprotocol.ObservationSemantic,
		},
	)
	if failure != nil ||
		!observationContains(suspectedObservation, "same_document_history=advanced") {
		return fmt.Errorf(
			"suspected challenge same-document fixture is invalid: %v",
			failure,
		)
	}
	if _, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/plain",
	}); failure != nil {
		return fmt.Errorf("clean cross-document navigation failed: %w", failure)
	}
	_, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionBack,
		Observation: browserprotocol.ObservationSemantic,
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeSuspected,
		true,
	); err != nil {
		return fmt.Errorf("BFCache challenge restore was not reclassified: %w", err)
	}
	restoredObservation, failure := execute(
		suspected,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionScreenshot,
			Observation: browserprotocol.ObservationSemantic,
		},
	)
	if failure != nil ||
		!observationContains(restoredObservation, "pageshow_persisted=true") {
		return fmt.Errorf(
			"real Chromium did not exercise the BFCache restore fixture: %v",
			failure,
		)
	}
	if _, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionForward,
		Observation: browserprotocol.ObservationSemantic,
	}); failure != nil {
		return fmt.Errorf("clean forward navigation did not release challenge gate: %w", failure)
	}
	if _, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("suspected challenge attachment close failed: %w", failure)
	}

	deniedIdentity := identityFor(20, 20, 20, 1, 20)
	denied, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		deniedIdentity,
	)
	if err != nil {
		return err
	}
	for attempt := 1; attempt <= 4; attempt++ {
		_, failure = execute(denied, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         base + "/access-denied",
		})
		if failure == nil ||
			failure.Code != browserprotocol.ErrorAccessDenied ||
			failure.ConsecutiveAccessDenials == nil ||
			*failure.ConsecutiveAccessDenials != min(3, attempt) ||
			failure.OriginBlockedForAttachment != (attempt >= 3) {
			return fmt.Errorf(
				"access-denial attempt %d returned invalid evidence: %v",
				attempt,
				failure,
			)
		}
	}
	if _, failure = execute(denied, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("access-denial attachment close failed: %w", failure)
	}

	requiredIdentity := identityFor(30, 30, 30, 1, 30)
	required, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		requiredIdentity,
	)
	if err != nil {
		return err
	}
	_, failure = execute(required, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/challenge-required",
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeRequired,
		false,
	); err != nil {
		return fmt.Errorf("required challenge classification: %w", err)
	}
	if _, terminalFailure := execute(required, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	}); terminalFailure == nil {
		return errors.New("required challenge attachment remained reusable")
	}
	recoveredIdentity := identityFor(31, 31, 31, 1, 31)
	recovered, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		recoveredIdentity,
	)
	if err != nil {
		return err
	}
	if _, failure = execute(recovered, browserprotocol.Action{
		Kind:        browserprotocol.ActionPreflight,
		Observation: browserprotocol.ObservationSemantic,
	}); failure != nil {
		return fmt.Errorf("Runtime did not recover after required challenge: %w", failure)
	}
	if _, failure = execute(recovered, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("recovered attachment close failed: %w", failure)
	}

	rateIdentity := identityFor(40, 40, 40, 1, 40)
	rateLimited, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		rateIdentity,
	)
	if err != nil {
		return err
	}
	_, failure = execute(rateLimited, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/rate-limited",
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorRateLimited ||
		failure.RetryAfterMS == nil ||
		*failure.RetryAfterMS < 1 ||
		*failure.RetryAfterMS > 2_000 {
		return fmt.Errorf("429 fixture returned invalid Retry-After evidence: %v", failure)
	}
	_, failure = execute(rateLimited, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorOriginRateLimited ||
		failure.RetryAfterMS == nil {
		return fmt.Errorf("429 retry budget did not reject locally: %v", failure)
	}
	time.Sleep(2100 * time.Millisecond)
	if _, failure = execute(rateLimited, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("rate-limited attachment close failed: %w", failure)
	}
	return nil
}

func expectChallengeFailure(
	failure *browserprotocol.Failure,
	code browserprotocol.ErrorCode,
	recoverable bool,
) error {
	if failure == nil ||
		failure.Code != code ||
		failure.SiteOutcome != code ||
		failure.Recoverable != recoverable ||
		failure.ClassifierRulesVersion != browserprotocol.ChallengeClassifierRulesVersion {
		return fmt.Errorf("invalid challenge failure: %v", failure)
	}
	return nil
}

func observationContains(
	observation browserprotocol.Observation,
	marker string,
) bool {
	encoded, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	return err == nil && strings.Contains(string(encoded), marker)
}

func identityFor(
	run, session, attachment, sessionEpoch, controlEpoch int,
) browserprotocol.Identity {
	return browserprotocol.Identity{
		RunID:            fmt.Sprintf("00000000-0000-4000-8000-%012d", run),
		AgentID:          "00000000-0000-4000-8000-000000000100",
		PrincipalScopeID: "ps1_browser_image_acceptance_scope",
		BrowserSessionID: fmt.Sprintf("00000000-0000-4000-8000-%012d", session+100),
		SessionEpoch:     uint64(sessionEpoch),
		AttachmentID:     fmt.Sprintf("00000000-0000-4000-8000-%012d", attachment+200),
		ControlEpoch:     uint64(controlEpoch),
		Controller:       browserprotocol.ControllerAgent,
	}
}

func activateClient(
	socketPath,
	credentialPath,
	leasePath string,
	identity browserprotocol.Identity,
) (*browserclient.Client, error) {
	lease := browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  time.Now().UTC().Add(10 * time.Minute),
		Identity:   identity,
	}
	if err := writeLease(leasePath, lease); err != nil {
		return nil, err
	}
	client, err := browserclient.New(browserclient.Config{
		SocketPath:     socketPath,
		CredentialFile: credentialPath,
		LeaseFile:      leasePath,
		Timeout:        actionTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create Browser client: %w", err)
	}
	return client, nil
}

func execute(
	client *browserclient.Client,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	return client.Execute(ctx, action)
}

func validateMCPEvidenceRecovery(
	client *browserclient.Client,
	environment browserprotocol.EnvironmentEvidence,
	identity browserprotocol.Identity,
) error {
	for session := 1; session <= 2; session++ {
		evidenceResults, err := runMCPEvidenceSession(
			client,
			environment,
			identity,
		)
		if err != nil {
			return fmt.Errorf("MCP evidence session %d failed: %w", session, err)
		}
		if len(evidenceResults) != 2 {
			return fmt.Errorf(
				"MCP evidence session %d returned %d tool results",
				session,
				len(evidenceResults),
			)
		}
		evidenceCount := 0
		for _, result := range evidenceResults {
			evidence, ok := result["attachment_evidence"].(map[string]any)
			if !ok {
				continue
			}
			evidenceCount++
			if err := validateMCPAttachmentEvidence(evidence, environment); err != nil {
				return fmt.Errorf("MCP evidence session %d: %w", session, err)
			}
		}
		if evidenceCount != 1 {
			return fmt.Errorf(
				"MCP evidence session %d emitted attachment evidence %d times",
				session,
				evidenceCount,
			)
		}
	}
	return nil
}

func runMCPEvidenceSession(
	client *browserclient.Client,
	environment browserprotocol.EnvironmentEvidence,
	identity browserprotocol.Identity,
) ([]map[string]any, error) {
	server := &browserplugin.Server{
		Host: "codex",
		IO: shared.IO{
			Getenv: func(string) string { return "" },
		},
		ClientFactory: func() (browserplugin.Executor, error) {
			return client, nil
		},
		EvidenceSupplier: func() (browserplugin.EvidenceSnapshot, error) {
			return browserplugin.EvidenceSnapshot{
				Environment:      environment,
				BrowserSessionID: identity.BrowserSessionID,
				SessionEpoch:     identity.SessionEpoch,
				ControlEpoch:     identity.ControlEpoch,
			}, nil
		},
	}
	input := strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"browser_session\",\"arguments\":{\"operation\":\"observe\",\"observation\":\"semantic\"}}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"browser_session\",\"arguments\":{\"operation\":\"observe\",\"observation\":\"semantic\"}}}\n",
	)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := server.Serve(ctx, input, &output); err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, 2)
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			return nil, errors.New("decode Browser MCP response")
		}
		id, ok := response["id"].(float64)
		if !ok || (id != 2 && id != 3) {
			continue
		}
		resultValue, ok := response["result"].(map[string]any)
		if !ok {
			return nil, errors.New("Browser MCP tool response has no result")
		}
		structured, ok := resultValue["structuredContent"].(map[string]any)
		if !ok {
			return nil, errors.New("Browser MCP tool response has no structured content")
		}
		results = append(results, structured)
	}
	return results, nil
}

func validateMCPAttachmentEvidence(
	actual map[string]any,
	expected browserprotocol.EnvironmentEvidence,
) error {
	expectedValues := map[string]any{
		"browser_engine":        expected.BrowserEngine,
		"browser_distribution":  expected.BrowserDistribution,
		"browser_major_version": float64(expected.BrowserMajorVersion),
		"browser_locale":        expected.BrowserLocale,
		"browser_timezone":      expected.BrowserTimezone,
		"font_contract_version": expected.FontContractVersion,
		"font_manifest_sha256":  expected.FontManifestSHA256,
	}
	if len(actual) != len(expectedValues) {
		return fmt.Errorf("attachment evidence field count = %d", len(actual))
	}
	for field, expectedValue := range expectedValues {
		if actual[field] != expectedValue {
			return fmt.Errorf(
				"attachment evidence %s = %#v, want %#v",
				field,
				actual[field],
				expectedValue,
			)
		}
	}
	if _, exists := actual["browser_version"]; exists {
		return errors.New("attachment evidence exposed the full Browser version")
	}
	return nil
}

func expectTargetBlocked(client *browserclient.Client, target string) error {
	_, failure := execute(client, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         target,
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorTargetBlocked ||
		failure.Recoverable {
		return fmt.Errorf("target was not blocked permanently: %s: %v", target, failure)
	}
	return nil
}

func expectRebindingBlocked(client *browserclient.Client, target string) error {
	for attempt := 0; attempt < 6; attempt++ {
		attemptURL, err := rebindAttemptURL(target, attempt)
		if err != nil {
			return err
		}
		_, failure := execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         attemptURL,
		})
		if failure != nil &&
			failure.Code == browserprotocol.ErrorTargetBlocked &&
			!failure.Recoverable {
			return nil
		}
		time.Sleep(1200 * time.Millisecond)
	}
	return errors.New("controlled DNS rebinding target was not blocked")
}

func rebindAttemptURL(target string, attempt int) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" {
		return "", errors.New("controlled DNS rebinding URL is invalid")
	}
	query := parsed.Query()
	query.Set("openlinker_rebind_attempt", fmt.Sprintf("%d", attempt+1))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func writeLease(path string, lease browserclient.Lease) error {
	if failure := lease.Validate(time.Now().UTC()); failure != nil {
		return failure
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Browser lease directory is invalid")
	}
	raw, err := json.Marshal(lease)
	if err != nil {
		return errors.New("encode Browser lease")
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(directory, ".acceptance-lease-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func waitForPrivateFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(path)
		if err == nil &&
			info.Mode().IsRegular() &&
			info.Mode()&os.ModeSymlink == 0 &&
			info.Mode().Perm()&0o077 == 0 &&
			info.Size() > 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for private file %s", filepath.Base(path))
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("timed out waiting for Browser Runtime socket")
}

func publicOrigin(raw string) (string, error) {
	action := browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: raw}
	if failure := action.Validate(); failure != nil {
		return "", failure
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("public URL is invalid")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
