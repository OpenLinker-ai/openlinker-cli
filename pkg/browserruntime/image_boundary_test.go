//go:build !windows

package browserruntime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBrowserImageIsSeparatePinnedAndHasNoProviderCredentialSurface(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve browserruntime package path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	browserDockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.browser"))
	if err != nil {
		t.Fatal(err)
	}
	browserSource := string(browserDockerfile)
	required := []string{
		"mcr.microsoft.com/playwright:v1.61.1-noble@sha256:5b8f294a",
		"useradd --uid 10001 --gid 10001",
		"USER 10001:10001",
		"NO_PROXY=",
		"OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE=/browser-control/channel-credential",
		"OPENLINKER_BROWSER_PROFILE_DIR=/browser-tmp/profiles/active",
		"OPENLINKER_BROWSER_PROFILE_STORE=/browser-state/encrypted-profiles",
		"OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE=/browser-key/profile-root-key",
		`ENTRYPOINT ["/usr/local/bin/openlinker-browser-runtime"]`,
	}
	for _, value := range required {
		if !strings.Contains(browserSource, value) {
			t.Errorf("Dockerfile.browser is missing %q", value)
		}
	}
	forbidden := []string{
		"EXPOSE ",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"CODEX_API_KEY",
		"OPENLINKER_AGENT_TOKEN",
		"docker.sock",
		"/Users/",
		"/home/pwuser/.config",
		"OPENLINKER_BROWSER_PROFILE_DIR=/browser-state/",
	}
	for _, value := range forbidden {
		if strings.Contains(browserSource, value) {
			t.Errorf("Dockerfile.browser contains forbidden surface %q", value)
		}
	}

	providerDockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.providers"))
	if err != nil {
		t.Fatal(err)
	}
	providerSource := string(providerDockerfile)
	for _, value := range []string{
		"COPY browser-engine",
		"mcr.microsoft.com/playwright",
		"playwright-core",
	} {
		if strings.Contains(providerSource, value) {
			t.Errorf("Dockerfile.providers unexpectedly contains Browser engine dependency %q", value)
		}
	}

	for _, name := range []string{
		"deploy/compose.codex.browser.yml",
		"deploy/compose.claude.browser.yml",
	} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, required := range []string{
			"openlinker-browser-runtime:",
			"condition: service_healthy",
			"OPENLINKER_BROWSER_PROFILE_STORE: /browser-state/encrypted-profiles",
			"OPENLINKER_BROWSER_PROFILE_WORK_ROOT: /browser-tmp/profiles",
			"OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE: /browser-key/profile-root-key",
			"-profile-key:/browser-key",
			"- agent-internal",
		} {
			if !strings.Contains(source, required) {
				t.Errorf("%s is missing %q", name, required)
			}
		}
		for _, forbidden := range []string{
			"CODEX_API_KEY",
			"ANTHROPIC_API_KEY",
			"OPENLINKER_AGENT_TOKEN",
			"egress-public",
			"ports:",
			"docker.sock",
			"/browser-state/profile-root-key",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s contains forbidden Browser boundary %q", name, forbidden)
			}
		}
	}
}
