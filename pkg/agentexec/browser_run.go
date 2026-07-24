package agentexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserclient"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserplugin"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const (
	browserSessionStateVersion = 1
	maxBrowserSessionStateSize = 16 << 10
)

type browserExecutionProvider struct {
	base   Provider
	config ProviderConfig
}

type browserSessionState struct {
	Version             int    `json:"version"`
	SessionKeyHash      string `json:"session_key_hash"`
	BrowserSessionID    string `json:"browser_session_id"`
	SessionEpoch        uint64 `json:"session_epoch"`
	ControlEpoch        uint64 `json:"control_epoch"`
	RuntimeSessionID    string `json:"runtime_session_id"`
	RuntimeSessionEpoch int64  `json:"runtime_session_epoch"`
	RuntimeAttachmentID string `json:"runtime_attachment_id"`
	UpdatedAt           string `json:"updated_at"`
}

type browserRunLease struct {
	config       ProviderConfig
	statePath    string
	activePath   string
	runPath      string
	state        browserSessionState
	identity     browserprotocol.Identity
	expiresAt    time.Time
	releaseScope func()
	mu           sync.Mutex
	runtimeUsed  bool
	closed       bool
}

var browserRootLocks = struct {
	sync.Mutex
	entries map[string]*sessionLockEntry
}{entries: map[string]*sessionLockEntry{}}

func newBrowserExecutionProvider(base Provider, config ProviderConfig) (Provider, error) {
	for label, value := range map[string]string{
		"Browser plugin binary":   config.BrowserPluginBin,
		"Browser socket":          config.BrowserSocket,
		"Browser credential file": config.BrowserCredentialFile,
		"Browser lease root":      config.BrowserLeaseRoot,
		"Browser broker root":     config.BrowserBrokerRoot,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required for the browser execution profile", label)
		}
	}
	if !filepath.IsAbs(config.BrowserSocket) ||
		!filepath.IsAbs(config.BrowserCredentialFile) ||
		!filepath.IsAbs(config.BrowserLeaseRoot) ||
		!filepath.IsAbs(config.BrowserBrokerRoot) {
		return nil, errors.New("Browser execution paths must be absolute")
	}
	return &browserExecutionProvider{base: base, config: config}, nil
}

func (provider *browserExecutionProvider) Run(
	ctx context.Context,
	run RunContext,
) (result openlinker.RuntimeResult, resultErr error) {
	lease, err := acquireBrowserRunLease(provider.config, run)
	if err != nil {
		return openlinker.RuntimeResult{}, err
	}
	broker, err := startBrowserToolBroker(
		ctx,
		provider.config.Provider,
		provider.config.BrowserBrokerRoot,
		lease,
	)
	if err != nil {
		_ = lease.Close()
		return openlinker.RuntimeResult{}, err
	}
	run.Browser = &BrowserRunContext{
		PluginBin:  provider.config.BrowserPluginBin,
		ToolSocket: broker.SocketPath(),
		Rotate:     lease.Rotate,
	}
	emitBrowserLifecycle(run.Emit, "ready", "")
	status := "failed"
	defer func() {
		brokerErr := broker.Close()
		runtimeErr := lease.closeBrowserRuntime()
		leaseErr := lease.Close()
		if cleanupErr := errors.Join(brokerErr, runtimeErr, leaseErr); cleanupErr != nil {
			status = "failed"
			if resultErr == nil {
				result = openlinker.RuntimeResult{}
				resultErr = fmt.Errorf("close Browser execution attachment: %w", cleanupErr)
			}
		}
		emitBrowserLifecycle(run.Emit, "closed", status)
	}()
	result, resultErr = provider.base.Run(ctx, run)
	if resultErr == nil {
		status = "success"
	}
	if output, ok := result.Output.(map[string]any); ok {
		copied := make(map[string]any, len(output)+2)
		for key, value := range output {
			copied[key] = value
		}
		copied["browser_execution_profile"] = "isolated"
		copied["browser_tool"] = "browser_session"
		result.Output = copied
	}
	return result, resultErr
}

func emitBrowserLifecycle(emit func(string, any) error, phase, status string) {
	if emit == nil {
		return
	}
	payload := map[string]any{
		"phase":             phase,
		"execution_profile": "browser",
		"runtime":           "isolated",
	}
	if status != "" {
		payload["status"] = status
	}
	_ = emit("run.browser.lifecycle", payload)
}

func (lease *browserRunLease) browserClient() (browserplugin.Executor, error) {
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return nil, errors.New("Browser lease is closed")
	}
	lease.runtimeUsed = true
	lease.mu.Unlock()
	return browserclient.New(browserclient.Config{
		SocketPath:     lease.config.BrowserSocket,
		CredentialFile: lease.config.BrowserCredentialFile,
		LeaseFile:      lease.runPath,
	})
}

