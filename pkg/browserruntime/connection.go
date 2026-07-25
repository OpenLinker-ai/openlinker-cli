//go:build !windows

package browserruntime

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func (server *Server) handleConnection(parent context.Context, connection *net.UnixConn) {
	now := server.options.Now().UTC()
	_ = connection.SetDeadline(now.Add(server.options.IOTimeout))

	request, failure := server.decodeRequest(connection)
	if failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	if subtle.ConstantTimeCompare(
		[]byte(request.ChannelCredential),
		[]byte(server.options.ChannelCredential),
	) != 1 {
		server.writeResponse(connection, browserprotocol.ErrorResponse(
			request.RequestID,
			browserprotocol.NewFailure(browserprotocol.ErrorUnauthorized, "browser channel credential is invalid", false),
		))
		return
	}
	if failure := request.Validate(now); failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	if failure := server.options.Lease.Validate(request.Identity); failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	if failure := server.reserveRequestID(request.Identity, request.RequestID); failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}

	_ = connection.SetDeadline(request.Deadline)
	actionContext, cancel := context.WithDeadline(parent, request.Deadline)
	defer cancel()
	observation, failure := server.options.Engine.Execute(actionContext, request.Identity, request.Action)
	if failure == nil && actionContext.Err() != nil {
		failure = browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser action deadline elapsed",
			true,
		)
	}
	_ = connection.SetWriteDeadline(server.options.Now().UTC().Add(server.options.IOTimeout))
	if failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	if failure := observation.Validate(); failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	server.writeResponse(connection, browserprotocol.SuccessResponse(request.RequestID, observation))
}

func (server *Server) reserveRequestID(
	identity browserprotocol.Identity,
	requestID string,
) *browserprotocol.Failure {
	server.seenMu.Lock()
	defer server.seenMu.Unlock()
	scope := identity.RunID + "|" + identity.AttachmentID + "|" +
		fmt.Sprintf("%d|%d", identity.SessionEpoch, identity.ControlEpoch)
	if scope != server.seenScope {
		clear(server.seen)
		server.seenScope = scope
	}
	if _, found := server.seen[requestID]; found {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRequestReplayed,
			"browser request_id has already been used",
			false,
		)
	}
	if len(server.seen) >= server.options.MaxRequests {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorActionLimitExceeded,
			"browser attachment reached its action limit",
			false,
		)
	}
	server.seen[requestID] = struct{}{}
	return nil
}

func (server *Server) decodeRequest(reader io.Reader) (browserprotocol.Request, *browserprotocol.Failure) {
	var request browserprotocol.Request
	limited := &io.LimitedReader{R: reader, N: server.options.MaxRequestBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		if limited.N == 0 {
			return request, browserprotocol.NewFailure(
				browserprotocol.ErrorRequestTooLarge,
				"browser request exceeds the input limit",
				false,
			)
		}
		return request, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser request is not valid",
			false,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if limited.N == 0 {
			return request, browserprotocol.NewFailure(
				browserprotocol.ErrorRequestTooLarge,
				"browser request exceeds the input limit",
				false,
			)
		}
		return request, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser request contains trailing data",
			false,
		)
	}
	if limited.N == 0 {
		return request, browserprotocol.NewFailure(
			browserprotocol.ErrorRequestTooLarge,
			"browser request exceeds the input limit",
			false,
		)
	}
	return request, nil
}

func (server *Server) writeResponse(writer io.Writer, response browserprotocol.Response) {
	raw, err := json.Marshal(response)
	if err != nil || len(raw)+1 > server.options.MaxResponseBytes {
		raw, _ = json.Marshal(browserprotocol.ErrorResponse(
			response.RequestID,
			browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"browser response exceeds the output limit",
				false,
			),
		))
	}
	raw = append(raw, '\n')
	_, _ = writer.Write(raw)
}
