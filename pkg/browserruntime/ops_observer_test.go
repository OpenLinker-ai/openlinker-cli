//go:build !windows

package browserruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

type opsObserverTestEngine struct {
	busy bool
}

func (engine *opsObserverTestEngine) ObserveOps(
	_ context.Context,
	_ string,
	operation browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverObservation, bool, *browserprotocol.OpsObserverError) {
	if engine.busy {
		return browserprotocol.OpsObserverObservation{}, true, nil
	}
	observation := browserprotocol.OpsObserverObservation{
		RunID:                "11111111-1111-4111-8111-111111111111",
		Controller:           browserprotocol.ControllerAgent,
		SessionEpoch:         1,
		ControlEpoch:         3,
		BrowserSessionSHA256: strings.Repeat("a", 64),
		AttachmentSHA256:     strings.Repeat("b", 64),
		SelectedBackend:      BackendOfficialChrome,
		ProfileGeneration:    2,
		PageURL:              "about:blank",
		PageTitle:            "Blank",
	}
	if operation == browserprotocol.OpsObserverFrameOperation {
		observation.Frame = &browserprotocol.ViewerFrame{
			MIMEType: "image/jpeg",
			Data:     []byte{0xff, 0xd8, 0xff, 0xd9},
			Width:    1280,
			Height:   720,
		}
	}
	return observation, false, nil
}

func TestOpsObserverServerEnforcesOneConnectionBoundLease(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "ol-ops-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(root, "credential")
	credential := strings.Repeat("c", 64)
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "observer.sock")
	observer := &opsObserverTestEngine{}
	server, err := NewOpsObserverServer(OpsObserverServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: credential,
		Observer:          observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if err := server.open(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- server.Serve(ctx) }()

	first := newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
	response, observerErr := first.Observe(ctx, browserprotocol.OpsObserverFrameOperation)
	if observerErr != nil || response.Status != "ok" ||
		response.Observation.FrameSequence != 1 {
		t.Fatalf("first observation = %#v, %v", response, observerErr)
	}

	second := newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
	_, observerErr = second.Observe(ctx, browserprotocol.OpsObserverStatusOperation)
	if observerErr == nil || observerErr.Code != browserprotocol.OpsObserverAlreadyActive {
		t.Fatalf("second observer error = %v", observerErr)
	}
	_ = second.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	var third *browserclient.OpsObserverStream
	releaseDeadline := time.Now().Add(2 * time.Second)
	for {
		third = newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
		response, observerErr = third.Observe(ctx, browserprotocol.OpsObserverStatusOperation)
		if observerErr == nil {
			break
		}
		_ = third.Close()
		if observerErr.Code != browserprotocol.OpsObserverAlreadyActive ||
			time.Now().After(releaseDeadline) {
			t.Fatalf("replacement observation = %#v, %v", response, observerErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if response.Status != "ok" || response.Observation.Frame != nil {
		t.Fatalf("replacement observation = %#v, %v", response, observerErr)
	}
	_ = third.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOpsObserverServerReturnsBusyWithoutConsumingFrameSequence(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "ol-ops-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(root, "credential")
	credential := strings.Repeat("d", 64)
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "observer.sock")
	observer := &opsObserverTestEngine{busy: true}
	server, err := NewOpsObserverServer(OpsObserverServerOptions{
		SocketPath: socketPath, ChannelCredential: credential, Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if err := server.open(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- server.Serve(ctx) }()
	stream := newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
	response, observerErr := stream.Observe(ctx, browserprotocol.OpsObserverFrameOperation)
	if observerErr != nil || response.Status != "busy" {
		t.Fatalf("busy observation = %#v, %v", response, observerErr)
	}
	observer.busy = false
	response, observerErr = stream.Observe(ctx, browserprotocol.OpsObserverStatusOperation)
	if observerErr != nil || response.Observation.FrameSequence != 1 {
		t.Fatalf("post-busy observation = %#v, %v", response, observerErr)
	}
	_ = stream.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func newOpsObserverTestStream(
	t *testing.T,
	ctx context.Context,
	socketPath string,
	credentialFile string,
) *browserclient.OpsObserverStream {
	t.Helper()
	stream, err := browserclient.NewOpsObserverStream(ctx, browserclient.OpsObserverConfig{
		SocketPath: socketPath, CredentialFile: credentialFile,
		RunID: "11111111-1111-4111-8111-111111111111", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}
