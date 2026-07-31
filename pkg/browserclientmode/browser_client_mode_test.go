package browserclientmode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectDefaultsEmptyRequestToMCPWithoutHostCommand(t *testing.T) {
	hostCalls := 0
	selection, err := Select(Options{
		Provider:   "codex",
		Requested:  " \t ",
		PluginPath: "/must/not/be/used",
		RunHostCommand: func(args ...string) ([]byte, error) {
			hostCalls++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Requested != "mcp" ||
		selection.Selected != "mcp" ||
		selection.PluginPath != "" ||
		selection.FallbackReason != "" ||
		hostCalls != 0 {
		t.Fatalf("empty selection = %#v, host calls = %d", selection, hostCalls)
	}
}

func TestCodexPluginListAcceptsCompatibleHostFieldsAndAvailableCatalog(
	t *testing.T,
) {
	root := t.TempDir()
	document := validCodexPluginList(root)
	document["hostVersion"] = "future-compatible"
	document["available"] = []any{
		map[string]any{
			"pluginId": "uninstalled-catalog-entry@example",
			"metadata": map[string]any{"new": true},
		},
	}
	installed := document["installed"].([]any)[0].(map[string]any)
	installed["newInstalledField"] = map[string]any{"future": true}
	installed["source"].(map[string]any)["newSourceField"] = "future"

	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCodexPluginList(raw, root); err != nil {
		t.Fatalf("compatible Codex Plugin list was rejected: %v", err)
	}
}

func TestCodexPluginListRejectsInvalidActivationEvidence(t *testing.T) {
	root := t.TempDir()
	tests := map[string]func(map[string]any) []byte{
		"missing": func(document map[string]any) []byte {
			document["installed"] = []any{}
			return mustJSON(t, document)
		},
		"disabled": func(document map[string]any) []byte {
			document["installed"].([]any)[0].(map[string]any)["enabled"] = false
			return mustJSON(t, document)
		},
		"duplicated": func(document map[string]any) []byte {
			installed := document["installed"].([]any)
			document["installed"] = append(installed, installed[0])
			return mustJSON(t, document)
		},
		"wrong-source": func(document map[string]any) []byte {
			document["installed"].([]any)[0].(map[string]any)["source"] =
				map[string]any{
					"source": "remote",
					"path":   filepath.Join(root, "plugins", "openlinker"),
				}
			return mustJSON(t, document)
		},
		"wrong-path": func(document map[string]any) []byte {
			document["installed"].([]any)[0].(map[string]any)["source"] =
				map[string]any{
					"source": "local",
					"path":   filepath.Join(root, "plugins", "other"),
				}
			return mustJSON(t, document)
		},
		"malformed": func(map[string]any) []byte {
			return []byte(`{"installed":`)
		},
		"trailing-document": func(document map[string]any) []byte {
			return append(mustJSON(t, document), []byte(` {}`)...)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateCodexPluginList(
				mutate(validCodexPluginList(root)),
				root,
			); err == nil {
				t.Fatal("invalid Codex Plugin activation evidence was accepted")
			}
		})
	}
}

func validCodexPluginList(root string) map[string]any {
	return map[string]any{
		"installed": []any{map[string]any{
			"pluginId":        "openlinker@openlinker-agent-runtime",
			"name":            "openlinker",
			"marketplaceName": "openlinker-agent-runtime",
			"version":         "0.1.0+agent-runtime.codex",
			"installed":       true,
			"enabled":         true,
			"source": map[string]any{
				"source": "local",
				"path":   filepath.Join(root, "plugins", "openlinker"),
			},
			"marketplaceSource": map[string]any{
				"sourceType": "local",
				"source":     root,
			},
			"installPolicy": "AVAILABLE",
			"authPolicy":    "ON_INSTALL",
		}},
		"available": []any{},
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

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
