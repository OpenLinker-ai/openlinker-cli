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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const (
	engineContractID       = "openlinker.browser.engine.v2"
	engineViewerContractID = "openlinker.browser.engine.viewer.v1"
	maxEngineOutputBytes   = browserprotocol.MaxResponseBytes
	maxEngineLogBytes      = 32 << 10
	maxEngineLogLine       = 4 << 10
)

type ProcessEngineOptions struct {
	Command          []string
	Environment      []string
	DiagnosticWriter io.Writer
}

type ProcessEngine struct {
	options ProcessEngineOptions
	mu      sync.Mutex
	process *engineProcess
	nextID  uint64
	closed  bool
}

type engineProcess struct {
	instanceID uint64
	command    *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	diagnostic *engineDiagnosticWriter
}

var processEngineInstanceSequence atomic.Uint64

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

type engineViewerRequest struct {
	ContractID string                          `json:"contract_id"`
	ActionID   string                          `json:"action_id"`
	Deadline   string                          `json:"deadline"`
	Identity   browserprotocol.Identity        `json:"identity"`
	Operation  browserprotocol.ViewerOperation `json:"operation"`
	Input      *browserprotocol.ViewerInput    `json:"input,omitempty"`
}

type engineViewerResponse struct {
	ContractID string                       `json:"contract_id"`
	ActionID   string                       `json:"action_id"`
	Status     string                       `json:"status"`
	Frame      *browserprotocol.ViewerFrame `json:"frame,omitempty"`
	Error      *browserprotocol.Failure     `json:"error,omitempty"`
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
	if failure := action.ValidateForPolicy(identity.BrowserInteractionPolicy); failure != nil {
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
		return browserprotocol.Observation{}, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser engine action requires a deadline",
			false,
		))
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
		return browserprotocol.Observation{}, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"browser engine request could not be encoded",
			false,
		))
	}
	raw = append(raw, '\n')
	if err := writeAllProcess(process.stdin, raw); err != nil {
		engine.resetProcess()
		return browserprotocol.Observation{}, processResetFailure(
			runtimeUnavailable("browser engine input failed"),
		)
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
		return browserprotocol.Observation{}, processResetFailure(contextFailure(ctx.Err()))
	case output := <-result:
		if err := ctx.Err(); err != nil {
			engine.resetProcess()
			return browserprotocol.Observation{}, processResetFailure(contextFailure(err))
		}
		if output.err != nil {
			engine.resetProcess()
			if errors.Is(output.err, errEngineOutputTooLarge) {
				return browserprotocol.Observation{}, processResetFailure(browserprotocol.NewFailure(
					browserprotocol.ErrorOutputTooLarge,
					"browser engine response exceeds the output limit",
					false,
				))
			}
			return browserprotocol.Observation{}, processResetFailure(
				runtimeUnavailable("browser engine output failed"),
			)
		}
		response, failure := decodeEngineResponse(output.value, actionID)
		if failure != nil {
			engine.resetProcess()
			return browserprotocol.Observation{}, processResetFailure(failure)
		}
		if response.Error != nil {
			failure := normalizeEngineFailure(response.Error)
			failure.EngineInstanceID = process.instanceID
			return browserprotocol.Observation{}, failure
		}
		if validationFailure := response.Observation.ValidateEngine(); validationFailure != nil {
			engine.resetProcess()
			return browserprotocol.Observation{}, processResetFailure(validationFailure)
		}
		response.Observation.EngineInstanceID = process.instanceID
		return *response.Observation, nil
	}
}

