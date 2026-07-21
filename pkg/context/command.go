package contextcmd

import (
	"github.com/OpenLinker-ai/openlinker-cli/pkg/buildinfo"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "Print the current OpenLinker execution context",
		RunE: func(cmd *cobra.Command, args []string) error {
			return shared.WriteJSON(ioStreams.Stdout, map[string]any{
				"api_base":        opts.APIBase,
				"run_id":          ioStreams.Env("OPENLINKER_RUN_ID"),
				"agent_id":        ioStreams.Env("OPENLINKER_AGENT_ID"),
				"trace_id":        ioStreams.Env("OPENLINKER_TRACE_ID"),
				"cli_version":     buildinfo.Version,
				"surface_version": buildinfo.SurfaceVersion,
				"capabilities":    buildinfo.Capabilities(),
			})
		},
	}
}
