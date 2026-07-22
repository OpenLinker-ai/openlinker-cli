package runs

import (
	"errors"

	runsartifacts "github.com/OpenLinker-ai/openlinker-cli/pkg/runs/artifacts"
	runscancel "github.com/OpenLinker-ai/openlinker-cli/pkg/runs/cancel"
	runschildren "github.com/OpenLinker-ai/openlinker-cli/pkg/runs/children"
	runsevents "github.com/OpenLinker-ai/openlinker-cli/pkg/runs/events"
	runsget "github.com/OpenLinker-ai/openlinker-cli/pkg/runs/get"
	runsmessages "github.com/OpenLinker-ai/openlinker-cli/pkg/runs/messages"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	runs := &cobra.Command{
		Use:   "runs",
		Short: "Inspect run state and traces",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("runs requires a subcommand: get, children, events, messages, artifacts, cancel")
		},
	}
	runs.AddCommand(runsget.New(ioStreams, opts))
	runs.AddCommand(runschildren.New(ioStreams, opts))
	runs.AddCommand(runsevents.New(ioStreams, opts))
	runs.AddCommand(runsmessages.New(ioStreams, opts))
	runs.AddCommand(runsartifacts.New(ioStreams, opts))
	runs.AddCommand(runscancel.New(ioStreams, opts))
	return runs
}