func (engine *ProcessEngine) ExecuteViewer(
	ctx context.Context,
	identity browserprotocol.Identity,
	operation browserprotocol.ViewerOperation,
	input *browserprotocol.ViewerInput,
) (*browserprotocol.ViewerFrame, *browserprotocol.Failure) {
	if engine == nil {
		return nil, runtimeUnavailable("browser engine is nil")
	}
	requestValidation := browserprotocol.ViewerRequest{
		ContractID: browserprotocol.ViewerContractID,
		RequestID:  "11111111-1111-4111-8111-111111111111",
		Deadline:   time.Now().UTC().Add(time.Second),
		Identity:   identity,
		Operation:  operation,
		Input:      input,
	}
	if failure := requestValidation.Validate(time.Now().UTC()); failure != nil {
		return nil, failure
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return nil, runtimeUnavailable("browser engine is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, contextFailure(err)
	}
	process, err := engine.ensureProcess()
	if err != nil {
		return nil, runtimeUnavailable("browser engine could not start")
	}
	engine.nextID++
	actionID := strconv.FormatUint(engine.nextID, 10)
	deadline, ok := ctx.Deadline()
	if !ok {
		engine.resetProcess()
		return nil, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser engine viewer operation requires a deadline",
			false,
		))
	}
	request := engineViewerRequest{
		ContractID: engineViewerContractID,
		ActionID:   actionID,
		Deadline:   deadline.UTC().Format(browserTimeFormat),
		Identity:   identity,
		Operation:  operation,
		Input:      input,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		engine.resetProcess()
		return nil, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"browser engine viewer request could not be encoded",
			false,
		))
	}
	raw = append(raw, '\n')
	if err := writeAllProcess(process.stdin, raw); err != nil {
		engine.resetProcess()
		return nil, processResetFailure(
			runtimeUnavailable("browser engine viewer input failed"),
		)
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
		return nil, processResetFailure(contextFailure(ctx.Err()))
	case output := <-result:
		if err := ctx.Err(); err != nil {
			engine.resetProcess()
			return nil, processResetFailure(contextFailure(err))
		}
		if output.err != nil {
			engine.resetProcess()
			if errors.Is(output.err, errEngineOutputTooLarge) {
				return nil, processResetFailure(browserprotocol.NewFailure(
					browserprotocol.ErrorOutputTooLarge,
					"browser engine viewer response exceeds the output limit",
					false,
				))
			}
			return nil, processResetFailure(
				runtimeUnavailable("browser engine viewer output failed"),
			)
		}
		response, failure := decodeEngineViewerResponse(output.value, actionID)
		if failure != nil {
			engine.resetProcess()
			return nil, processResetFailure(failure)
		}
		if response.Error != nil {
			return nil, normalizeEngineFailure(response.Error)
		}
		if response.Frame != nil {
			if validationFailure := response.Frame.Validate(); validationFailure != nil {
				engine.resetProcess()
				return nil, processResetFailure(validationFailure)
			}
		}
		return response.Frame, nil
	}
}

func processResetFailure(failure *browserprotocol.Failure) *browserprotocol.Failure {
	if failure != nil {
		failure.EngineProcessReset = true
	}
	return failure
}

func (engine *ProcessEngine) Close() error {
	if engine == nil {
		return nil
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.closed = true
	return engine.closeProcess(true)
}

func (engine *ProcessEngine) ensureProcess() (*engineProcess, error) {
	if engine.process != nil {
		return engine.process, nil
	}
	command := exec.Command(engine.options.Command[0], engine.options.Command[1:]...) // #nosec G204 -- trusted fixed image configuration, never caller input.
	command.Env = make([]string, len(engine.options.Environment))
	copy(command.Env, engine.options.Environment)
	diagnosticTarget := engine.options.DiagnosticWriter
	if diagnosticTarget == nil {
		diagnosticTarget = os.Stderr
	}
	diagnostic := newEngineDiagnosticWriter(diagnosticTarget)
	command.Stderr = diagnostic
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
		instanceID: processEngineInstanceSequence.Add(1),
		command:    command,
		stdin:      stdin,
		stdout:     bufio.NewReaderSize(stdout, 64<<10),
		diagnostic: diagnostic,
	}
	return engine.process, nil
}

func (engine *ProcessEngine) resetProcess() error {
	return engine.closeProcess(false)
}

func (engine *ProcessEngine) closeProcess(graceful bool) error {
	process := engine.process
	engine.process = nil
	if process == nil {
		return nil
	}
	_ = process.stdin.Close()
	if !graceful && process.command.Process != nil {
		_ = process.command.Process.Kill()
	}
	waited := make(chan error, 1)
	go func() {
		waited <- process.command.Wait()
	}()
	var waitErr error
	if graceful {
		select {
		case waitErr = <-waited:
		case <-time.After(10 * time.Second):
			if process.command.Process != nil {
				_ = process.command.Process.Kill()
			}
			waitErr = <-waited
		}
	} else {
		waitErr = <-waited
	}
	process.diagnostic.Flush()
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		return nil
	}
	return waitErr
}

