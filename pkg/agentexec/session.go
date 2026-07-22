package agentexec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type sessionStore struct {
	Sessions map[string]sessionRecord `json:"sessions"`
}

type sessionRecord struct {
	SessionID      string `json:"session_id"`
	SessionKeyHash string `json:"session_key_hash"`
	Workspace      string `json:"workspace"`
	UpdatedAt      string `json:"updated_at"`
}

var sessionStoreMu sync.Mutex

func sessionStorePath(configured, provider, workspace string) string {
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		cacheDir = os.TempDir()
	}
	sum := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return filepath.Join(cacheDir, "openlinker", "session-map", provider+"-"+hex.EncodeToString(sum[:8])+".json")
}

func sessionStoreKey(provider, workspace, sessionKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(provider) + "\x00" + filepath.Clean(workspace) + "\x00" + strings.TrimSpace(sessionKey)))
	return hex.EncodeToString(sum[:])
}

func sessionKeyHash(provider, workspace, sessionKey string) string {
	return sessionStoreKey(provider, workspace, sessionKey)[:24]
}

func loadSessionID(path, provider, workspace, sessionKey string) string {
	sessionStoreMu.Lock()
	defer sessionStoreMu.Unlock()
	store := readSessionStore(path)
	return strings.TrimSpace(store.Sessions[sessionStoreKey(provider, workspace, sessionKey)].SessionID)
}

func saveSessionID(path, provider, workspace, sessionKey, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	sessionStoreMu.Lock()
	defer sessionStoreMu.Unlock()
	store := readSessionStore(path)
	key := sessionStoreKey(provider, workspace, sessionKey)
	store.Sessions[key] = sessionRecord{
		SessionID: sessionID, SessionKeyHash: key[:24], Workspace: filepath.Clean(workspace),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	return writeSessionStore(path, store)
}

func deleteSessionID(path, provider, workspace, sessionKey string) error {
	sessionStoreMu.Lock()
	defer sessionStoreMu.Unlock()
	store := readSessionStore(path)
	key := sessionStoreKey(provider, workspace, sessionKey)
	if _, ok := store.Sessions[key]; !ok {
		return nil
	}
	delete(store.Sessions, key)
	return writeSessionStore(path, store)
}

func readSessionStore(path string) sessionStore {
	store := sessionStore{Sessions: map[string]sessionRecord{}}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !sessionFileOwnedByCurrentUser(info) || info.Size() > maxSessionStoreBytes {
		return store
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return store
	}
	if err := json.Unmarshal(raw, &store); err != nil || store.Sessions == nil {
		return sessionStore{Sessions: map[string]sessionRecord{}}
	}
	return store
}

func writeSessionStore(path string, store sessionStore) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !sessionFileOwnedByCurrentUser(info)) {
		return errors.New("provider session store must be an owner-only regular non-symlink file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".sessions-*.tmp")
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

const maxSessionStoreBytes = 8 << 20

type sessionLockEntry struct {
	mu   sync.Mutex
	refs int
}

var sessionLocks = struct {
	sync.Mutex
	entries map[string]*sessionLockEntry
}{entries: map[string]*sessionLockEntry{}}

func lockSession(provider, workspace, sessionKey string) func() {
	key := sessionStoreKey(provider, workspace, sessionKey)
	sessionLocks.Lock()
	entry := sessionLocks.entries[key]
	if entry == nil {
		entry = &sessionLockEntry{}
		sessionLocks.entries[key] = entry
	}
	entry.refs++
	sessionLocks.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		sessionLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(sessionLocks.entries, key)
		}
		sessionLocks.Unlock()
	}
}

func conversationSessionKey(run RunContext) string {
	if run.Conversation != nil {
		for _, value := range []string{run.Conversation.SessionKey, run.Conversation.RootContextID, run.Conversation.ProtocolContextID, run.Conversation.ID} {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func missingProviderSession(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, fragment := range []string{"session not found", "unknown session", "no conversation found", "invalid session id", "no rollout found", "thread not found"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func sessionPersistenceError(provider string, err error) error {
	return fmt.Errorf("persist %s session: %w", provider, err)
}
