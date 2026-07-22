package create

import (
	"errors"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var (
		query      string
		templateID string
		skillIDs   shared.StringList
		mcpTools   shared.StringList
		agentSlugs shared.StringList
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Resolve a private task into skills and Agent recommendations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(query) == "" {
				return errors.New("tasks create requires --query")
			}
			ctx, cancel := shared.ContextForOptions(*opts)
			defer cancel()
			client, err := shared.UserClient(*opts)
			if err != nil {
				return err
			}
			out, err := client.RecommendTask(ctx, openlinker.RecommendTaskRequest{
				Query:      strings.TrimSpace(query),
				TemplateID: strings.TrimSpace(templateID),
				SkillIDs:   skillIDs,
				MCPTools:   mcpTools,
				AgentSlugs: agentSlugs,
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "natural-language task intent")
	cmd.Flags().StringVar(&templateID, "template", "", "task template id")
	cmd.Flags().Var(&skillIDs, "skill", "skill id; repeatable")
	cmd.Flags().Var(&mcpTools, "mcp-tool", "OpenLinker tool name; repeatable")
	cmd.Flags().Var(&agentSlugs, "agent-slug", "candidate Agent slug; repeatable")
	return cmd
}
