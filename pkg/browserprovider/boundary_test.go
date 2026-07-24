package browserprovider_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	anthropicadapter "github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider/anthropic"
	openaiadapter "github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider/openai"
)

func TestProviderNativeFoundationCannotAffectOrdinaryAgentPath(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relative := range []string{"pkg/agent", "pkg/agentexec"} {
		err := filepath.WalkDir(
			filepath.Join(root, relative),
			func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || filepath.Ext(path) != ".go" {
					return nil
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				source := string(raw)
				for _, forbidden := range []string{
					"/pkg/browserprovider",
					"/pkg/providerbroker",
					"native_browser",
				} {
					if strings.Contains(source, forbidden) {
						t.Errorf(
							"ordinary Agent source %s unexpectedly references %q",
							path, forbidden,
						)
					}
				}
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestProviderAdaptersDoNotImportAgentRuntimeOrPersistentEvents(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relative := range []string{
		"pkg/browserprovider",
		"pkg/providerbroker",
	} {
		err := filepath.WalkDir(
			filepath.Join(root, relative),
			func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || filepath.Ext(path) != ".go" ||
					strings.HasSuffix(path, "_test.go") {
					return nil
				}
				file, parseErr := parser.ParseFile(
					token.NewFileSet(),
					path,
					nil,
					parser.ImportsOnly,
				)
				if parseErr != nil {
					return parseErr
				}
				for _, imported := range file.Imports {
					value := strings.Trim(imported.Path.Value, `"`)
					for _, forbidden := range []string{
						"/pkg/agent",
						"/pkg/agentexec",
						"/runtime",
					} {
						if strings.Contains(value, forbidden) {
							t.Errorf("%s imports forbidden boundary %q", path, value)
						}
					}
				}
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestProviderAdapterConfigurationHasNoRealCredentialField(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]any{
		"openai":    openaiadapter.Config{},
		"anthropic": anthropicadapter.Config{},
	} {
		typ := reflect.TypeOf(value)
		for index := range typ.NumField() {
			field := typ.Field(index).Name
			lower := strings.ToLower(field)
			if strings.Contains(lower, "apikey") ||
				strings.Contains(lower, "secret") ||
				lower == "key" {
				t.Errorf("%s adapter exposes real credential field %q", name, field)
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve boundary test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
