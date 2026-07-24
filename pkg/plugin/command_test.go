package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

func TestPluginCommandIncludesBrowserOnlyServer(t *testing.T) {
	var output bytes.Buffer
	command := New(shared.IO{
		Stdin:  strings.NewReader(""),
		Stdout: &output,
		Stderr: &output,
		Getenv: func(string) string { return "" },
	}, nil, nil)
	found, _, err := command.Find([]string{"browser-serve"})
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Name() != "browser-serve" {
		t.Fatalf("browser command = %#v", found)
	}
	command.SetArgs([]string{"browser-serve"})
	if err := command.ExecuteContext(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "--host codex or --host claude") {
		t.Fatalf("browser-serve error = %v", err)
	}
}
