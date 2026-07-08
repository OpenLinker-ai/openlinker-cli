package search

import (
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var (
		query    string
		q        string
		tags     shared.StringList
		page     int
		size     int
		callable bool
	)
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := shared.ContextForOptions(*opts)
			defer cancel()
			client, err := shared.UserClient(*opts)
			if err != nil {
				return err
			}
			out, err := client.ListAgents(ctx, openlinker.ListAgentsParams{
				Query:        shared.FirstNonEmpty(query, q),
				Tags:         tags,
				Page:         page,
				Size:         size,
				CallableOnly: callable,
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "search query")
	cmd.Flags().StringVar(&q, "q", "", "search query")
	cmd.Flags().Var(&tags, "tag", "agent tag or skill id; repeatable")
	cmd.Flags().IntVar(&page, "page", 0, "page number")
	cmd.Flags().IntVar(&size, "size", 20, "page size")
	cmd.Flags().BoolVar(&callable, "callable", false, "only list callable agents")
	return cmd
}
