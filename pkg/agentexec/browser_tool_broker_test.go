//go:build !windows

package agentexec

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserruntime"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type brokerTestEngine struct {
	actions chan browserprotocol.ActionKind
}

func (engine brokerTestEngine) Execute(
	_ context.Context,
	_ browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	engine.actions <- action.Kind
	return browserprotocol.Observation{
		PageStateID: "page-state-1",
		Screenshot: &browserprotocol.Screenshot{
			MIMEType: "image/jpeg",
			Data:     []byte("jpeg"),
		},
		Origin: "https://example.com",
		Title:  "Example",
	}, nil
}

type brokerMCPProvider struct {
	observed bool
}

func (provider *brokerMCPProvider) Run(
	_ context.Context,
	run RunContext,
) (openlinker.RuntimeResult, error) {
	connection, err := net.DialTimeout("unix", run.Browser.ToolSocket, time.Second)
	if err != nil {
		return openlinker.RuntimeResult{}, err
	}
	defer connection.Close()
	for _, request := range []map[string]any{
		{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params":  map[string]any{},
		},
		{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "browser_session",
				"arguments": map[string]any{"operation": "observe"},
			},
		},
	} {
		raw, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return openlinker.RuntimeResult{}, marshalErr
		}
		if _, err := connection.Write(append(raw, '\n')); err != nil {
			return openlinker.RuntimeResult{}, err
		}
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(connection)
	for scanner.Scan() {
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			return openlinker.RuntimeResult{}, err
		}
		if response["id"] != float64(2) {
			continue
		}
		raw := string(scanner.Bytes())
		if strings.Contains(raw, "page-state-1") &&
			strings.Contains(raw, "https://example.com") &&
			strings.Contains(raw, `"type":"image"`) {
			provider.observed = true
		}
		break
	}
	if err := scanner.Err(); err != nil {
		return openlinker.RuntimeResult{}, err
	}
	return openlinker.RuntimeResult{
		Status: "success",
		Output: map[string]any{"observed": provider.observed},
	}, nil
}

func TestBrowserToolBrokerKeepsAuthorityOutOfProviderProcess(t *testing.T) {
	root := shortBrowserTestRoot(t)
	controlRoot := filepath.Join(root, "control")
	leaseRoot := filepath.Join(controlRoot, "leases")
	for _, dir := range []string{controlRoot, leaseRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	credentialPath := filepath.Join(controlRoot, "channel")
	credential := strings.Repeat("a", 64)
	if err := os.WriteFile(credentialPath, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(controlRoot, "runtime.sock")
	actions := make(chan browserprotocol.ActionKind, 4)
	runtimeServer, err := browserruntime.NewServer(browserruntime.ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: credential,
		Lease: browserruntime.FileLease{
			Path: filepath.Join(leaseRoot, "active-lease.json"),
		},
		Engine: brokerTestEngine{actions: actions},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeContext, stopRuntime := context.WithCancel(context.Background())
	defer stopRuntime()
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- runtimeServer.Serve(runtimeContext) }()
	waitForUnixSocket(t, socketPath)

	base := &brokerMCPProvider{}
	config := browserProviderTestConfig(leaseRoot)
	config.BrowserSocket = socketPath
	config.BrowserCredentialFile = credentialPath
	config.BrowserBrokerRoot = filepath.Join(root, "broker")
	provider, err := newBrowserExecutionProvider(base, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Run(
		context.Background(),
		browserProviderTestRun("88888888-8888-4888-8888-888888888888"),
	); err != nil {
		t.Fatal(err)
	}
	if !base.observed {
		t.Fatal("MCP Browser observation did not cross trusted broker and Runtime UDS")
	}
	if first, second := <-actions, <-actions; first != browserprotocol.ActionScreenshot ||
		second != browserprotocol.ActionClose {
		t.Fatalf("Browser actions = [%s %s], want screenshot followed by trusted close", first, second)
	}
	stopRuntime()
	if err := <-runtimeDone; err != nil {
		t.Fatal(err)
	}
}

func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Unix socket %s was not created: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
