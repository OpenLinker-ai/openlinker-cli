package root

import (
	"fmt"
	"io"
	"os"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/agents"
	contextcmd "github.com/OpenLinker-ai/openlinker-cli/pkg/context"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/delegate"
	runcmd "github.com/OpenLinker-ai/openlinker-cli/pkg/run"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/runs"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if getenv == nil {
		getenv = os.Getenv
	}
	ioStreams := shared.IO{Stdin: stdin, Stdout: stdout, Stderr: stderr, Getenv: getenv}
	opts := shared.DefaultGlobalOptions(getenv)
	cmd := NewCommand(ioStreams, &opts)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(stderr, "openlinker: %v\n", err)
		return 1
	}
	return 0
}

func NewCommand(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	if opts == nil {
		defaults := shared.DefaultGlobalOptions(ioStreams.Getenv)
		opts = &defaults
	}

	root := &cobra.Command{
		Use:           "openlinker",
		Short:         "JSON-first CLI for OpenLinker agents, skills, and automation",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Usage()
		},
	}
	root.SetOut(ioStreams.Stderr)
	root.SetErr(ioStreams.Stderr)
	root.SetUsageFunc(func(cmd *cobra.Command) error {
		printUsage(ioStreams.Stderr)
		return nil
	})
	root.PersistentFlags().StringVar(&opts.APIBase, "api", opts.APIBase, "OpenLinker Core API base URL")
	root.PersistentFlags().StringVar(&opts.UserToken, "token", opts.UserToken, "user JWT or API token")
	root.PersistentFlags().StringVar(&opts.RuntimeToken, "runtime-token", opts.RuntimeToken, "runtime token for delegate commands")
	root.PersistentFlags().DurationVar(&opts.Timeout, "timeout", opts.Timeout, "request timeout")

	root.AddCommand(contextcmd.New(ioStreams))
	root.AddCommand(agents.New(ioStreams, opts))
	root.AddCommand(runcmd.New(ioStreams, opts))
	root.AddCommand(delegate.New(ioStreams, opts))
	root.AddCommand(runs.New(ioStreams, opts))
	return root
}

func printUsage(stderr io.Writer) {
	fmt.Fprintln(stderr, `Usage:
  openlinker [global flags] context
  openlinker [global flags] agents search [--query q] [--tag tag] [--callable]
  openlinker [global flags] agents get --slug slug
  openlinker [global flags] agents card --slug slug [--extended]
  openlinker [global flags] run --agent agent_id [--input json|text] [--input-file file]
  openlinker [global flags] delegate --agent agent_id [--parent-run run_id] [--reason text] [--input json|text]
  openlinker [global flags] runs get --id run_id
  openlinker [global flags] runs children --id run_id
  openlinker [global flags] runs events --id run_id [--limit n]
  openlinker [global flags] runs messages --id run_id
  openlinker [global flags] runs artifacts --id run_id

Global flags:
  --api             OpenLinker Core API base URL, default OPENLINKER_API_BASE or http://localhost:8080
  --token           user token, default OPENLINKER_TOKEN / OPENLINKER_USER_TOKEN
  --runtime-token   runtime token for delegate, default OPENLINKER_RUNTIME_TOKEN
  --timeout         request timeout

The CLI always writes JSON to stdout and never prints configured tokens.`)
}
