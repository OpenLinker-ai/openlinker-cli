//go:build !windows

package browserruntime

import (
	"context"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

type selectorTestBackend struct {
	failure        *browserprotocol.Failure
	fallbackReason string
	calls          []browserprotocol.Action
	closed         bool
	aborted        bool
}

func (backend *selectorTestBackend) Execute(
	_ context.Context,
	_ browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	backend.calls = append(backend.calls, action)
	if backend.failure != nil {
		return browserprotocol.Observation{}, backend.failure
	}
	return browserprotocol.Observation{PageStateID: "test-state"}, nil
}

func (backend *selectorTestBackend) StartupFallbackReason(
	*browserprotocol.Failure,
) string {
	return backend.fallbackReason
}

func (backend *selectorTestBackend) Close() error {
	backend.closed = true
	return nil
}

func (backend *selectorTestBackend) AbortStartup() error {
	backend.aborted = true
	return nil
}

func TestBackendSelectorAutoPrefersOfficialAndLocksSelection(t *testing.T) {
	official := &selectorTestBackend{}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "auto",
		},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendOfficialChrome ||
		observation.BackendSelection.RequestedMode != "auto" ||
		len(official.calls) != 1 || len(isolated.calls) != 0 ||
		official.calls[0].BackendMode != "" {
		t.Fatalf("selection = %#v, official=%#v isolated=%#v", observation.BackendSelection, official.calls, isolated.calls)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionWait, DurationMS: selectorIntPointer(1)},
	); failure != nil || len(official.calls) != 2 {
		t.Fatalf("locked official execution failed: %v", failure)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "isolated"},
	); failure == nil || failure.Code != browserprotocol.ErrorProtocolInvalid {
		t.Fatalf("backend reselection failure = %#v", failure)
	}
}

func TestBackendSelectorReleasesSelectionOnlyAfterSuccessfulClose(t *testing.T) {
	official := &selectorTestBackend{}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := browserprotocol.Identity{}
	if _, failure := selector.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := selector.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	observation, failure := selector.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "isolated"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendIsolated ||
		len(official.calls) != 2 || len(isolated.calls) != 1 {
		t.Fatalf(
			"second selection = %#v, official=%d isolated=%d",
			observation.BackendSelection,
			len(official.calls),
			len(isolated.calls),
		)
	}
}

func TestBackendSelectorDiscardsAStaleGenerationBeforeNewPreflight(t *testing.T) {
	official := &selectorTestBackend{}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := browserprotocol.Identity{AttachmentID: "first"}
	if _, failure := selector.Execute(
		context.Background(),
		first,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	); failure != nil {
		t.Fatal(failure)
	}
	second := first
	second.AttachmentID = "second"
	second.SessionEpoch = 1
	observation, failure := selector.Execute(
		context.Background(),
		second,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "isolated"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if !official.aborted || observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendIsolated {
		t.Fatalf(
			"recovered selection = %#v, official aborted=%v",
			observation.BackendSelection,
			official.aborted,
		)
	}
}

func TestBackendSelectorRepreflightsTheLockedBackendAfterAttachmentRotation(
	t *testing.T,
) {
	official := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         &selectorTestBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := browserprotocol.Identity{AttachmentID: "first", SessionEpoch: 1}
	if _, failure := selector.Execute(
		context.Background(),
		first,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	); failure != nil {
		t.Fatal(failure)
	}
	second := first
	second.AttachmentID = "second"
	second.SessionEpoch++
	observation, failure := selector.Execute(
		context.Background(),
		second,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendOfficialChrome ||
		official.aborted || len(official.calls) != 2 {
		t.Fatalf(
			"rotated selection = %#v, aborted=%v calls=%d",
			observation.BackendSelection,
			official.aborted,
			len(official.calls),
		)
	}
}

func TestBackendSelectorAutoFallsBackBeforeSelectionOnly(t *testing.T) {
	official := &selectorTestBackend{
		failure: browserprotocol.NewFailure(
			browserprotocol.ErrorEngineUnavailable,
			"extension did not load",
			false,
		),
		fallbackReason: "official_extension_unavailable",
	}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendIsolated ||
		observation.BackendSelection.FallbackReason != "official_extension_unavailable" ||
		len(official.calls) != 1 || len(isolated.calls) != 1 {
		t.Fatalf("fallback selection = %#v", observation.BackendSelection)
	}
	if !official.aborted || official.closed {
		t.Fatal("failed official startup was not discarded before fallback")
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://example.com"},
	); failure != nil || len(official.calls) != 1 || len(isolated.calls) != 2 {
		t.Fatalf("post-selection execution switched backend: %v", failure)
	}
}

func TestBackendSelectorStrictOfficialReturnsCompleteSelectionEvidence(t *testing.T) {
	official := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         &selectorTestBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "official-chrome",
		},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.RequestedMode != "official-chrome" ||
		observation.BackendSelection.SelectedBackend != BackendOfficialChrome ||
		observation.BackendSelection.Validate() != nil {
		t.Fatalf("strict official evidence = %#v", observation.BackendSelection)
	}
}

func TestBackendSelectorStrictOfficialNeverFallsBack(t *testing.T) {
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{Isolated: isolated})
	if err != nil {
		t.Fatal(err)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "official-chrome",
		},
	); failure == nil || failure.Code != browserprotocol.ErrorEngineUnavailable ||
		len(isolated.calls) != 0 {
		t.Fatalf("strict official failure = %#v, isolated calls=%d", failure, len(isolated.calls))
	}
}

func TestBackendSelectorStrictOfficialDiscardsFailedStartup(t *testing.T) {
	official := &selectorTestBackend{
		failure: browserprotocol.NewFailure(
			browserprotocol.ErrorEngineUnavailable,
			"Chrome failed to start",
			false,
		),
	}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "official-chrome",
		},
	); failure == nil || !official.aborted || len(isolated.calls) != 0 {
		t.Fatalf(
			"strict failure = %#v, aborted=%v, isolated calls=%d",
			failure,
			official.aborted,
			len(isolated.calls),
		)
	}
}

func validOfficialSelectionEvidence() browserprotocol.BackendSelectionEvidence {
	return browserprotocol.BackendSelectionEvidence{
		AssetManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExtensionID:         "abcdefghijklmnopabcdefghijklmnop",
		ExtensionVersion:    "1.2.3.4",
		NativeHostProtocol:  "openlinker.native-chrome.v1",
	}
}

func selectorIntPointer(value int) *int { return &value }
