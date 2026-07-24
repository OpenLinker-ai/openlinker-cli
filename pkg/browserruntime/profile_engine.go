//go:build !windows

package browserruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprofile"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const browserProfileRootKeyGeneration = 1

const browserProfileRetention = 30 * 24 * time.Hour

type ProfileEngineOptions struct {
	Process     ProcessEngineOptions
	StoreRoot   string
	WorkRoot    string
	RootKeyFile string
}

type managedBrowserEngine interface {
	Engine
	Close() error
}

type activeBrowserProfile struct {
	identity browserprofile.Identity
	session  string
	exists   bool
	process  managedBrowserEngine
}

type ProfileEngine struct {
	options        ProfileEngineOptions
	store          *browserprofile.Store
	rootKey        *browserprofile.RootKey
	workDirectory  string
	processFactory func(ProcessEngineOptions) (managedBrowserEngine, error)
	mu             sync.Mutex
	active         *activeBrowserProfile
	closed         bool
}

func NewProfileEngine(options ProfileEngineOptions) (*ProfileEngine, error) {
	for label, value := range map[string]string{
		"Browser Profile store root": options.StoreRoot,
		"Browser Profile work root":  options.WorkRoot,
		"Browser Profile root key":   options.RootKeyFile,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return nil, fmt.Errorf("%s must be an absolute clean path", label)
		}
	}
	if err := ensurePrivateRuntimeDirectory(options.WorkRoot); err != nil {
		return nil, fmt.Errorf("prepare Browser Profile work root: %w", err)
	}
	workDirectory := filepath.Join(options.WorkRoot, "active")
	if err := resetProfileWorkDirectory(workDirectory); err != nil {
		return nil, err
	}
	rootKey, err := loadOrCreateProfileRootKey(options.RootKeyFile)
	if err != nil {
		return nil, err
	}
	store, err := browserprofile.NewStore(options.StoreRoot, browserprofile.NewProtector(nil))
	if err != nil {
		rootKey.Close()
		return nil, err
	}
	engine := &ProfileEngine{
		options:        options,
		store:          store,
		rootKey:        rootKey,
		workDirectory:  workDirectory,
		processFactory: newManagedProcessEngine,
	}
	return engine, nil
}

func (engine *ProfileEngine) Execute(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	if engine == nil {
		return browserprotocol.Observation{}, runtimeUnavailable("Browser Profile engine is nil")
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
		return browserprotocol.Observation{}, runtimeUnavailable("Browser Profile engine is closed")
	}
	if err := ctx.Err(); err != nil {
		return browserprotocol.Observation{}, contextFailure(err)
	}
	if failure := engine.activate(identity); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	switch action.Kind {
	case browserprotocol.ActionCheckpoint:
		process, failure := engine.ensureProcess()
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		observation, failure := process.Execute(ctx, identity, browserprotocol.Action{
			Kind: browserprotocol.ActionScreenshot,
		})
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		if failure := engine.checkpointActive(true); failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return observation, nil
	case browserprotocol.ActionClose:
		if failure := engine.checkpointActive(false); failure != nil {
			return browserprotocol.Observation{}, failure
		}
		digest := sha256.Sum256([]byte(identity.AttachmentID))
		return browserprotocol.Observation{
			PageStateID: "closed-" + hex.EncodeToString(digest[:16]),
		}, nil
	default:
		process, failure := engine.ensureProcess()
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return process.Execute(ctx, identity, action)
	}
}

func (engine *ProfileEngine) Close() error {
	if engine == nil {
		return nil
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return nil
	}
	engine.closed = true
	var checkpointErr error
	if engine.active != nil {
		if failure := engine.checkpointActive(false); failure != nil {
			checkpointErr = failure
		}
	}
	storeErr := engine.store.Close()
	engine.rootKey.Close()
	workErr := os.RemoveAll(engine.workDirectory)
	return errors.Join(checkpointErr, storeErr, workErr)
}