func (lease *browserRunLease) closeBrowserRuntime() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	used := lease.runtimeUsed
	closed := lease.closed
	lease.mu.Unlock()
	if !used || closed {
		return nil
	}
	client, err := browserclient.New(browserclient.Config{
		SocketPath:     lease.config.BrowserSocket,
		CredentialFile: lease.config.BrowserCredentialFile,
		LeaseFile:      lease.runPath,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, failure := client.Execute(ctx, browserprotocol.Action{
		Kind: browserprotocol.ActionClose,
	}); failure != nil {
		return failure
	}
	return nil
}

func acquireBrowserRunLease(
	config ProviderConfig,
	run RunContext,
) (*browserRunLease, error) {
	if run.Authority == nil {
		return nil, errors.New("Browser execution requires Core-owned Runtime authority")
	}
	if run.Conversation != nil && run.Conversation.Source != "core" {
		return nil, errors.New("Browser execution rejected untrusted conversation identity")
	}
	sessionKey := conversationSessionKey(run)
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(run.RunID)
	}
	if sessionKey == "" {
		return nil, errors.New("Browser execution requires a conversation or Run identity")
	}
	releaseScope := lockBrowserRoot(config.BrowserLeaseRoot)
	keepLock := false
	defer func() {
		if !keepLock {
			releaseScope()
		}
	}()
	for _, dir := range []string{
		config.BrowserLeaseRoot,
		filepath.Join(config.BrowserLeaseRoot, "sessions"),
		filepath.Join(config.BrowserLeaseRoot, "runs"),
	} {
		if err := ensurePrivateBrowserDirectory(dir); err != nil {
			return nil, err
		}
	}
	sessionDigest := sha256.Sum256([]byte(
		run.AgentID + "\x00" +
			run.Authority.PrincipalScopeID + "\x00" +
			sessionKey,
	))
	sessionKeyHash := hex.EncodeToString(sessionDigest[:])
	statePath := filepath.Join(
		config.BrowserLeaseRoot,
		"sessions",
		sessionKeyHash+".json",
	)
	state, err := readBrowserSessionState(statePath)
	if err != nil {
		return nil, err
	}
	if state.Version == 0 {
		browserSessionID, idErr := newBrowserUUID()
		if idErr != nil {
			return nil, idErr
		}
		state = browserSessionState{
			Version:          browserSessionStateVersion,
			SessionKeyHash:   sessionKeyHash,
			BrowserSessionID: browserSessionID,
			SessionEpoch:     1,
		}
	} else if state.SessionKeyHash != sessionKeyHash {
		return nil, errors.New("Browser session state identity does not match")
	}
	authority := run.Authority
	if state.RuntimeSessionID != "" &&
		(state.RuntimeSessionID != authority.RuntimeSessionID ||
			state.RuntimeSessionEpoch != authority.RuntimeSessionEpoch ||
			state.RuntimeAttachmentID != authority.RuntimeAttachmentID) {
		state.SessionEpoch++
	}
	state.RuntimeSessionID = authority.RuntimeSessionID
	state.RuntimeSessionEpoch = authority.RuntimeSessionEpoch
	state.RuntimeAttachmentID = authority.RuntimeAttachmentID
	state.ControlEpoch++
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	attachmentID, err := newBrowserUUID()
	if err != nil {
		return nil, err
	}
	identity := browserprotocol.Identity{
		RunID:            run.RunID,
		AgentID:          run.AgentID,
		PrincipalScopeID: authority.PrincipalScopeID,
		BrowserSessionID: state.BrowserSessionID,
		SessionEpoch:     state.SessionEpoch,
		AttachmentID:     attachmentID,
		ControlEpoch:     state.ControlEpoch,
	}
	if failure := identity.Validate(); failure != nil {
		return nil, failure
	}
	expiresAt, err := browserLeaseExpiry(config, run)
	if err != nil {
		return nil, err
	}
	lease := &browserRunLease{
		config:     config,
		statePath:  statePath,
		activePath: filepath.Join(config.BrowserLeaseRoot, "active-lease.json"),
		runPath: filepath.Join(
			config.BrowserLeaseRoot,
			"runs",
			run.RunID+".json",
		),
		state:        state,
		identity:     identity,
		expiresAt:    expiresAt,
		releaseScope: releaseScope,
	}
	if err := lease.persist(); err != nil {
		return nil, err
	}
	keepLock = true
	return lease, nil
}

func browserLeaseExpiry(config ProviderConfig, run RunContext) (time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(config.Timeout)
	if config.Timeout <= 0 {
		expiresAt = now.Add(30 * time.Minute)
	}
	for _, deadline := range []time.Time{run.AttemptDeadlineAt, run.RunDeadlineAt} {
		if !deadline.IsZero() && deadline.Before(expiresAt) {
			expiresAt = deadline.UTC()
		}
	}
	if !expiresAt.After(now) {
		return time.Time{}, errors.New("Browser execution deadline has elapsed")
	}
	return expiresAt, nil
}

