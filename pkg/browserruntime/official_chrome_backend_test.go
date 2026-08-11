//go:build !windows

package browserruntime

import (
	"slices"
	"testing"
)

func TestOfficialChromeEnvironmentPreservesLimitsAndOverridesEngineIdentity(
	t *testing.T,
) {
	environment := officialChromeEnvironment(
		[]string{
			"HOME=/browser-home",
			"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE=120",
			"OPENLINKER_BROWSER_ENGINE=chromium",
			"OPENLINKER_BROWSER_ENGINE=must-not-survive",
		},
		[]string{
			"OPENLINKER_BROWSER_ENGINE=chrome",
			"OPENLINKER_BROWSER_DISTRIBUTION=google_chrome",
		},
	)
	for _, expected := range []string{
		"HOME=/browser-home",
		"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE=120",
		"OPENLINKER_BROWSER_ENGINE=chrome",
		"OPENLINKER_BROWSER_DISTRIBUTION=google_chrome",
	} {
		if !slices.Contains(environment, expected) {
			t.Fatalf("official Chrome environment omitted %q: %#v", expected, environment)
		}
	}
	engineEntries := 0
	for _, entry := range environment {
		if entry == "OPENLINKER_BROWSER_ENGINE=chrome" {
			engineEntries++
		}
	}
	if engineEntries != 1 {
		t.Fatalf("official Chrome engine entries = %d: %#v", engineEntries, environment)
	}
}
