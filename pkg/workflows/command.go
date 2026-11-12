// Package workflows implements `openlinker workflows`: list, inspect, run
// and follow workflows through the platform API. It uses the CLI's own HTTP
// client (the SDK has no workflow client; see Q6 of the workflow pipeline
// design).
package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

const maxResponseBytes = 8 << 20

var terminalRunStatuses = map[string]bool{"success": true, "failed": true, "canceled": true}

// APIError is a non-2xx platform response.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(opts shared.GlobalOptions) (*client, error) {
	base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(opts.APIBase), "/"), "/api/v1")
	if base == "" {
		return nil, errors.New("API base URL is required")
	}
	if _, err := url.ParseRequestURI(base); err != nil {
		return nil, fmt.Errorf("invalid API base URL: %w", err)
	}
	httpClient := &http.Client{Timeout: opts.Timeout}
	if opts.SavedCredential {
		// A saved credential is never forwarded to a redirect target.
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &client{base: base, token: strings.TrimSpace(opts.UserToken), http: httpClient}, nil
}

func (c *client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	target := c.base + "/api/v1/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "openlinker-cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &parsed)
		apiErr := &APIError{StatusCode: resp.StatusCode, Code: parsed.Error.Code, Message: parsed.Error.Message}
		if apiErr.Code == "" {
			apiErr.Code = "HTTP_" + strconv.Itoa(resp.StatusCode)
		}
		if apiErr.Message == "" {
			apiErr.Message = resp.Status
		}
		return apiErr
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// New returns the `workflows` command group.
func New(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workflows",
		Aliases: []string{"workflow"},
		Short:   "List, inspect and run workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("workflows requires a subcommand: list, get, run, runs, status, cancel")
		},
	}
	cmd.AddCommand(listCommand(ioStreams, opts))
	cmd.AddCommand(getCommand(ioStreams, opts))
	cmd.AddCommand(runCommand(ioStreams, opts))
	cmd.AddCommand(runsCommand(ioStreams, opts))
	cmd.AddCommand(statusCommand(ioStreams, opts))
	cmd.AddCommand(cancelCommand(ioStreams, opts))
	return cmd
}

func withClient(opts *shared.GlobalOptions, fn func(context.Context, *client) (any, error)) (any, error) {
	ctx, cancel := shared.ContextForOptions(*opts)
	defer cancel()
	c, err := newClient(*opts)
	if err != nil {
		return nil, err
	}
	return fn(ctx, c)
}

