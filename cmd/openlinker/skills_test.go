package main

import (
	"bytes"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/credentials"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSkillCommandsUseSavedCredentialOnlyForOwnedOperations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test file credential backend")
	}
	received := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "config")
	store, _ := credentials.New(testEnv(map[string]string{"OPENLINKER_CONFIG_DIR": config}))
	unlock, err := store.Lock(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(credentials.Record{API: server.URL, Store: "file", UserID: "synthetic", TokenID: "synthetic", Token: "ol_user_saved_skill", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	unlock()
	for _, tc := range []struct {
		owned           bool
		flag, env, want string
	}{{false, "", "", ""}, {false, "ol_user_explicit", "ol_user_env", ""}, {true, "", "", "Bearer ol_user_saved_skill"}, {true, "", "ol_user_env", "Bearer ol_user_env"}, {true, "ol_user_flag", "ol_user_env", "Bearer ol_user_flag"}} {
		var out, diag bytes.Buffer
		args := []string{"--api", server.URL, "skills", "list"}
		if tc.owned {
			args = append(args, "--owned")
		}
		if tc.flag != "" {
			args = append(args, "--token", tc.flag)
		}
		status := runCLI(args, nil, &out, &diag, testEnv(map[string]string{"OPENLINKER_CONFIG_DIR": config, "OPENLINKER_USER_TOKEN": tc.env}))
		if status != 0 || received != tc.want || strings.Contains(out.String(), "ol_user_") {
			t.Fatalf("credential precedence %v status %d diagnostics %s", tc.owned, status, diag.String())
		}
	}
}
