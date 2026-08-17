//go:build !windows

package browserruntime

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const maxOpsObserverRequestsPerLease = 4096

type OpsObserverServerOptions struct {
	SocketPath        string
	SocketMode        os.FileMode
	ChannelCredential string
	Observer          OpsObserverEngine
	Now               func() time.Time
}

type OpsObserverServer struct {
	options     OpsObserverServerOptions
	mu          sync.Mutex
	listener    *net.UnixListener
	socketInfo  os.FileInfo
	closed      bool
	active      *opsObserverLease
	connections map[*net.UnixConn]struct{}
	wg          sync.WaitGroup
}

type opsObserverLease struct {
	id        string
	runID     string
	expiresAt time.Time
	sequence  uint64
	seen      map[string]struct{}
}

func NewOpsObserverServer(options OpsObserverServerOptions) (*OpsObserverServer, error) {
	options.SocketPath = filepath.Clean(options.SocketPath)
	if !filepath.IsAbs(options.SocketPath) {
		return nil, errors.New("Ops Observer socket path must be absolute")
	}
	if len(options.ChannelCredential) < 32 || len(options.ChannelCredential) > 512 {
		return nil, errors.New("Ops Observer channel credential length is invalid")
	}
	if options.Observer == nil {
		return nil, errors.New("Ops Observer engine is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.SocketMode == 0 {
		options.SocketMode = 0o600
	}
	if options.SocketMode.Perm()&0o077 != 0 {
		return nil, errors.New("Ops Observer socket must be owner-only")
	}
	return &OpsObserverServer{
		options:     options,
		connections: make(map[*net.UnixConn]struct{}),
	}, nil
}

func (server *OpsObserverServer) Serve(ctx context.Context) error {
	if err := server.open(); err != nil {
		return err
	}
	defer server.Close()
	server.mu.Lock()
	listener := server.listener
	server.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				server.wg.Wait()
				return nil
			}
			return fmt.Errorf("accept Ops Observer Unix connection: %w", err)
		}
		server.mu.Lock()
		if server.closed {
			server.mu.Unlock()
			_ = connection.Close()
			continue
		}
		server.connections[connection] = struct{}{}
		server.wg.Add(1)
		server.mu.Unlock()
		go func() {
			defer server.wg.Done()
			defer server.removeConnection(connection)
			server.handleConnection(ctx, connection)
		}()
	}
}

func (server *OpsObserverServer) Close() error {
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return nil
	}
	server.closed = true
	listener := server.listener
	socketInfo := server.socketInfo
	server.listener = nil
	server.socketInfo = nil
	connections := make([]*net.UnixConn, 0, len(server.connections))
	for connection := range server.connections {
		connections = append(connections, connection)
	}
	server.mu.Unlock()
	var result error
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, err)
		}
	}
	for _, connection := range connections {
		result = errors.Join(result, connection.Close())
	}
	result = errors.Join(result, removeOwnedSocket(server.options.SocketPath, socketInfo))
	return result
}

func (server *OpsObserverServer) open() error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return errors.New("Ops Observer server is closed")
	}
	if server.listener != nil {
		return nil
	}
	if err := validateSocketParent(filepath.Dir(server.options.SocketPath)); err != nil {
		return err
	}
	if err := removeStaleSocket(server.options.SocketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{
		Name: server.options.SocketPath,
		Net:  "unix",
	})
	if err != nil {
		return fmt.Errorf("listen on Ops Observer Unix socket: %w", err)
	}
	if err := os.Chmod(server.options.SocketPath, server.options.SocketMode.Perm()); err != nil {
		_ = listener.Close()
		_ = os.Remove(server.options.SocketPath)
		return fmt.Errorf("protect Ops Observer Unix socket: %w", err)
	}
	info, err := os.Lstat(server.options.SocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		_ = listener.Close()
		_ = os.Remove(server.options.SocketPath)
		return errors.New("cannot verify Ops Observer Unix socket identity")
	}
	server.listener = listener
	server.socketInfo = info
	return nil
}

func (server *OpsObserverServer) removeConnection(connection *net.UnixConn) {
	_ = connection.Close()
	server.mu.Lock()
	delete(server.connections, connection)
	server.mu.Unlock()
}