func listCommand(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var status, query string
	var size int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := withClient(opts, func(ctx context.Context, c *client) (any, error) {
				params := url.Values{}
				if status != "" {
					params.Set("status", status)
				}
				if query != "" {
					params.Set("q", query)
				}
				if size > 0 {
					params.Set("size", strconv.Itoa(size))
				}
				var result json.RawMessage
				return result, c.do(ctx, http.MethodGet, "workflows", params, nil, &result)
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status (active or archived)")
	cmd.Flags().StringVar(&query, "query", "", "search by name")
	cmd.Flags().IntVar(&size, "size", 0, "page size")
	return cmd
}

func requireID(kind string, flag string, args []string) (string, error) {
	id := shared.FirstNonEmpty(flag, shared.FirstArg(args))
	if id == "" {
		return "", fmt.Errorf("requires --id or a positional %s id", kind)
	}
	return url.PathEscape(id), nil
}

func getCommand(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var id string
	var version int
	cmd := &cobra.Command{
		Use:   "get [workflow_id]",
		Short: "Get a workflow definition (the current version, or --version)",
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, err := requireID("workflow", id, args)
			if err != nil {
				return err
			}
			out, err := withClient(opts, func(ctx context.Context, c *client) (any, error) {
				path := "workflows/" + workflowID
				if version > 0 {
					path += "/versions/" + strconv.Itoa(version)
				}
				var result json.RawMessage
				return result, c.do(ctx, http.MethodGet, path, nil, nil, &result)
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "workflow id")
	cmd.Flags().IntVar(&version, "version", 0, "definition version")
	return cmd
}

type runState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func runCommand(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var id, input, inputFile, text string
	var version int
	var wait bool
	var pollInterval time.Duration
	cmd := &cobra.Command{
		Use:   "run [workflow_id]",
		Short: "Start a workflow run (queued; --wait follows it to the end)",
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, err := requireID("workflow", id, args)
			if err != nil {
				return err
			}
			payload, err := shared.Payload(ioStreams.Stdin, input, inputFile, text)
			if err != nil {
				return err
			}
			if payload == nil {
				payload = map[string]any{}
			}
			if encoded, err := json.Marshal(payload); err != nil || !bytes.HasPrefix(bytes.TrimSpace(encoded), []byte("{")) {
				return errors.New("workflow input must be a JSON object")
			}
			body := map[string]any{"input": payload}
			if version > 0 {
				body["version"] = version
			}
			c, err := newClient(*opts)
			if err != nil {
				return err
			}
			ctx, cancel := shared.ContextForOptions(*opts)
			var started json.RawMessage
			err = c.do(ctx, http.MethodPost, "workflows/"+workflowID+"/runs", nil, body, &started)
			cancel()
			if err != nil {
				return err
			}
			if !wait {
				return shared.WriteJSON(ioStreams.Stdout, started)
			}
			var state runState
			if err := json.Unmarshal(started, &state); err != nil || state.ID == "" {
				return errors.New("workflow run response has no id")
			}
			for {
				ctx, cancel := shared.ContextForOptions(*opts)
				var current json.RawMessage
				err := c.do(ctx, http.MethodGet, "workflow-runs/"+url.PathEscape(state.ID), nil, nil, &current)
				cancel()
				if err != nil {
					return err
				}
				if err := json.Unmarshal(current, &state); err != nil {
					return err
				}
				if terminalRunStatuses[state.Status] {
					if err := shared.WriteJSON(ioStreams.Stdout, current); err != nil {
						return err
					}
					if state.Status != "success" {
						return fmt.Errorf("workflow run %s ended %s", state.ID, state.Status)
					}
					return nil
				}
				time.Sleep(pollInterval)
			}
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "workflow id")
	cmd.Flags().StringVar(&input, "input", "", "input JSON object")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "read input JSON from a file (- for stdin)")
	cmd.Flags().StringVar(&text, "text", "", "input as {\"text\": ...}")
	cmd.Flags().IntVar(&version, "version", 0, "definition version to run (default: current)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the run to finish and print the final run")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 2*time.Second, "status poll interval with --wait")
	return cmd
}

func runsCommand(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var id, status string
	var size int
	cmd := &cobra.Command{
		Use:   "runs [workflow_id]",
		Short: "List runs of a workflow",
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, err := requireID("workflow", id, args)
			if err != nil {
				return err
			}
			out, err := withClient(opts, func(ctx context.Context, c *client) (any, error) {
				params := url.Values{"sort": {"created_desc"}}
				if status != "" {
					params.Set("status", status)
				}
				if size > 0 {
					params.Set("size", strconv.Itoa(size))
				}
				var result json.RawMessage
				return result, c.do(ctx, http.MethodGet, "workflows/"+workflowID+"/runs", params, nil, &result)
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "workflow id")
	cmd.Flags().StringVar(&status, "status", "", "filter by run status")
	cmd.Flags().IntVar(&size, "size", 0, "page size")
	return cmd
}

func statusCommand(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "status [run_id]",
		Short: "Get a workflow run snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := requireID("workflow run", id, args)
			if err != nil {
				return err
			}
			out, err := withClient(opts, func(ctx context.Context, c *client) (any, error) {
				var result json.RawMessage
				return result, c.do(ctx, http.MethodGet, "workflow-runs/"+runID, nil, nil, &result)
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "workflow run id")
	return cmd
}

func cancelCommand(ioStreams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "cancel [run_id]",
		Short: "Cancel a workflow run",
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := requireID("workflow run", id, args)
			if err != nil {
				return err
			}
			out, err := withClient(opts, func(ctx context.Context, c *client) (any, error) {
				var result json.RawMessage
				return result, c.do(ctx, http.MethodPost, "workflow-runs/"+runID+"/cancel", nil, nil, &result)
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, out)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "workflow run id")
	return cmd
}
