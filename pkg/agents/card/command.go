package card

import (
	"errors"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var (
		slug     string
		extended bool
	)
	cmd := &cobra.Command{
		Use:   "card [slug]",
		Short: "Get an agent card by slug",
		RunE: func(cmd *cobra.Command, args []string) error {
			value := shared.FirstNonEmpty(slug, shared.FirstArg(args))
			if value == "" {
				return errors.New("agents card requires --slug or positional slug")
			}
			ctx, cancel := shared.ContextForOptions(*opts)
			defer cancel()
			client, err := shared.UserClient(*opts)
			if err != nil {
				return err
			}
			out, err := client.GetAgentCard(ctx, value, extended)
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&slug, "slug", "", "agent slug")
	cmd.Flags().BoolVar(&extended, "extended", false, "fetch extended agent card")
	return cmd
}