var (
	engineURLDiagnostic         = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>]+`)
	engineSecretFieldDiagnostic = regexp.MustCompile(
		`(?i)\b(authorization|cookie|token|secret|password|api[_-]?key)\s*[:=].*$`,
	)
	engineSecretValueDiagnostic = regexp.MustCompile(
		`(?i)\b(?:sk-[a-z0-9_-]{8,}|ol_(?:agent|user)_[a-z0-9_-]{8,})\b`,
	)
)

type engineDiagnosticWriter struct {
	target    io.Writer
	mu        sync.Mutex
	pending   []byte
	remaining int
}

func newEngineDiagnosticWriter(target io.Writer) *engineDiagnosticWriter {
	return &engineDiagnosticWriter{target: target, remaining: maxEngineLogBytes}
}

func (writer *engineDiagnosticWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	originalLength := len(value)
	for len(value) > 0 && writer.remaining > 0 {
		index := bytes.IndexByte(value, '\n')
		if index < 0 {
			writer.appendPending(value)
			break
		}
		writer.appendPending(value[:index])
		writer.flushLocked()
		value = value[index+1:]
	}
	return originalLength, nil
}

func (writer *engineDiagnosticWriter) Flush() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.flushLocked()
}

func (writer *engineDiagnosticWriter) appendPending(value []byte) {
	available := maxEngineLogLine - len(writer.pending)
	if available <= 0 {
		return
	}
	if len(value) > available {
		value = value[:available]
	}
	writer.pending = append(writer.pending, value...)
}

func (writer *engineDiagnosticWriter) flushLocked() {
	if len(writer.pending) == 0 || writer.remaining <= 0 {
		writer.pending = writer.pending[:0]
		return
	}
	line := strings.Map(func(character rune) rune {
		if character == '\t' || character >= 0x20 {
			return character
		}
		return ' '
	}, string(writer.pending))
	line = engineURLDiagnostic.ReplaceAllString(line, "[url]")
	line = engineSecretFieldDiagnostic.ReplaceAllString(line, "$1=[redacted]")
	line = engineSecretValueDiagnostic.ReplaceAllString(line, "[redacted]")
	raw := []byte("browser-engine: " + line + "\n")
	if len(raw) > writer.remaining {
		raw = raw[:writer.remaining]
	}
	_, _ = writer.target.Write(raw)
	writer.remaining -= len(raw)
	writer.pending = writer.pending[:0]
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

func decodeEngineViewerResponse(
	raw []byte,
	actionID string,
) (engineViewerResponse, *browserprotocol.Failure) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response engineViewerResponse
	if err := decoder.Decode(&response); err != nil {
		return engineViewerResponse{}, invalidEngineOutput()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return engineViewerResponse{}, invalidEngineOutput()
	}
	if response.ContractID != engineViewerContractID ||
		response.ActionID != actionID {
		return engineViewerResponse{}, invalidEngineOutput()
	}
	switch response.Status {
	case "ok":
		if response.Error != nil {
			return engineViewerResponse{}, invalidEngineOutput()
		}
	case "error":
		if response.Error == nil || response.Frame != nil {
			return engineViewerResponse{}, invalidEngineOutput()
		}
	default:
		return engineViewerResponse{}, invalidEngineOutput()
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
		"HOME":                                                 true,
		"LANG":                                                 true,
		"LC_ALL":                                               true,
		"NO_PROXY":                                             true,
		"OPENLINKER_BROWSER_EGRESS_PROXY":                      true,
		"OPENLINKER_BROWSER_PROFILE_DIR":                       true,
		"OPENLINKER_BROWSER_ENGINE":                            true,
		"OPENLINKER_BROWSER_DISTRIBUTION":                      true,
		"OPENLINKER_BROWSER_VERSION":                           true,
		"OPENLINKER_BROWSER_LOCALE":                            true,
		"OPENLINKER_BROWSER_TIMEZONE":                          true,
		"OPENLINKER_BROWSER_FONT_CONTRACT_VERSION":             true,
		"OPENLINKER_BROWSER_FONT_MANIFEST_SHA256":              true,
		"OPENLINKER_BROWSER_PROFILE_GENERATION":                true,
		"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE":     true,
		"OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE": true,
		"PLAYWRIGHT_BROWSERS_PATH":                             true,
		"TMPDIR":                                               true,
		"TZ":                                                   true,
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
	if failure == nil ||
		browserprotocol.ValidateFailure(failure) != nil ||
		failure.BlockedClickNavigationAttemptsRemaining != nil ||
		failure.BlockedClickRunAttemptsRemaining != nil ||
		!allowedEngineErrorCode(failure.Code) {
		return invalidEngineOutput()
	}
	if failure.TargetCategory != "" &&
		failure.Code != browserprotocol.ErrorHighImpactActionBlocked {
		return invalidEngineOutput()
	}
	normalized := browserprotocol.NewFailure(failure.Code, failure.Message, failure.Recoverable)
	if failure.ActionIndex != nil {
		index := *failure.ActionIndex
		normalized.ActionIndex = &index
	}
	normalized.TargetCategory = failure.TargetCategory
	normalized.PageStateID = failure.PageStateID
	normalized.NavigationGeneration = failure.NavigationGeneration
	normalized.SiteOutcome = failure.SiteOutcome
	if failure.RetryAfterMS != nil {
		retryAfterMS := *failure.RetryAfterMS
		normalized.RetryAfterMS = &retryAfterMS
	}
	normalized.ClassifierRulesVersion = failure.ClassifierRulesVersion
	if failure.ConsecutiveAccessDenials != nil {
		denials := *failure.ConsecutiveAccessDenials
		normalized.ConsecutiveAccessDenials = &denials
	}
	normalized.OriginBlockedForAttachment = failure.OriginBlockedForAttachment
	normalized.ChallengeReleaseUnavailable = failure.ChallengeReleaseUnavailable
	if failure.RetrySameAction != nil {
		value := *failure.RetrySameAction
		normalized.RetrySameAction = &value
	}
	if failure.AttachmentUsable != nil {
		value := *failure.AttachmentUsable
		normalized.AttachmentUsable = &value
	}
	if failure.FreshObservationRequired != nil {
		value := *failure.FreshObservationRequired
		normalized.FreshObservationRequired = &value
	}
	normalized.MutationOutcomeReason = failure.MutationOutcomeReason
	normalized.MutationRequestsObserved = failure.MutationRequestsObserved
	if failure.AttemptedUnits != nil {
		value := *failure.AttemptedUnits
		normalized.AttemptedUnits = &value
	}
	if failure.UndispatchedUnits != nil {
		value := *failure.UndispatchedUnits
		normalized.UndispatchedUnits = &value
	}
	if failure.CompletedActions != nil {
		value := *failure.CompletedActions
		normalized.CompletedActions = &value
	}
	normalized.ObservedOrigin = failure.ObservedOrigin
	if failure.BrowserMutationOrigins != nil {
		normalized.BrowserMutationOrigins = append(
			[]string{},
			failure.BrowserMutationOrigins...,
		)
	}
	return normalized
}

func allowedEngineErrorCode(code browserprotocol.ErrorCode) bool {
	switch code {
	case browserprotocol.ErrorOutputTooLarge,
		browserprotocol.ErrorRuntimeUnavailable,
		browserprotocol.ErrorEngineUnavailable,
		browserprotocol.ErrorEgressUnavailable,
		browserprotocol.ErrorTargetBlocked,
		browserprotocol.ErrorProfileLocked,
		browserprotocol.ErrorProfileCorrupt,
		browserprotocol.ErrorUserActionRequired,
		browserprotocol.ErrorHighImpactActionBlocked,
		browserprotocol.ErrorMutationOriginBlocked,
		browserprotocol.ErrorMutationOutcomeUnknown,
		browserprotocol.ErrorAccessDenied,
		browserprotocol.ErrorRateLimited,
		browserprotocol.ErrorChallengeSuspected,
		browserprotocol.ErrorChallengeRequired,
		browserprotocol.ErrorOriginRateLimited,
		browserprotocol.ErrorActionLimitExceeded,
		browserprotocol.ErrorActionRejected,
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
