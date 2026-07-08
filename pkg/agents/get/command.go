package get

import (
	"errors"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var slug string
	cmd := &cobra.Command{
		Use:   "get [slug]",
		Short: "Get an agent by slug",
		RunE: func(cmd *cobra.Command, args []string) error {
			value := shared.FirstNonEmpty(slug, shared.FirstArg(args))
			if value == "" {
				return errors.New("agents get requires --slug or positional slug")
			}
			ctx, cancel := shared.ContextForOptions(*opts)
			defer cancel()
			client, err := shared.UserClient(*opts)
			if err != nil {
				return err
			}
			out, err := client.GetAgent(ctx, value)
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&slug, "slug", "", "agent slug")
	return cmd
}
