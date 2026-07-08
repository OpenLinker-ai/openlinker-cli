package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const SDKAgent = "openlinker-cli/0.1"

type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

type GlobalOptions struct {
	APIBase      string
	UserToken    string
	RuntimeToken string
	Timeout      time.Duration
}

func (io IO) Env(key string) string {
	if io.Getenv == nil {
		return os.Getenv(key)
	}
	return io.Getenv(key)
}

func (io IO) FirstEnv(keys ...string) string {
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, io.Env(key))
	}
	return FirstNonEmpty(values...)
}

func DefaultGlobalOptions(getenv func(string) string) GlobalOptions {
	if getenv == nil {
		getenv = os.Getenv
	}
	return GlobalOptions{
		APIBase:      FirstNonEmpty(getenv("OPENLINKER_API_BASE"), getenv("OPENLINKER_API_URL"), "http://localhost:8080"),
		UserToken:    FirstNonEmpty(getenv("OPENLINKER_TOKEN"), getenv("OPENLINKER_USER_TOKEN"), getenv("OPENLINKER_DEMO_JWT")),
		RuntimeToken: FirstNonEmpty(getenv("OPENLINKER_RUNTIME_TOKEN"), getenv("OPENLINKER_AGENT_TOKEN")),
		Timeout:      60 * time.Second,
	}
}

func ContextForOptions(opts GlobalOptions) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opts.Timeout)
}

func UserClient(opts GlobalOptions) (*openlinker.Client, error) {
	return newClient(opts, false)
}

func RuntimeClient(opts GlobalOptions) (*openlinker.Client, error) {
	if strings.TrimSpace(opts.RuntimeToken) == "" {
		return nil, errors.New("OPENLINKER_RUNTIME_TOKEN is required for delegate")
	}
	return newClient(opts, true)
}

func newClient(opts GlobalOptions, runtime bool) (*openlinker.Client, error) {
	httpClient := &http.Client{Timeout: opts.Timeout}
	options := []openlinker.Option{
		openlinker.WithHTTPClient(httpClient),
		openlinker.WithSDKAgent(SDKAgent),
	}
	if runtime {
		options = append(options, openlinker.WithRuntimeToken(opts.RuntimeToken))
	} else if strings.TrimSpace(opts.UserToken) != "" {
		options = append(options, openlinker.WithUserToken(opts.UserToken))
	}
	return openlinker.NewClient(opts.APIBase, options...)
}

func Payload(stdin io.Reader, input, inputFile, text string) (any, error) {
	set := 0
	for _, value := range []string{input, inputFile, text} {
		if strings.TrimSpace(value) != "" {
			set++
		}
	}
	if set > 1 {
		return nil, errors.New("use only one of --input, --input-file, or --text")
	}
	switch {
	case strings.TrimSpace(text) != "":
		return openlinker.JSON{"text": text}, nil
	case strings.TrimSpace(inputFile) != "":
		raw, err := readInputFile(stdin, inputFile)
		if err != nil {
			return nil, err
		}
		return ParseInputPayload(raw)
	case strings.TrimSpace(input) != "":
		return ParseInputPayload([]byte(input))
	default:
		return openlinker.JSON{}, nil
	}
}

func readInputFile(stdin io.Reader, path string) ([]byte, error) {
	if strings.TrimSpace(path) == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

func WriteJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func ParseInputPayload(raw []byte) (any, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return openlinker.JSON{}, nil
	}
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err == nil {
		return payload, nil
	}
	return openlinker.JSON{"text": text}, nil
}

func ParseOptionalJSON(raw string) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func SplitCSV(raw string) []string {
	fields := strings.Split(raw, ",")
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(field); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func FirstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func MetadataError(err error) error {
	return fmt.Errorf("metadata: %w", err)
}

type StringList []string

func (l *StringList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *StringList) Set(value string) error {
	if strings.TrimSpace(value) != "" {
		*l = append(*l, strings.TrimSpace(value))
	}
	return nil
}

func (l *StringList) Type() string {
	return "stringList"
}
