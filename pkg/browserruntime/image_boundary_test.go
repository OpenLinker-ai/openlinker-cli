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
		"USER pwuser",
		"NO_PROXY=",
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
}
