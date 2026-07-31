package browserclientmode

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestProviderImagePinsExactAgentRuntimePluginArtifacts(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/agent-runtime-plugin.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		SchemaVersion int    `json:"schema_version"`
		Repository    string `json:"repository"`
		Release       string `json:"release"`
		Artifacts     map[string]struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.SchemaVersion != 1 ||
		lock.Repository != "OpenLinker-ai/openlinker-plugin" ||
		!regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(lock.Release) {
		t.Fatalf("Plugin release lock = %#v", lock)
	}
	for _, host := range []string{"codex", "claude"} {
		artifact, ok := lock.Artifacts[host]
		if !ok ||
			artifact.Name != "openlinker-agent-runtime-"+host+"-plugin-"+
				lock.Release+".tar.gz" ||
			!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(artifact.SHA256) {
			t.Fatalf("%s Plugin artifact lock = %#v", host, artifact)
		}
	}
	dockerfile, err := os.ReadFile("../../Dockerfile.providers")
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	for _, required := range []string{
		"deploy/agent-runtime-plugin.lock.json",
		"sha256sum --check --strict",
		"/opt/openlinker/agent-runtime-plugin/codex",
		"/opt/openlinker/agent-runtime-plugin/claude",
		`stat -c '%U:%G %a'`,
		`root:root 555`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Provider Dockerfile omitted %q", required)
		}
	}
}