func (lease *browserRunLease) Rotate() error {
	if lease == nil {
		return errors.New("Browser lease is unavailable")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return errors.New("Browser lease is closed")
	}
	attachmentID, err := newBrowserUUID()
	if err != nil {
		return err
	}
	lease.state.SessionEpoch++
	lease.state.ControlEpoch++
	lease.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	lease.identity.SessionEpoch = lease.state.SessionEpoch
	lease.identity.ControlEpoch = lease.state.ControlEpoch
	lease.identity.AttachmentID = attachmentID
	if err := lease.persist(); err != nil {
		return err
	}
	return nil
}

func (lease *browserRunLease) persist() error {
	if err := writePrivateBrowserJSON(lease.statePath, lease.state); err != nil {
		return fmt.Errorf("persist Browser session state: %w", err)
	}
	envelope := browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  lease.expiresAt,
		Identity:   lease.identity,
	}
	if err := writePrivateBrowserJSON(lease.activePath, envelope); err != nil {
		return fmt.Errorf("activate Browser lease: %w", err)
	}
	if err := writePrivateBrowserJSON(lease.runPath, envelope); err != nil {
		lease.removeActiveIfCurrent()
		return fmt.Errorf("persist per-Run Browser lease: %w", err)
	}
	return nil
}

func (lease *browserRunLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return nil
	}
	lease.closed = true
	runErr := removePrivateBrowserFile(lease.runPath)
	activeErr := lease.removeActiveIfCurrent()
	if lease.releaseScope != nil {
		lease.releaseScope()
		lease.releaseScope = nil
	}
	if runErr != nil {
		return runErr
	}
	return activeErr
}

func (lease *browserRunLease) removeActiveIfCurrent() error {
	current, err := readBrowserLease(lease.activePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Identity != lease.identity {
		return nil
	}
	return removePrivateBrowserFile(lease.activePath)
}

func readBrowserSessionState(path string) (browserSessionState, error) {
	raw, err := readPrivateBrowserFile(path, maxBrowserSessionStateSize)
	if errors.Is(err, os.ErrNotExist) {
		return browserSessionState{}, nil
	}
	if err != nil {
		return browserSessionState{}, err
	}
	var state browserSessionState
	if err := decodeStrictBrowserJSON(raw, &state); err != nil ||
		state.Version != browserSessionStateVersion ||
		state.SessionKeyHash == "" ||
		state.BrowserSessionID == "" ||
		state.SessionEpoch == 0 {
		return browserSessionState{}, errors.New("Browser session state is invalid")
	}
	return state, nil
}

func readBrowserLease(path string) (browserclient.Lease, error) {
	raw, err := readPrivateBrowserFile(path, maxBrowserSessionStateSize)
	if err != nil {
		return browserclient.Lease{}, err
	}
	var lease browserclient.Lease
	if err := decodeStrictBrowserJSON(raw, &lease); err != nil {
		return browserclient.Lease{}, errors.New("Browser lease is invalid")
	}
	return lease, nil
}

func readPrivateBrowserFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		!sessionFileOwnedByCurrentUser(info) ||
		info.Size() <= 0 ||
		info.Size() > limit {
		return nil, errors.New("Browser state file must be an owner-only regular non-symlink file")
	}
	file, err := os.Open(path) // #nosec G304 -- private state path is operator-controlled and validated.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("Browser state file exceeds its size limit")
	}
	return raw, nil
}

func writePrivateBrowserJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	if err := ensurePrivateBrowserDirectory(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 ||
			!info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 ||
			!sessionFileOwnedByCurrentUser(info) {
			return errors.New("Browser state destination is not an owner-only regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".browser-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func ensurePrivateBrowserDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm()&0o077 != 0 ||
		!sessionFileOwnedByCurrentUser(info) {
		return errors.New("Browser state directory must be owner-only and not a symlink")
	}
	return nil
}

func removePrivateBrowserFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		!sessionFileOwnedByCurrentUser(info) {
		return errors.New("refusing to remove an unowned Browser state path")
	}
	return os.Remove(path)
}

func decodeStrictBrowserJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing Browser JSON value")
		}
		return err
	}
	return nil
}

func newBrowserUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	), nil
}

func lockBrowserRoot(root string) func() {
	key := filepath.Clean(root)
	browserRootLocks.Lock()
	entry := browserRootLocks.entries[key]
	if entry == nil {
		entry = &sessionLockEntry{}
		browserRootLocks.entries[key] = entry
	}
	entry.refs++
	browserRootLocks.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		browserRootLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(browserRootLocks.entries, key)
		}
		browserRootLocks.Unlock()
	}
}
