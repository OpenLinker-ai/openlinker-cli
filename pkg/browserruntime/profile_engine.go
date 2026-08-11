//go:build !windows

package browserruntime

import (
	"context"
	"crypto/rand"
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

type ProfileEngineOptions struct {
	Process     ProcessEngineOptions
	Environment ProfileEnvironment
	StoreRoot   string
	WorkRoot    string
	RootKeyFile string
}

type managedBrowserEngine interface {
	Engine
	Close() error
}

type activeBrowserProfile struct {
	identity                   browserprofile.Identity
	session                    string
	owner                      browserprotocol.Identity
	exists                     bool
	process                    managedBrowserEngine
	upgradePending             bool
	preflightValidated         bool
	environmentAdoptionPending bool
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
	if err := options.Environment.Validate(); err != nil {
		return nil, err
	}
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
	store, err := browserprofile.NewStore(options.StoreRoot)
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
	if failure := action.ValidateForPolicy(identity.BrowserInteractionPolicy); failure != nil {
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
	if action.Kind == browserprotocol.ActionClose &&
		(engine.active == nil || !sameActiveAttachment(engine.active.owner, identity)) {
		return closedAttachmentObservation(identity), nil
	}
	if failure := engine.activate(identity); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	switch action.Kind {
	case browserprotocol.ActionPreflight:
		process, failure := engine.ensureProcess()
		if failure != nil {
			return browserprotocol.Observation{}, engine.upgradeFailure(failure)
		}
		observation, failure := process.Execute(ctx, identity, action)
		if failure != nil {
			return browserprotocol.Observation{}, engine.upgradeFailure(failure)
		}
		if observation.Environment == nil ||
			*observation.Environment != engine.options.Environment.Evidence {
			upgradePending := engine.active != nil && engine.active.upgradePending
			_ = engine.discardActive()
			if upgradePending {
				return browserprotocol.Observation{}, browserprotocol.NewFailure(
					browserprotocol.ErrorProfileEngineUpgrade,
					"Browser upgrade preflight failed; the saved Profile checkpoint was preserved",
					false,
				)
			}
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorEngineUnavailable,
				"Browser engine environment did not match the locked image configuration",
				false,
			)
		}
		if engine.active != nil {
			engine.active.preflightValidated = true
		}
		if engine.active == nil || engine.active.exists {
			return observation, nil
		}
		// A new Profile is not durable until the first checkpoint. Persist it
		// before advertising ready, then reopen Chromium to prove that the
		// encrypted store, extraction path and live process all work together.
		if failure := engine.checkpointActive(true); failure != nil {
			return browserprotocol.Observation{}, failure
		}
		process, failure = engine.ensureProcess()
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return process.Execute(ctx, identity, action)
	case browserprotocol.ActionCheckpoint:
		process, failure := engine.ensureProcess()
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		observation, failure := process.Execute(ctx, identity, browserprotocol.Action{
			Kind:        browserprotocol.ActionCheckpoint,
			Observation: browserprotocol.ObservationNone,
		})
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		if failure := engine.checkpointActive(true); failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return observation, nil
	case browserprotocol.ActionClose:
		if engine.active != nil && engine.active.process != nil {
			if _, failure := engine.active.process.Execute(
				ctx,
				identity,
				browserprotocol.Action{
					Kind:        browserprotocol.ActionCheckpoint,
					Observation: browserprotocol.ObservationNone,
				},
			); failure != nil {
				return browserprotocol.Observation{}, failure
			}
		}
		if failure := engine.checkpointActive(false); failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return closedAttachmentObservation(identity), nil
	default:
		process, failure := engine.ensureProcess()
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return process.Execute(ctx, identity, action)
	}
}

func (engine *ProfileEngine) ExecuteViewer(
	ctx context.Context,
	identity browserprotocol.Identity,
	operation browserprotocol.ViewerOperation,
	input *browserprotocol.ViewerInput,
) (*browserprotocol.ViewerFrame, *browserprotocol.Failure) {
	if engine == nil {
		return nil, runtimeUnavailable("Browser Profile engine is nil")
	}
	if identity.Controller != browserprotocol.ControllerHuman {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorStaleControlEpoch,
			"browser viewer does not hold human control",
			false,
		)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed || engine.active == nil ||
		!sameBrowserSessionAttachment(engine.active.owner, identity) {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorViewerUnavailable,
			"browser viewer has no active Session attachment",
			true,
		)
	}
	if (operation == browserprotocol.ViewerOperationEnter &&
		identity.ControlEpoch <= engine.active.owner.ControlEpoch) ||
		(operation != browserprotocol.ViewerOperationEnter &&
			identity.ControlEpoch != engine.active.owner.ControlEpoch) {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorStaleControlEpoch,
			"browser viewer control epoch is stale",
			false,
		)
	}
	process, ok := engine.active.process.(ViewerEngine)
	if !ok {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorViewerUnavailable,
			"browser engine does not support human control",
			false,
		)
	}
	engine.active.owner = identity
	return process.ExecuteViewer(ctx, identity, operation, input)
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

