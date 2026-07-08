package get

import (
	"errors"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var runID string
	cmd := &cobra.Command{
		Use:   "get [run_id]",
		Short: "Get a run",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := shared.FirstNonEmpty(runID, shared.FirstArg(args))
			if id == "" {
				return errors.New("runs get requires --id or positional run id")
			}
			ctx, cancel := shared.ContextForOptions(*opts)
			defer cancel()
			client, err := shared.UserClient(*opts)
			if err != nil {
				return err
			}
			out, err := client.GetRun(ctx, id)
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&runID, "id", "", "run id")
	return cmd
}
