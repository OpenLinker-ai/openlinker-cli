//go:build !windows

package browserruntime

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func TestFileLeaseLoadsCurrentOwnerOnlyAttachment(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "active-lease.json")
	identity := validRuntimeRequest().Identity
	raw, err := json.Marshal(browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  now.Add(time.Minute),
		Identity:   identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	validator := FileLease{Path: path, Now: func() time.Time { return now }}
	if failure := validator.Validate(identity); failure != nil {
		t.Fatalf("valid active lease failed: %v", failure)
	}
	stale := identity
	stale.ControlEpoch--
	if failure := validator.Validate(stale); failure == nil ||
		failure.Code != browserprotocol.ErrorStaleControlEpoch {
		t.Fatalf("stale epoch failure = %#v", failure)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if failure := validator.Validate(identity); failure == nil ||
		failure.Code != browserprotocol.ErrorRuntimeUnavailable {
		t.Fatalf("insecure lease failure = %#v", failure)
	}
}

type fakeEngine struct {
	calls atomic.Int64
	run   func(context.Context) (browserprotocol.Observation, *browserprotocol.Failure)
}

func (engine *fakeEngine) Execute(
	ctx context.Context,
	_ browserprotocol.Identity,
	_ browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	engine.calls.Add(1)
	if engine.run != nil {
		return engine.run(ctx)
	}
	return browserprotocol.Observation{
		PageStateID: "state-1",
		AXTree:      json.RawMessage(`{"role":"document"}`),
		Origin:      "https://example.com",
		Title:       "Example",
	}, nil
}

func TestServerUsesProtectedUnixSocketAndExecutesAuthorizedAction(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode = %v, want Unix socket", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions = %o, want 0600", info.Mode().Perm())
	}
	server.mu.Lock()
	network := server.listener.Addr().Network()
	server.mu.Unlock()
	if network != "unix" {
		t.Fatalf("listener network = %q, want unix", network)
	}

	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Status != "ok" || response.Observation == nil || response.Observation.PageStateID != "state-1" {
		t.Fatalf("response = %#v", response)
	}
	if engine.calls.Load() != 1 {
		t.Fatalf("engine calls = %d, want 1", engine.calls.Load())
	}
}

func TestServerRejectsCredentialIdentityAndStaleEpochBeforeEngine(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	expected := validRuntimeRequest().Identity
	expected.ControlEpoch = 2
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{
		Lease: StaticLease{Identity: expected},
	})
	defer stopTestServer(t, server, cancel, done)

	cases := []struct {
		name string
		edit func(*browserprotocol.Request)
		code browserprotocol.ErrorCode
	}{
		{
			name: "credential",
			edit: func(request *browserprotocol.Request) {
				request.ChannelCredential = strings.Repeat("b", 64)
			},
			code: browserprotocol.ErrorUnauthorized,
		},
		{
			name: "cross run",
			edit: func(request *browserprotocol.Request) {
				request.Identity.RunID = "99999999-9999-4999-8999-999999999999"
			},
			code: browserprotocol.ErrorIdentityMismatch,
		},
		{
			name: "stale epoch",
			edit: func(request *browserprotocol.Request) {
				request.Identity.ControlEpoch = 1
			},
			code: browserprotocol.ErrorStaleControlEpoch,
		},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := validRuntimeRequest()
			test.edit(&request)
			response := sendRequest(t, socketPath, request)
			if response.Error == nil || response.Error.Code != test.code {
				t.Fatalf("response = %#v, want %s", response, test.code)
			}
		})
	}
	if engine.calls.Load() != 0 {
		t.Fatalf("engine calls = %d, want 0", engine.calls.Load())
	}
}

func TestServerRejectsUnknownFieldsAndOversizedRequests(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{MaxRequestBytes: 1024})
	defer stopTestServer(t, server, cancel, done)

	request := validRuntimeRequest()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"cdp_command":"Runtime.evaluate"}`)...)
	response := sendRaw(t, socketPath, raw)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorProtocolInvalid {
		t.Fatalf("unknown-field response = %#v", response)
	}

	large := append([]byte(`{"padding":"`), []byte(strings.Repeat("x", 2048))...)
	large = append(large, []byte(`"}`)...)
	response = sendRaw(t, socketPath, large)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorRequestTooLarge {
		t.Fatalf("oversized response = %#v", response)
	}
	if engine.calls.Load() != 0 {
		t.Fatalf("engine calls = %d, want 0", engine.calls.Load())
	}
}

