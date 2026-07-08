package events

import (
	"errors"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var (
		runID string
		after int
		limit int
	)
	cmd := &cobra.Command{
		Use:   "events [run_id]",
		Short: "List run events",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := shared.FirstNonEmpty(runID, shared.FirstArg(args))
			if id == "" {
				return errors.New("runs events requires --id or positional run id")
			}
			ctx, cancel := shared.ContextForOptions(*opts)
			defer cancel()
			client, err := shared.UserClient(*opts)
			if err != nil {
				return err
			}
			out, err := client.ListRunEvents(ctx, id, openlinker.ListRunEventsParams{
				AfterSequence: int32(after),
				Limit:         int32(limit),
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&runID, "id", "", "run id")
	cmd.Flags().IntVar(&after, "after-sequence", 0, "only return events after this sequence")
	cmd.Flags().IntVar(&limit, "limit", 100, "max events")
	return cmd
}
