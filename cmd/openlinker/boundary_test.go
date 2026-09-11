package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformClientHasNoLocalAgentCommands(t *testing.T) {
	for _, args := range [][]string{{"agent", "serve", "--provider", "codex"}, {"agent", "configure"}, {"plugin", "serve", "--host", "claude"}, {"plugin", "browser-serve", "--host", "codex"}, {"plugin", "browser-proxy"}, {"plugin", "delegation-proxy"}} {
		var out, errOut bytes.Buffer
		if runCLI(args, strings.NewReader(""), &out, &errOut, func(string) string { return "" }) == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "unknown command") {
			t.Fatalf("removed command %v: stdout=%q stderr=%q", args, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if runCLI([]string{"context"}, nil, &out, &errOut, func(string) string { return "" }) != 0 {
		t.Fatal(errOut.String())
	}
	var context struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(out.Bytes(), &context); err != nil {
		t.Fatal(err)
	}
	if len(context.Capabilities) == 0 {
		t.Fatal("empty caller surface")
	}
	for _, capability := range context.Capabilities {
		if strings.HasPrefix(capability, "agent.") || strings.HasPrefix(capability, "plugin.") {
			t.Fatalf("local capability remains: %s", capability)
		}
	}
}

func TestPlatformClientDependencyBoundary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			for _, args := range [][]string{{"mod", "edit", "-json"}, {"mod", "graph"}, {"list", "-m", "-mod=readonly", "all"}, {"list", "-deps", "-test", "-mod=readonly", "-f", "{{.ImportPath}}", "./..."}} {
				command := exec.Command("go", args...)
				command.Dir = root
				command.Env = append(os.Environ(), "GOWORK=off", "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
				output, err := command.CombinedOutput()
				if err != nil || len(bytes.TrimSpace(output)) == 0 {
					t.Fatalf("go %v: %v\n%s", args, err, output)
				}
				for _, forbidden := range []string{"github.com/OpenLinker-ai/openlinker-plugin", "github.com/OpenLinker-ai/openlinker-agent-node"} {
					if bytes.Contains(output, []byte(forbidden)) {
						t.Fatalf("go %v includes forbidden local execution dependency %s", args, forbidden)
					}
				}
				if args[0] == "mod" && args[1] == "edit" {
					var module struct{ Replace []json.RawMessage }
					if err := json.Unmarshal(output, &module); err != nil {
						t.Fatal(err)
					}
					if len(module.Replace) != 0 {
						t.Fatal("release module must not contain replacements")
					}
				}
			}
		})
	}
}
