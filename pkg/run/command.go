package runcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var (
		agentID        string
		input          string
		inputFile      string
		text           string
		metadata       string
		async          bool
		idempotencyKey string
		conversationID string
		protocolTaskID string
		a2aContext     string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run an agent from a user/API context",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(agentID) == "" {
				return errors.New("run requires --agent")
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
			client, err := shared.UserClient(*opts)
			if err != nil {
				return err
			}
			request := openlinker.RunAgentRequest{
				AgentID:        strings.TrimSpace(agentID),
				Input:          payload,
				Metadata:       meta,
				IdempotencyKey: strings.TrimSpace(idempotencyKey),
			}
			if strings.TrimSpace(a2aContext) != "" {
				var contextValue openlinker.RunA2AContext
				if err := json.Unmarshal([]byte(a2aContext), &contextValue); err != nil {
					return fmt.Errorf("a2a context: %w", err)
				}
				request.A2AContext = &contextValue
			}
			if strings.TrimSpace(conversationID) != "" {
				if request.A2AContext == nil {
					request.A2AContext = &openlinker.RunA2AContext{}
				}
				if request.A2AContext.ProtocolContextID == "" {
					request.A2AContext.ProtocolContextID = strings.TrimSpace(conversationID)
				}
				if request.A2AContext.RootContextID == "" {
					request.A2AContext.RootContextID = strings.TrimSpace(conversationID)
				}
			}
			if strings.TrimSpace(protocolTaskID) != "" {
				if request.A2AContext == nil {
					request.A2AContext = &openlinker.RunA2AContext{}
				}
				request.A2AContext.ProtocolTaskID = strings.TrimSpace(protocolTaskID)
			}
			var out *openlinker.RunResponse
			if async {
				out, err = client.StartAgentRun(ctx, request)
			} else {
				out, err = client.RunAgent(ctx, request)
			}
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "target agent id")
	cmd.Flags().StringVar(&input, "input", "", "JSON payload or plain text")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "file containing JSON payload or plain text; use - for stdin")
	cmd.Flags().StringVar(&text, "text", "", "plain text input")
	cmd.Flags().StringVar(&metadata, "metadata", "", "JSON metadata")
	cmd.Flags().BoolVar(&async, "async", false, "create the run and return without waiting for terminal state")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable run-creation idempotency key")
	cmd.Flags().StringVar(&conversationID, "conversation-id", "", "stable Core conversation ID for provider session reuse")
	cmd.Flags().StringVar(&protocolTaskID, "protocol-task-id", "", "protocol task ID within the conversation")
	cmd.Flags().StringVar(&a2aContext, "a2a-context", "", "full A2A context JSON")
	return cmd
}