func (engine *ProfileEngine) activate(identity browserprotocol.Identity) *browserprotocol.Failure {
	profileIdentity := browserprofile.Identity{
		AgentID:           identity.AgentID,
		PrincipalScopeID:  identity.PrincipalScopeID,
		ProfileSlot:       "default",
		ProfileGeneration: 1,
	}
	session := identity.BrowserSessionID + ":" + fmt.Sprintf("%d", identity.SessionEpoch)
	if engine.active != nil &&
		engine.active.identity == profileIdentity &&
		engine.active.session == session {
		return nil
	}
	if engine.active != nil {
		if failure := engine.checkpointActive(false); failure != nil {
			return failure
		}
	}
	if _, err := engine.store.PruneInactive(time.Now().UTC().Add(-browserProfileRetention)); err != nil {
		return profileRuntimeFailure(err)
	}
	if err := resetProfileWorkDirectory(engine.workDirectory); err != nil {
		return profileRuntimeFailure(err)
	}
	archive, err := os.CreateTemp(engine.options.WorkRoot, ".profile-load-")
	if err != nil {
		return profileRuntimeFailure(err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	if err := archive.Chmod(0o600); err != nil {
		_ = archive.Close()
		return profileRuntimeFailure(err)
	}
	roots := map[uint64]*browserprofile.RootKey{
		engine.rootKey.Generation(): engine.rootKey,
	}
	loadErr := engine.store.Load(profileIdentity, roots, archive)
	exists := true
	switch {
	case errors.Is(loadErr, browserprofile.ErrProfileNotFound):
		exists = false
	case loadErr != nil:
		_ = archive.Close()
		return profileFailure(loadErr)
	default:
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			_ = archive.Close()
			return profileRuntimeFailure(err)
		}
		if err := extractProfileArchive(archive, engine.workDirectory); err != nil {
			_ = archive.Close()
			return profileFailure(err)
		}
	}
	if err := archive.Close(); err != nil {
		return profileRuntimeFailure(err)
	}
	engine.active = &activeBrowserProfile{
		identity: profileIdentity,
		session:  session,
		exists:   exists,
	}
	return nil
}

func (engine *ProfileEngine) ensureProcess() (managedBrowserEngine, *browserprotocol.Failure) {
	if engine.active == nil {
		return nil, runtimeUnavailable("Browser Profile is not active")
	}
	if engine.active.process != nil {
		return engine.active.process, nil
	}
	options := engine.options.Process
	options.Environment = withBrowserProfileDirectory(options.Environment, engine.workDirectory)
	process, err := engine.processFactory(options)
	if err != nil {
		return nil, profileRuntimeFailure(err)
	}
	engine.active.process = process
	return process, nil
}

func (engine *ProfileEngine) checkpointActive(retain bool) *browserprotocol.Failure {
	active := engine.active
	if active == nil {
		return nil
	}
	if active.process != nil {
		if err := active.process.Close(); err != nil {
			return profileRuntimeFailure(err)
		}
		active.process = nil
	}
	archive, err := os.CreateTemp(engine.options.WorkRoot, ".profile-checkpoint-")
	if err != nil {
		return profileRuntimeFailure(err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	if err := archive.Chmod(0o600); err != nil {
		_ = archive.Close()
		return profileRuntimeFailure(err)
	}
	if err := writeProfileArchive(engine.workDirectory, archive); err != nil {
		_ = archive.Close()
		return profileFailure(err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		return profileRuntimeFailure(err)
	}
	roots := map[uint64]*browserprofile.RootKey{
		engine.rootKey.Generation(): engine.rootKey,
	}
	if active.exists {
		err = engine.store.Checkpoint(active.identity, roots, archive)
	} else {
		err = engine.store.Create(active.identity, engine.rootKey, archive)
	}
	closeErr := archive.Close()
	if err != nil {
		return profileFailure(err)
	}
	if closeErr != nil {
		return profileRuntimeFailure(closeErr)
	}
	active.exists = true
	if retain {
		return nil
	}
	engine.active = nil
	if err := resetProfileWorkDirectory(engine.workDirectory); err != nil {
		return profileRuntimeFailure(err)
	}
	return nil
}

func newManagedProcessEngine(options ProcessEngineOptions) (managedBrowserEngine, error) {
	return NewProcessEngine(options)
}

func withBrowserProfileDirectory(environment []string, directory string) []string {
	result := make([]string, 0, len(environment)+1)
	replaced := false
	for _, item := range environment {
		key, _, found := stringsCut(item)
		if found && key == "OPENLINKER_BROWSER_PROFILE_DIR" {
			result = append(result, key+"="+directory)
			replaced = true
			continue
		}
		result = append(result, item)
	}
	if !replaced {
		result = append(result, "OPENLINKER_BROWSER_PROFILE_DIR="+directory)
	}
	return result
}

func stringsCut(value string) (string, string, bool) {
	for index := range value {
		if value[index] == '=' {
			return value[:index], value[index+1:], true
		}
	}
	return value, "", false
}

func resetProfileWorkDirectory(path string) error {
	parent := filepath.Dir(path)
	if err := ensurePrivateRuntimeDirectory(parent); err != nil {
		return err
	}
	if filepath.Base(path) != "active" {
		return errors.New("Browser Profile work directory target is invalid")
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("clear Browser Profile work directory: %w", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("create Browser Profile work directory: %w", err)
	}
	return nil
}

func ensurePrivateRuntimeDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Browser Profile directory is invalid")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o700)
	}
	return nil
}

func loadOrCreateProfileRootKey(path string) (*browserprofile.RootKey, error) {
	if err := ensurePrivateRuntimeDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	raw, err := readProfileRootKey(path)
	if err == nil {
		key, keyErr := browserprofile.NewRootKey(browserProfileRootKeyGeneration, raw)
		clear(raw)
		return key, keyErr
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	raw = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return nil, fmt.Errorf("generate Browser Profile root key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- fixed validated Browser-only state path.
	if errors.Is(err, fs.ErrExist) {
		clear(raw)
		existing, readErr := readProfileRootKey(path)
		if readErr != nil {
			return nil, readErr
		}
		key, keyErr := browserprofile.NewRootKey(browserProfileRootKeyGeneration, existing)
		clear(existing)
		return key, keyErr
	}
	if err != nil {
		clear(raw)
		return nil, fmt.Errorf("create Browser Profile root key: %w", err)
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		clear(raw)
		return nil, errors.Join(writeErr, syncErr, closeErr)
	}
	key, keyErr := browserprofile.NewRootKey(browserProfileRootKeyGeneration, raw)
	clear(raw)
	return key, keyErr
}

func readProfileRootKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() ||
		!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || info.Size() != 32 {
		return nil, errors.New("Browser Profile root key file is invalid")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed validated Browser-only state path.
	if err != nil || len(raw) != 32 {
		return nil, errors.New("read Browser Profile root key")
	}
	return raw, nil
}

func profileFailure(err error) *browserprotocol.Failure {
	switch {
	case errors.Is(err, browserprofile.ErrProfileStoreLocked):
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProfileLocked,
			"Browser Profile store is locked",
			true,
		)
	case errors.Is(err, browserprofile.ErrProfileQuarantined),
		errors.Is(err, browserprofile.ErrProfileCorrupt),
		errors.Is(err, browserprofile.ErrIdentityMismatch),
		errors.Is(err, browserprofile.ErrKeyGeneration),
		errors.Is(err, browserprofile.ErrRootKeyUnavailable):
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProfileCorrupt,
			"Browser Profile failed authentication and was not loaded",
			false,
		)
	default:
		return profileRuntimeFailure(err)
	}
}

func profileRuntimeFailure(_ error) *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"Browser Profile operation failed",
		true,
	)
}
