//go:build !windows

package browserruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const (
	engineContractID     = "openlinker.browser.engine.v1"
	maxEngineOutputBytes = browserprotocol.MaxResponseBytes
)

type ProcessEngineOptions struct {
	Command     []string
	Environment []string
}

type ProcessEngine struct {
	options ProcessEngineOptions
	mu      sync.Mutex
	process *engineProcess
	nextID  uint64
	closed  bool
}

type engineProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
}

type engineRequest struct {
	ContractID string                   `json:"contract_id"`
	ActionID   string                   `json:"action_id"`
	Deadline   string                   `json:"deadline"`
	Identity   browserprotocol.Identity `json:"identity"`
	Action     browserprotocol.Action   `json:"action"`
}

type engineResponse struct {
	ContractID  string                       `json:"contract_id"`
	ActionID    string                       `json:"action_id"`
	Status      string                       `json:"status"`
	Observation *browserprotocol.Observation `json:"observation,omitempty"`
	Error       *browserprotocol.Failure     `json:"error,omitempty"`
}

func NewProcessEngine(options ProcessEngineOptions) (*ProcessEngine, error) {
	if len(options.Command) == 0 ||
		!filepath.IsAbs(options.Command[0]) ||
		filepath.Clean(options.Command[0]) != options.Command[0] {
		return nil, errors.New("browser engine command must use an absolute executable path")
	}
	for _, argument := range options.Command[1:] {
		if strings.ContainsRune(argument, 0) {
			return nil, errors.New("browser engine command contains an invalid argument")
		}
	}
	if err := validateEngineEnvironment(options.Environment); err != nil {
		return nil, err
	}
	return &ProcessEngine{options: options}, nil
}

func (engine *ProcessEngine) Execute(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	if engine == nil {
		return browserprotocol.Observation{}, runtimeUnavailable("browser engine is nil")
	}
	if failure := identity.Validate(); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	if failure := action.Validate(); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return browserprotocol.Observation{}, runtimeUnavailable("browser engine is closed")
	}
	if err := ctx.Err(); err != nil {
		return browserprotocol.Observation{}, contextFailure(err)
	}
	process, err := engine.ensureProcess()
	if err != nil {
		return browserprotocol.Observation{}, runtimeUnavailable("browser engine could not start")
	}
	engine.nextID++
	actionID := strconv.FormatUint(engine.nextID, 10)
	deadline, ok := ctx.Deadline()
	if !ok {
		engine.resetProcess()
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser engine action requires a deadline",
			false,
		)
	}
	request := engineRequest{
		ContractID: engineContractID,
		ActionID:   actionID,
		Deadline:   deadline.UTC().Format(browserTimeFormat),
		Identity:   identity,
		Action:     action,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		engine.resetProcess()
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"browser engine request could not be encoded",
			false,
		)
	}
	raw = append(raw, '\n')
	if err := writeAllProcess(process.stdin, raw); err != nil {
		engine.resetProcess()
		return browserprotocol.Observation{}, runtimeUnavailable("browser engine input failed")
	}

	type readResult struct {
		value []byte
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		value, readErr := readBoundedLine(process.stdout, maxEngineOutputBytes)
		result <- readResult{value: value, err: readErr}
	}()

	select {
	case <-ctx.Done():
		engine.resetProcess()
		<-result
		return browserprotocol.Observation{}, contextFailure(ctx.Err())
	case output := <-result:
		if err := ctx.Err(); err != nil {
			engine.resetProcess()
			return browserprotocol.Observation{}, contextFailure(err)
		}
		if output.err != nil {
			engine.resetProcess()
			if errors.Is(output.err, errEngineOutputTooLarge) {
				return browserprotocol.Observation{}, browserprotocol.NewFailure(
					browserprotocol.ErrorOutputTooLarge,
					"browser engine response exceeds the output limit",
					false,
				)
			}
			return browserprotocol.Observation{}, runtimeUnavailable("browser engine output failed")
		}
		response, failure := decodeEngineResponse(output.value, actionID)
		if failure != nil {
			engine.resetProcess()
			return browserprotocol.Observation{}, failure
		}
		if response.Error != nil {
			return browserprotocol.Observation{}, normalizeEngineFailure(response.Error)
		}
		if validationFailure := response.Observation.Validate(); validationFailure != nil {
			engine.resetProcess()
			return browserprotocol.Observation{}, validationFailure
		}
		return *response.Observation, nil
	}
}

func (engine *ProcessEngine) Close() error {
	if engine == nil {
		return nil
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.closed = true
	return engine.resetProcess()
}

func (engine *ProcessEngine) ensureProcess() (*engineProcess, error) {
	if engine.process != nil {
		return engine.process, nil
	}
	command := exec.Command(engine.options.Command[0], engine.options.Command[1:]...) // #nosec G204 -- trusted fixed image configuration, never caller input.
	command.Env = make([]string, len(engine.options.Environment))
	copy(command.Env, engine.options.Environment)
	command.Stderr = io.Discard
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	engine.process = &engineProcess{
		command: command,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 64<<10),
	}
	return engine.process, nil
}

