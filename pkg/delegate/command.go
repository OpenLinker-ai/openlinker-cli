package delegate

import (
	"errors"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var (
		targetAgentID  string
		parentRunID    string
		reason         string
		input          string
		inputFile      string
		text           string
		metadata       string
		contextID      string
		traceID        string
		referenceTasks string
	)
	cmd := &cobra.Command{
		Use:   "delegate",
		Short: "Delegate from the current OpenLinker runtime run",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(targetAgentID) == "" {
				return errors.New("delegate requires --agent")
			}
			if strings.TrimSpace(parentRunID) == "" {
				return errors.New("delegate requires --parent-run or OPENLINKER_RUN_ID")
			}
			payload, err := shared.Payload(ioStreams.Stdin, input, inputFile, text)
			if err != nil {
				return err
			}
			meta, err := shared.ParseOptionalJSON(metadata)
			if err != nil {
				return shared.MetadataError(err)
			}
			ctx, cancel := shared.ContextForOptions(*opts)
			defer cancel()
			client, err := shared.RuntimeClient(*opts)
			if err != nil {
				return err
			}
			out, err := client.CallAgent(ctx, openlinker.CallAgentRequest{
				ParentRunID:      strings.TrimSpace(parentRunID),
				TargetAgentID:    strings.TrimSpace(targetAgentID),
				Reason:           strings.TrimSpace(reason),
				Input:            payload,
				Metadata:         meta,
				ContextID:        strings.TrimSpace(contextID),
				TraceID:          strings.TrimSpace(traceID),
				ReferenceTaskIDs: shared.SplitCSV(referenceTasks),
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&targetAgentID, "agent", "", "target agent id")
	cmd.Flags().StringVar(&parentRunID, "parent-run", ioStreams.Env("OPENLINKER_RUN_ID"), "parent/current run id; defaults to OPENLINKER_RUN_ID")
	cmd.Flags().StringVar(&reason, "reason", "", "delegation reason")
	cmd.Flags().StringVar(&input, "input", "", "JSON payload or plain text")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "file containing JSON payload or plain text; use - for stdin")
	cmd.Flags().StringVar(&text, "text", "", "plain text input")
	cmd.Flags().StringVar(&metadata, "metadata", "", "JSON metadata")
	cmd.Flags().StringVar(&contextID, "context-id", "", "A2A context id")
	cmd.Flags().StringVar(&traceID, "trace-id", ioStreams.Env("OPENLINKER_TRACE_ID"), "trace id; defaults to OPENLINKER_TRACE_ID")
	cmd.Flags().StringVar(&referenceTasks, "reference-task", "", "comma-separated reference task ids")
	return cmd
}
