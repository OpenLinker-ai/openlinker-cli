package tasks

import (
	"errors"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	taskscreate "github.com/OpenLinker-ai/openlinker-cli/pkg/tasks/create"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	tasks := &cobra.Command{
		Use:   "tasks",
		Short: "Resolve private task intents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("tasks requires a subcommand: create")
		},
	}
	tasks.AddCommand(taskscreate.New(ioStreams, opts))
	return tasks
}
