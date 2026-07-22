package plugin

import (
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/agent"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/pluginbridge"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, options *shared.GlobalOptions, agentService *agent.Service) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Run OpenLinker native plugin services"}
	command.AddCommand(newServeCommand(ioStreams, options, agentService))
	return command
}

func newServeCommand(ioStreams shared.IO, options *shared.GlobalOptions, agentService *agent.Service) *cobra.Command {
	var host string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the local OpenLinker MCP bridge over stdio",
		RunE: func(command *cobra.Command, args []string) error {
			host = strings.ToLower(strings.TrimSpace(host))
			if host != "codex" && host != "claude" {
				return errors.New("plugin serve requires --host codex or --host claude")
			}
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			server := &pluginbridge.Server{Host: host, IO: ioStreams, Options: options, Agent: agentService}
			return server.Serve(ctx, ioStreams.Stdin, ioStreams.Stdout)
		},
	}
	command.Flags().StringVar(&host, "host", "", "native host: codex or claude")
	return command
}
