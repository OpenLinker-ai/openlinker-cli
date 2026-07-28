//go:build !windows

package main

import (
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func TestRebindAttemptURLPreservesHostAndForcesANewRequest(t *testing.T) {
	first, err := rebindAttemptURL(
		"http://fixture.example/path?existing=value",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rebindAttemptURL(
		"http://fixture.example/path?existing=value",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first !=
		"http://fixture.example/path?existing=value&openlinker_rebind_attempt=1" {
		t.Fatalf("first attempt URL = %q", first)
	}
	if second !=
		"http://fixture.example/path?existing=value&openlinker_rebind_attempt=2" {
		t.Fatalf("second attempt URL = %q", second)
	}
}

func TestRebindAttemptURLRejectsMalformedOrNonHTTPTargets(t *testing.T) {
	for _, target := range []string{
		"://bad",
		"https://fixture.example/",
		"http:///missing-host",
	} {
		if _, err := rebindAttemptURL(target, 0); err == nil {
			t.Errorf("accepted invalid rebind target %q", target)
		}
	}
}

func TestValidateMCPAttachmentEvidenceRequiresBoundedConstants(t *testing.T) {
	environment := browserprotocol.EnvironmentEvidence{
		BrowserEngine:       "chromium",
		BrowserDistribution: "playwright_chromium",
		BrowserVersion:      "149.0.7827.0",
		BrowserMajorVersion: 149,
		BrowserLocale:       "en-US",
		BrowserTimezone:     "UTC",
		FontContractVersion: "openlinker.browser.fonts.v1",
		FontManifestSHA256:  strings.Repeat("a", 64),
	}
	actual := map[string]any{
		"browser_engine":        "chromium",
		"browser_distribution":  "playwright_chromium",
		"browser_major_version": float64(149),
		"browser_locale":        "en-US",
		"browser_timezone":      "UTC",
		"font_contract_version": "openlinker.browser.fonts.v1",
		"font_manifest_sha256":  strings.Repeat("a", 64),
	}
	if err := validateMCPAttachmentEvidence(actual, environment); err != nil {
		t.Fatal(err)
	}
	actual["browser_version"] = environment.BrowserVersion
	if err := validateMCPAttachmentEvidence(actual, environment); err == nil {
		t.Fatal("accepted attachment evidence containing the full Browser version")
	}
}
