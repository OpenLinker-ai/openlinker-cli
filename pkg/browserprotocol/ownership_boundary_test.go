package browserprotocol

import (
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
	for _, removed := range []string{"browserprovider", "providerbroker"} {
		entries, err := os.ReadDir(filepath.Join(pkgDir, removed))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			t.Fatalf("removed Provider-owned Browser source still exists: pkg/%s/%s", removed, entry.Name())
		}
	}
	for _, packageName := range []string{"browserclient", "browserplugin"} {
		dir := filepath.Join(pkgDir, packageName)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{
				"api.openai.com",
				"api.anthropic.com",
				`"/v1/responses"`,
				`"computer_use_preview"`,
			} {
				if strings.Contains(string(raw), marker) {
					t.Errorf("%s contains forbidden Provider Browser wire marker %q", path, marker)
				}
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, raw, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if importPath == "net/http" ||
					strings.Contains(importPath, "/pkg/providerbroker") ||
					strings.Contains(importPath, "/pkg/browserprovider") {
					t.Errorf("%s imports forbidden Provider Browser dependency %q", path, importPath)
				}
			}
		}
	}
}
