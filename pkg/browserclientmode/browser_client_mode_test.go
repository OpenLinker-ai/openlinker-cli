package browserclientmode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexManifestRequiresIngestionInterfaceMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.json")
	valid := `{
		"name":"openlinker",
		"skills":"./skills/",
		"mcpServers":"./.mcp.json",
		"interface":{
			"displayName":"OpenLinker Isolated Browser",
			"shortDescription":"Use the isolated Browser.",
			"longDescription":"Use the Runtime-authorized isolated Browser.",
			"developerName":"OpenLinker",
			"category":"Productivity",
			"capabilities":["Browser automation"],
			"defaultPrompt":["Use the isolated Browser."]
		}
	}`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePluginManifest(path, "codex"); err != nil {
		t.Fatalf("valid Codex manifest was rejected: %v", err)
	}

	invalid := `{
		"name":"openlinker",
		"skills":"./skills/",
		"mcpServers":"./.mcp.json"
	}`
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePluginManifest(path, "codex"); err == nil {
		t.Fatal("Codex manifest without interface metadata was accepted")
	}
}
