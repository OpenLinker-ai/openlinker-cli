package agentexec

import (
	"context"
	"errors"
)

type browserToolBroker struct{}

func startBrowserToolBroker(
	context.Context,
	string,
	string,
	*browserRunLease,
) (*browserToolBroker, error) {
	return nil, errors.New("Browser execution profile requires a Linux container or Unix host")
}

func (*browserToolBroker) SocketPath() string { return "" }
func (*browserToolBroker) Close() error       { return nil }