func (engine *ProfileEngine) AbortActive() error {
	if engine == nil {
		return nil
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return nil
	}
	return engine.discardActive()
}

func (engine *ProfileEngine) AbortStartup() error {
	return engine.AbortActive()
}

func (engine *ProfileEngine) activate(identity browserprotocol.Identity) *browserprotocol.Failure {
	profileIdentity := browserprofile.Identity{
		AgentID:           identity.AgentID,
		PrincipalScopeID:  identity.PrincipalScopeID,
		ProfileSlot:       "default",
		ProfileGeneration: engine.options.Environment.ProfileGeneration,
	}
	session := identity.BrowserSessionID + ":" + fmt.Sprintf("%d", identity.SessionEpoch)
	if engine.active != nil &&
		engine.active.identity == profileIdentity &&
		engine.active.session == session {
		engine.active.owner = identity
		return nil
	}
	if engine.active != nil {
		if failure := engine.checkpointActive(false); failure != nil {
			return failure
		}
	}
	if _, err := engine.store.PruneInactive(
		time.Now().UTC().Add(-browserprotocol.BrowserStateRetention),
	); err != nil {
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
	upgradePending := false
	environmentAdoptionPending := false
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
		if err := validateBrowserProfileState(engine.workDirectory); err != nil {
			_ = archive.Close()
			quarantineErr := engine.store.QuarantineInvalidState(profileIdentity)
			_ = resetProfileWorkDirectory(engine.workDirectory)
			return profileFailure(errors.Join(
				browserprofile.ErrProfileCorrupt,
				quarantineErr,
			))
		}
		storedEnvironment, environmentErr := loadProfileEnvironment(engine.workDirectory)
		switch {
		case errors.Is(environmentErr, fs.ErrNotExist):
			// Legacy Profiles are bound to the current locked image
			// environment at their next successful checkpoint.
			environmentAdoptionPending = true
		case environmentErr != nil:
			_ = archive.Close()
			quarantineErr := engine.store.QuarantineInvalidState(profileIdentity)
			_ = resetProfileWorkDirectory(engine.workDirectory)
			return profileFailure(errors.Join(
				browserprofile.ErrProfileCorrupt,
				environmentErr,
				quarantineErr,
			))
		case !storedEnvironment.sameGenerationBinding(engine.options.Environment):
			_ = archive.Close()
			_ = resetProfileWorkDirectory(engine.workDirectory)
			return browserprotocol.NewFailure(
				browserprotocol.ErrorProfileEnvironmentMismatch,
				"Browser Profile environment changed; select a new Profile generation",
				false,
			)
		default:
			versionComparison, compareErr := compareBrowserVersions(
				engine.options.Environment.Evidence.BrowserVersion,
				storedEnvironment.Evidence.BrowserVersion,
			)
			if compareErr != nil {
				_ = archive.Close()
				_ = resetProfileWorkDirectory(engine.workDirectory)
				return profileFailure(compareErr)
			}
			if versionComparison < 0 {
				_ = archive.Close()
				_ = resetProfileWorkDirectory(engine.workDirectory)
				return browserprotocol.NewFailure(
					browserprotocol.ErrorProfileEngineDowngrade,
					"Saved Browser Profile uses a newer Browser; select a new Profile generation before rollback",
					false,
				)
			}
			upgradePending = versionComparison > 0
		}
	}
	if err := archive.Close(); err != nil {
		return profileRuntimeFailure(err)
	}
	engine.active = &activeBrowserProfile{
		identity:                   profileIdentity,
		session:                    session,
		owner:                      identity,
		exists:                     exists,
		upgradePending:             upgradePending,
		environmentAdoptionPending: environmentAdoptionPending,
	}
	return nil
}

