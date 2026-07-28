package browserprotocol

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestBrowserClientPackagesCannotOwnProviderHTTPProtocol(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve browser protocol package path")
	}
	pkgDir := filepath.Dir(filepath.Dir(currentFile))
	violations, err := browserOwnershipViolations(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}

func TestBrowserOwnershipBoundaryDetectsAgentexecProviderBrowserMarkers(t *testing.T) {
	t.Parallel()
	pkgDir := t.TempDir()
	agentexecDir := filepath.Join(pkgDir, "agentexec")
	if err := os.MkdirAll(agentexecDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agentexecDir, "provider_browser_wire.go")
	if err := os.WriteFile(
		path,
		[]byte("package agentexec\nconst forbidden = \"computer_tool_call\"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	violations, err := browserOwnershipViolations(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 ||
		!strings.Contains(strings.Join(violations, "\n"), "computer_tool_call") {
		t.Fatalf("agentexec forbidden marker was not detected: %#v", violations)
	}
}

func TestBrowserOwnershipBoundaryRejectsRemovedDirectoryEvenWhenEmpty(t *testing.T) {
	t.Parallel()
	pkgDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(pkgDir, "providerbroker"), 0o700); err != nil {
		t.Fatal(err)
	}
	violations, err := browserOwnershipViolations(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 ||
		!strings.Contains(strings.Join(violations, "\n"), "pkg/providerbroker") {
		t.Fatalf("empty removed directory was not detected: %#v", violations)
	}
}

func browserOwnershipViolations(pkgDir string) ([]string, error) {
	var violations []string
	for _, removed := range []string{
		"browserprovider",
		"clientbrowser",
		"providerbroker",
	} {
		_, err := os.Stat(filepath.Join(pkgDir, removed))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		violations = append(
			violations,
			fmt.Sprintf("removed Browser package directory still exists: pkg/%s", removed),
		)
	}
	for _, packageName := range []string{"browserclient", "browserplugin", "agentexec"} {
		dir := filepath.Join(pkgDir, packageName)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			for _, marker := range []string{
				"api.openai.com",
				"api.anthropic.com",
				`"/v1/responses"`,
				`"computer_use_preview"`,
				`"computer_tool_call"`,
				`"computer_use"`,
			} {
				if strings.Contains(string(raw), marker) {
					violations = append(
						violations,
						fmt.Sprintf(
							"%s contains forbidden Provider Browser wire marker %q",
							path,
							marker,
						),
					)
				}
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, raw, parser.ImportsOnly)
			if err != nil {
				return nil, err
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return nil, err
				}
				if importPath == "net/http" ||
					strings.Contains(importPath, "/pkg/providerbroker") ||
					strings.Contains(importPath, "/pkg/browserprovider") ||
					strings.Contains(importPath, "/pkg/clientbrowser") {
					violations = append(
						violations,
						fmt.Sprintf(
							"%s imports forbidden Provider Browser dependency %q",
							path,
							importPath,
						),
					)
				}
			}
		}
	}
	return violations, nil
}