func (server *OpsObserverServer) handleConnection(parent context.Context, connection *net.UnixConn) {
	reader := bufio.NewReaderSize(connection, browserprotocol.MaxOpsObserverRequestBytes)
	var admittedLeaseID string
	defer func() {
		if admittedLeaseID != "" {
			server.releaseLease(admittedLeaseID)
		}
	}()
	for {
		_ = connection.SetReadDeadline(server.options.Now().UTC().Add(browserprotocol.MaxOpsObserverDeadline))
		raw, err := readBoundedLine(reader, browserprotocol.MaxOpsObserverRequestBytes)
		if err != nil {
			return
		}
		now := server.options.Now().UTC()
		request, decodeErr := browserprotocol.DecodeOpsObserverRequest(raw)
		if decodeErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(
				"",
				browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverProtocolError,
					"Ops Observer request is invalid",
				),
			))
			return
		}
		if subtle.ConstantTimeCompare(
			[]byte(request.ChannelCredential),
			[]byte(server.options.ChannelCredential),
		) != 1 {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(
				request.RequestID,
				browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverUnauthorized,
					"Ops Observer request is unauthorized",
				),
			))
			return
		}
		if observerErr := request.Validate(now); observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			return
		}
		if admittedLeaseID == "" {
			if request.LeaseExpiresAt.Before(now.Add(
				browserprotocol.MinOpsObserverTTL - browserprotocol.MaxOpsObserverDeadline,
			)) {
				server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(
					request.RequestID,
					browserprotocol.NewOpsObserverError(
						browserprotocol.OpsObserverProtocolError,
						"Ops Observer initial TTL is too short",
					),
				))
				return
			}
			if observerErr := server.admitLease(request); observerErr != nil {
				server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
				return
			}
			admittedLeaseID = request.ObserverLeaseID
		} else if observerErr := server.validateLeaseRequest(request); observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			return
		}
		if observerErr := server.admitRequest(request.ObserverLeaseID, request.RequestID); observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			return
		}
		requestContext, cancel := context.WithDeadline(parent, request.Deadline)
		observation, busy, observerErr := server.options.Observer.ObserveOps(
			requestContext,
			request.RunID,
			request.Operation,
		)
		cancel()
		if observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			if observerErr.Code == browserprotocol.OpsObserverRunNotActive {
				return
			}
			continue
		}
		if busy {
			server.writeResponse(connection, browserprotocol.OpsObserverBusyResponse(request.RequestID))
			continue
		}
		sequence, sequenceErr := server.nextSequence(request.ObserverLeaseID)
		if sequenceErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, sequenceErr))
			return
		}
		observation.FrameSequence = sequence
		observation.CapturedAt = server.options.Now().UTC()
		if validationErr := observation.Validate(request.Operation); validationErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, validationErr))
			continue
		}
		server.writeResponse(connection, browserprotocol.OpsObserverSuccessResponse(request.RequestID, observation))
	}
}

func (server *OpsObserverServer) admitLease(request browserprotocol.OpsObserverRequest) *browserprotocol.OpsObserverError {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverDisabled, "Ops Observer is unavailable")
	}
	if server.active != nil {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverAlreadyActive, "another Ops Observer is active")
	}
	server.active = &opsObserverLease{
		id:        request.ObserverLeaseID,
		runID:     request.RunID,
		expiresAt: request.LeaseExpiresAt.UTC(),
		seen:      make(map[string]struct{}, maxOpsObserverRequestsPerLease),
	}
	return nil
}

func (server *OpsObserverServer) validateLeaseRequest(request browserprotocol.OpsObserverRequest) *browserprotocol.OpsObserverError {
	server.mu.Lock()
	defer server.mu.Unlock()
	lease := server.active
	if lease == nil || lease.id != request.ObserverLeaseID || lease.runID != request.RunID ||
		!lease.expiresAt.Equal(request.LeaseExpiresAt.UTC()) ||
		!server.options.Now().UTC().Before(lease.expiresAt) {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Ops Observer lease is stale")
	}
	return nil
}

func (server *OpsObserverServer) admitRequest(leaseID, requestID string) *browserprotocol.OpsObserverError {
	server.mu.Lock()
	defer server.mu.Unlock()
	lease := server.active
	if lease == nil || lease.id != leaseID {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Ops Observer lease is stale")
	}
	if _, exists := lease.seen[requestID]; exists {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Ops Observer request was replayed")
	}
	if len(lease.seen) >= maxOpsObserverRequestsPerLease {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Ops Observer request limit reached")
	}
	lease.seen[requestID] = struct{}{}
	return nil
}

func (server *OpsObserverServer) nextSequence(leaseID string) (uint64, *browserprotocol.OpsObserverError) {
	server.mu.Lock()
	defer server.mu.Unlock()
	lease := server.active
	if lease == nil || lease.id != leaseID {
		return 0, browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Ops Observer lease is stale")
	}
	lease.sequence++
	return lease.sequence, nil
}

func (server *OpsObserverServer) releaseLease(leaseID string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.active != nil && server.active.id == leaseID {
		server.active = nil
	}
}

func (server *OpsObserverServer) writeResponse(connection *net.UnixConn, response browserprotocol.OpsObserverResponse) {
	_ = connection.SetWriteDeadline(server.options.Now().UTC().Add(browserprotocol.MaxOpsObserverDeadline))
	raw, err := json.Marshal(response)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	_, _ = connection.Write(raw)
}
