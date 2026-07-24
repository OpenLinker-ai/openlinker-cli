//go:build !windows

package browserruntime

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

type fixtureProfileProcess struct {
	directory string
}

func (process *fixtureProfileProcess) Execute(
	_ context.Context,
	_ browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	marker := filepath.Join(process.directory, "Default", "Cookies.fixture")
	if action.Kind == browserprotocol.ActionNavigate {
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			return browserprotocol.Observation{}, profileRuntimeFailure(err)
		}
		if err := os.WriteFile(marker, []byte("persistent-login-state"), 0o600); err != nil {
			return browserprotocol.Observation{}, profileRuntimeFailure(err)
		}
	}
	return browserprotocol.Observation{PageStateID: "fixture-page-state"}, nil
}

func (*fixtureProfileProcess) Close() error {
	return nil
}

func TestProfileEngineEncryptsCheckpointAndIsolatesPrincipal(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	work := t.TempDir()
	engine := newFixtureProfileEngine(t, state, work)
	identityA := profileEngineIdentity("principal-a")
	identityB := profileEngineIdentity("principal-b")
	loadedA := false
	engine.processFactory = fixtureProfileFactory(&loadedA)

	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://example.com"},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionCheckpoint},
	); failure != nil {
		t.Fatal(failure)
	}
	if loadedA {
		t.Fatal("first Browser Profile process unexpectedly loaded an existing marker")
	}
	if raw, err := os.ReadFile(filepath.Join(work, "active", "Default", "Cookies.fixture")); err != nil ||
		string(raw) != "persistent-login-state" {
		t.Fatalf("active Browser Profile marker = %q, %v", raw, err)
	}
	assertPersistentProfileDoesNotContain(t, state, "persistent-login-state")

	loadedAfterCheckpoint := false
	engine.processFactory = fixtureProfileFactory(&loadedAfterCheckpoint)
	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if !loadedAfterCheckpoint {
		t.Fatal("same Browser Session did not retain its decrypted Profile after checkpoint")
	}
	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}

	loadedOtherPrincipal := false
	engine.processFactory = fixtureProfileFactory(&loadedOtherPrincipal)
	if _, failure := engine.Execute(
		context.Background(),
		identityB,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if loadedOtherPrincipal {
		t.Fatal("different principal loaded another principal's Browser Profile")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newFixtureProfileEngine(t, state, t.TempDir())
	defer restarted.Close()
	loadedAfterRestart := false
	restarted.processFactory = fixtureProfileFactory(&loadedAfterRestart)
	if _, failure := restarted.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if !loadedAfterRestart {
		t.Fatal("encrypted Browser Profile did not survive a Runtime restart")
	}
}

func TestProfileEngineQuarantinesCorruptEncryptedProfile(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(state, func(path string, entry os.DirEntry, _ error) error {
			if entry == nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	engine := newFixtureProfileEngine(t, state, t.TempDir())
	loaded := false
	engine.processFactory = fixtureProfileFactory(&loaded)
	identity := profileEngineIdentity("principal-corrupt")
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://example.com"},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	payloads, err := filepath.Glob(filepath.Join(state, "encrypted", "profiles", "*", "checkpoints", "*", "payload.bin"))
	if err != nil || len(payloads) != 1 {
		t.Fatalf("encrypted payloads = %#v, %v", payloads, err)
	}
	raw, err := os.ReadFile(payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(payloads[0], raw, 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := newFixtureProfileEngine(t, state, t.TempDir())
	defer restarted.Close()
	restarted.processFactory = fixtureProfileFactory(new(bool))
	if _, failure := restarted.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure == nil || failure.Code != browserprotocol.ErrorProfileCorrupt {
		t.Fatalf("corrupt Browser Profile failure = %#v", failure)
	}
	quarantine, err := os.ReadDir(filepath.Join(state, "encrypted", "quarantine"))
	if err != nil || len(quarantine) != 1 {
		t.Fatalf("Browser Profile quarantine = %#v, %v", quarantine, err)
	}
}

func TestProfileArchiveRejectsTraversal(t *testing.T) {
	t.Parallel()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{
		Name:     "../outside",
		Typeflag: tar.TypeReg,
		Mode:     0o600,
		Size:     1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "profile")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := extractProfileArchive(bytes.NewReader(raw.Bytes()), root); err == nil {
		t.Fatal("traversing Browser Profile archive was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !os.IsNotExist(err) {
		t.Fatalf("Browser Profile archive escaped its root: %v", err)
	}
}

func newFixtureProfileEngine(t *testing.T, state, work string) *ProfileEngine {
	t.Helper()
	engine, err := NewProfileEngine(ProfileEngineOptions{
		Process: ProcessEngineOptions{
			Command: []string{"/fixture/browser-engine"},
			Environment: []string{
				"NO_PROXY=",
				"OPENLINKER_BROWSER_EGRESS_PROXY=http://egress.test:3128",
				"OPENLINKER_BROWSER_PROFILE_DIR=" + filepath.Join(work, "active"),
			},
		},
		StoreRoot:   filepath.Join(state, "encrypted"),
		WorkRoot:    work,
		RootKeyFile: filepath.Join(state, "profile-root-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func fixtureProfileFactory(loaded *bool) func(ProcessEngineOptions) (managedBrowserEngine, error) {
	return func(options ProcessEngineOptions) (managedBrowserEngine, error) {
		directory := ""
		for _, value := range options.Environment {
			if strings.HasPrefix(value, "OPENLINKER_BROWSER_PROFILE_DIR=") {
				directory = strings.TrimPrefix(value, "OPENLINKER_BROWSER_PROFILE_DIR=")
			}
		}
		if _, err := os.Stat(filepath.Join(directory, "Default", "Cookies.fixture")); err == nil {
			*loaded = true
		}
		return &fixtureProfileProcess{directory: directory}, nil
	}
}

func profileEngineIdentity(principal string) browserprotocol.Identity {
	return browserprotocol.Identity{
		RunID:            "11111111-1111-4111-8111-111111111111",
		AgentID:          "22222222-2222-4222-8222-222222222222",
		PrincipalScopeID: principal,
		BrowserSessionID: "33333333-3333-4333-8333-333333333333",
		SessionEpoch:     1,
		AttachmentID:     "44444444-4444-4444-8444-444444444444",
		ControlEpoch:     1,
	}
}

func assertPersistentProfileDoesNotContain(t *testing.T, root, value string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(value)) {
			t.Fatalf("persistent Browser Profile file %s contains plaintext state", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