func (engine *ProcessEngine) resetProcess() error {
	process := engine.process
	engine.process = nil
	if process == nil {
		return nil
	}
	_ = process.stdin.Close()
	if process.command.Process != nil {
		_ = process.command.Process.Kill()
	}
	waitErr := process.command.Wait()
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		return nil
	}
	return waitErr
}

func decodeEngineResponse(raw []byte, actionID string) (engineResponse, *browserprotocol.Failure) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response engineResponse
	if err := decoder.Decode(&response); err != nil {
		return engineResponse{}, invalidEngineOutput()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return engineResponse{}, invalidEngineOutput()
	}
	if response.ContractID != engineContractID || response.ActionID != actionID {
		return engineResponse{}, invalidEngineOutput()
	}
	switch response.Status {
	case "ok":
		if response.Observation == nil || response.Error != nil {
			return engineResponse{}, invalidEngineOutput()
		}
	case "error":
		if response.Error == nil || response.Observation != nil {
			return engineResponse{}, invalidEngineOutput()
		}
	default:
		return engineResponse{}, invalidEngineOutput()
	}
	return response, nil
}

var errEngineOutputTooLarge = errors.New("browser engine output is too large")

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var output []byte
	for {
		fragment, more, err := reader.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(output)+len(fragment) > limit {
			return nil, errEngineOutputTooLarge
		}
		output = append(output, fragment...)
		if !more {
			return output, nil
		}
	}
}

func writeAllProcess(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if written < 0 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validateEngineEnvironment(environment []string) error {
	allowed := map[string]bool{
		"HOME":                            true,
		"LANG":                            true,
		"LC_ALL":                          true,
		"NO_PROXY":                        true,
		"OPENLINKER_BROWSER_EGRESS_PROXY": true,
		"OPENLINKER_BROWSER_PROFILE_DIR":  true,
		"PLAYWRIGHT_BROWSERS_PATH":        true,
		"TMPDIR":                          true,
		"TZ":                              true,
	}
	seen := make(map[string]bool, len(environment))
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if !found || key == "" || !allowed[key] || seen[key] || strings.ContainsRune(value, 0) {
			return errors.New("browser engine environment contains a forbidden entry")
		}
		if key == "NO_PROXY" && value != "" {
			return errors.New("browser engine NO_PROXY must be empty")
		}
		switch key {
		case "HOME", "TMPDIR", "OPENLINKER_BROWSER_PROFILE_DIR", "PLAYWRIGHT_BROWSERS_PATH":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value {
				return errors.New("browser engine environment path must be absolute and clean")
			}
		case "OPENLINKER_BROWSER_EGRESS_PROXY":
			proxy, err := url.Parse(value)
			if err != nil || proxy.Scheme != "http" || proxy.Host == "" || proxy.User != nil ||
				proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" {
				return errors.New("browser engine egress proxy must be an HTTP origin without credentials")
			}
		}
		seen[key] = true
	}
	return nil
}

func normalizeEngineFailure(failure *browserprotocol.Failure) *browserprotocol.Failure {
	if failure == nil || !allowedEngineErrorCode(failure.Code) {
		return invalidEngineOutput()
	}
	return browserprotocol.NewFailure(failure.Code, failure.Message, failure.Recoverable)
}

func allowedEngineErrorCode(code browserprotocol.ErrorCode) bool {
	switch code {
	case browserprotocol.ErrorOutputTooLarge,
		browserprotocol.ErrorRuntimeUnavailable,
		browserprotocol.ErrorEgressUnavailable,
		browserprotocol.ErrorTargetBlocked,
		browserprotocol.ErrorProfileLocked,
		browserprotocol.ErrorProfileCorrupt,
		browserprotocol.ErrorConversationRecovery,
		browserprotocol.ErrorUserActionRequired,
		browserprotocol.ErrorHighImpactActionBlocked,
		browserprotocol.ErrorActionLimitExceeded,
		browserprotocol.ErrorCanceled,
		browserprotocol.ErrorOutputInvalid,
		browserprotocol.ErrorInternal:
		return true
	default:
		return false
	}
}

func contextFailure(err error) *browserprotocol.Failure {
	if errors.Is(err, context.DeadlineExceeded) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser action deadline elapsed",
			true,
		)
	}
	return browserprotocol.NewFailure(
		browserprotocol.ErrorCanceled,
		"browser action was canceled",
		true,
	)
}

func runtimeUnavailable(message string) *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		message,
		true,
	)
}

func invalidEngineOutput() *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorOutputInvalid,
		"browser engine response is invalid",
		false,
	)
}

const browserTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"