func TestServerCancelsEngineAtRequestDeadline(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{
		run: func(ctx context.Context) (browserprotocol.Observation, *browserprotocol.Failure) {
			<-ctx.Done()
			return browserprotocol.Observation{}, nil
		},
	}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	request := validRuntimeRequest()
	request.Deadline = time.Now().UTC().Add(50 * time.Millisecond)
	response := sendRequest(t, socketPath, request)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorDeadlineExceeded {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerRejectsRequestReplayAndActionLimitWithoutReexecution(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{MaxRequests: 1})
	defer stopTestServer(t, server, cancel, done)

	request := validRuntimeRequest()
	if response := sendRequest(t, socketPath, request); response.Status != "ok" {
		t.Fatalf("first response = %#v", response)
	}
	response := sendRequest(t, socketPath, request)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorRequestReplayed {
		t.Fatalf("replay response = %#v", response)
	}
	request.RequestID = "88888888-8888-4888-8888-888888888888"
	response = sendRequest(t, socketPath, request)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorActionLimitExceeded {
		t.Fatalf("limit response = %#v", response)
	}
	if engine.calls.Load() != 1 {
		t.Fatalf("engine calls = %d, want 1", engine.calls.Load())
	}
}

func TestReplayGuardResetsForNewAttachmentIdentity(t *testing.T) {
	t.Parallel()
	server, err := NewServer(ServerOptions{
		SocketPath:        filepath.Join(shortTempDir(t), "browser.sock"),
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
		MaxRequests:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := validRuntimeRequest().Identity
	requestID := "77777777-7777-4777-8777-777777777777"
	if failure := server.reserveRequestID(identity, requestID); failure != nil {
		t.Fatal(failure)
	}
	if failure := server.reserveRequestID(identity, requestID); failure == nil ||
		failure.Code != browserprotocol.ErrorRequestReplayed {
		t.Fatalf("replay failure = %#v", failure)
	}
	identity.AttachmentID = "66666666-6666-4666-8666-666666666666"
	if failure := server.reserveRequestID(identity, requestID); failure != nil {
		t.Fatalf("new attachment failure = %v", failure)
	}
}

func TestServerReplacesOversizedEncodedResponseWithStableError(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{
		run: func(context.Context) (browserprotocol.Observation, *browserprotocol.Failure) {
			return browserprotocol.Observation{
				PageStateID: "state-1",
				Title:       strings.Repeat("x", 1500),
			}, nil
		},
	}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{MaxResponseBytes: 1024})
	defer stopTestServer(t, server, cancel, done)

	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorOutputTooLarge {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerReturnsStableNotReadyWithoutActiveLease(t *testing.T) {
	t.Parallel()
	server, socketPath, cancel, done := startTestServer(t, NotReadyEngine{}, ServerOptions{
		Lease: NoActiveLease{},
	})
	defer stopTestServer(t, server, cancel, done)

	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorRuntimeUnavailable {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerReplacesOnlyStaleUnixSockets(t *testing.T) {
	t.Parallel()
	dir := shortTempDir(t)
	path := filepath.Join(dir, "browser.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{
		SocketPath:        path,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Open(); err == nil {
		t.Fatal("Open() succeeded with a regular file at the socket path")
	}

	stalePath := filepath.Join(dir, "stale.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: stalePath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	staleDeadline := time.Now().Add(time.Second)
	for {
		connection, dialErr := net.DialTimeout("unix", stalePath, 10*time.Millisecond)
		if dialErr != nil {
			break
		}
		_ = connection.Close()
		if time.Now().After(staleDeadline) {
			t.Fatal("closed Unix listener continued accepting connections")
		}
		time.Sleep(time.Millisecond)
	}
	staleServer, err := NewServer(ServerOptions{
		SocketPath:        stalePath,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staleServer.Open(); err != nil {
		t.Fatalf("Open() stale socket error = %v", err)
	}
	if err := staleServer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerDoesNotReplaceActiveUnixSocket(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	second, err := NewServer(ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Open(); err == nil {
		t.Fatal("second Open() replaced an active Unix socket")
	}
	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Status != "ok" {
		t.Fatalf("original server response = %#v", response)
	}
}

func startTestServer(
	t *testing.T,
	engine Engine,
	overrides ServerOptions,
) (*Server, string, context.CancelFunc, <-chan error) {
	t.Helper()
	dir := shortTempDir(t)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(dir, "browser.sock")
	identity := validRuntimeRequest().Identity
	options := ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             StaticLease{Identity: identity},
		Engine:            engine,
	}
	if overrides.Lease != nil {
		options.Lease = overrides.Lease
	}
	if overrides.MaxRequestBytes > 0 {
		options.MaxRequestBytes = overrides.MaxRequestBytes
	}
	if overrides.MaxResponseBytes > 0 {
		options.MaxResponseBytes = overrides.MaxResponseBytes
	}
	if overrides.MaxRequests > 0 {
		options.MaxRequests = overrides.MaxRequests
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Open(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx)
	}()
	return server, socketPath, cancel, done
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "olbr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func stopTestServer(t *testing.T, server *Server, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("browser server did not stop")
	}
}

func sendRequest(t *testing.T, socketPath string, request browserprotocol.Request) browserprotocol.Response {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return sendRaw(t, socketPath, raw)
}

func sendRaw(t *testing.T, socketPath string, raw []byte) browserprotocol.Response {
	t.Helper()
	connection, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		_ = unixConnection.CloseWrite()
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var response browserprotocol.Response
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func validRuntimeRequest() browserprotocol.Request {
	return browserprotocol.Request{
		ContractID:        browserprotocol.ContractID,
		ChannelCredential: strings.Repeat("a", 64),
		RequestID:         "77777777-7777-4777-8777-777777777777",
		Deadline:          time.Now().UTC().Add(5 * time.Second),
		Identity: browserprotocol.Identity{
			RunID:            "11111111-1111-4111-8111-111111111111",
			AgentID:          "22222222-2222-4222-8222-222222222222",
			PrincipalScopeID: "scope_333333333333",
			BrowserSessionID: "44444444-4444-4444-8444-444444444444",
			SessionEpoch:     1,
			AttachmentID:     "55555555-5555-4555-8555-555555555555",
			ControlEpoch:     1,
		},
		Action: browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	}
}