func sameActiveAttachment(
	active browserprotocol.Identity,
	requested browserprotocol.Identity,
) bool {
	return active.RunID == requested.RunID &&
		active.AgentID == requested.AgentID &&
		active.PrincipalScopeID == requested.PrincipalScopeID &&
		active.BrowserSessionID == requested.BrowserSessionID &&
		active.SessionEpoch == requested.SessionEpoch &&
		active.AttachmentID == requested.AttachmentID &&
		active.ControlEpoch == requested.ControlEpoch
}

func sameBrowserSessionAttachment(
	active browserprotocol.Identity,
	requested browserprotocol.Identity,
) bool {
	return active.RunID == requested.RunID &&
		active.AgentID == requested.AgentID &&
		active.PrincipalScopeID == requested.PrincipalScopeID &&
		active.BrowserSessionID == requested.BrowserSessionID &&
		active.SessionEpoch == requested.SessionEpoch &&
		active.AttachmentID == requested.AttachmentID
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
	if active.upgradePending && !active.preflightValidated {
		if err := engine.discardActive(); err != nil {
			return profileRuntimeFailure(err)
		}
		return nil
	}
	if active.environmentAdoptionPending && !active.preflightValidated {
		_ = engine.discardActive()
		return browserprotocol.NewFailure(
			browserprotocol.ErrorEngineUnavailable,
			"Legacy Browser Profile requires locked environment preflight before checkpoint",
			false,
		)
	}
	if active.process != nil {
		if err := active.process.Close(); err != nil {
			return profileRuntimeFailure(err)
		}
		active.process = nil
	}
	if validationErr := validateBrowserProfileState(engine.workDirectory); validationErr != nil {
		var quarantineErr error
		if active.exists {
			quarantineErr = engine.store.QuarantineInvalidState(active.identity)
		}
		engine.active = nil
		_ = resetProfileWorkDirectory(engine.workDirectory)
		return profileFailure(errors.Join(
			browserprofile.ErrProfileCorrupt,
			validationErr,
			quarantineErr,
		))
	}
	if err := writeProfileEnvironment(
		engine.workDirectory,
		engine.options.Environment,
	); err != nil {
		return profileFailure(err)
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
	active.upgradePending = false
	active.preflightValidated = true
	active.environmentAdoptionPending = false
	if retain {
		return nil
	}
	engine.active = nil
	if err := resetProfileWorkDirectory(engine.workDirectory); err != nil {
		return profileRuntimeFailure(err)
	}
	return nil
}

func (engine *ProfileEngine) upgradeFailure(
	failure *browserprotocol.Failure,
) *browserprotocol.Failure {
	if engine.active == nil || !engine.active.upgradePending {
		return failure
	}
	_ = engine.discardActive()
	return browserprotocol.NewFailure(
		browserprotocol.ErrorProfileEngineUpgrade,
		"Browser upgrade preflight failed; the saved Profile checkpoint was preserved",
		false,
	)
}

func (engine *ProfileEngine) discardActive() error {
	active := engine.active
	engine.active = nil
	var closeErr error
	if active != nil && active.process != nil {
		closeErr = active.process.Close()
	}
	workErr := resetProfileWorkDirectory(engine.workDirectory)
	return errors.Join(closeErr, workErr)
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
		_ = os.Remove(path)
		clear(raw)
		return nil, errors.Join(writeErr, syncErr, closeErr)
	}
	directory, openErr := os.Open(filepath.Dir(path)) // #nosec G304 -- parent is the validated Profile key directory.
	if openErr != nil {
		clear(raw)
		return nil, openErr
	}
	directorySyncErr := directory.Sync()
	directoryCloseErr := directory.Close()
	if directorySyncErr != nil || directoryCloseErr != nil {
		clear(raw)
		return nil, errors.Join(directorySyncErr, directoryCloseErr)
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
