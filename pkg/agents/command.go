package agents

import (
	"errors"

	agentcard "github.com/OpenLinker-ai/openlinker-cli/pkg/agents/card"
	agentget "github.com/OpenLinker-ai/openlinker-cli/pkg/agents/get"
	agentsearch "github.com/OpenLinker-ai/openlinker-cli/pkg/agents/search"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	agents := &cobra.Command{
		Use:   "agents",
		Short: "Discover and inspect agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("agents requires a subcommand: search, get, card")
		},
	}
	agents.AddCommand(agentsearch.New(ioStreams, opts))
	agents.AddCommand(agentget.New(ioStreams, opts))
	agents.AddCommand(agentcard.New(ioStreams, opts))
	return agents
}
