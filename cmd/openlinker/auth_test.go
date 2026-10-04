package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/credentials"
)

func TestPlatformCommandsUseInstanceBoundLoginAndPreserveOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("explicit POSIX file backend; Windows uses keyring")
	}
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "openlinker")
	s, _ := credentials.New(testEnv(map[string]string{"OPENLINKER_CONFIG_DIR": config}))
	unlock, err := s.Lock(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(credentials.Record{API: server.URL, Store: "file", UserID: "user", TokenID: "token", Token: "ol_user_saved", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	unlock()
	for _, tc := range []struct{ name, env, flag, want string }{
		{"saved", "", "", "Bearer ol_user_saved"},
		{"environment", "ol_user_environment", "", "Bearer ol_user_environment"},
		{"flag", "ol_user_environment", "ol_user_flag", "Bearer ol_user_flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			args := []string{"--api", server.URL, "agents", "search"}
			if tc.flag != "" {
				args = append(args, "--token", tc.flag)
			}
			if runCLI(args, nil, &out, &diagnostics, testEnv(map[string]string{"OPENLINKER_CONFIG_DIR": config, "OPENLINKER_USER_TOKEN": tc.env})) != 0 {
				t.Fatal(diagnostics.String())
			}
			if received != tc.want {
				t.Errorf("got %q, want %q", received, tc.want)
			}
		})
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked to another instance")
		}
		w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer other.Close()
	var out, diagnostics bytes.Buffer
	if runCLI([]string{"--api", other.URL, "agents", "search"}, nil, &out, &diagnostics, testEnv(map[string]string{"OPENLINKER_CONFIG_DIR": config})) != 0 {
		t.Fatal(diagnostics.String())
	}
}

func TestAnonymousCommandsRemainUsableWithoutSavedLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected credential")
		}
		w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer server.Close()
	for _, scenario := range []string{"public-parent", "relative-config", "no-home", "expired", "unreadable-record"} {
		t.Run(scenario, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "config")
			if err := os.Mkdir(parent, 0755); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{"OPENLINKER_CONFIG_DIR": parent}
			switch scenario {
			case "relative-config":
				env["OPENLINKER_CONFIG_DIR"] = "relative"
			case "no-home":
				t.Setenv("HOME", "")
				t.Setenv("XDG_CONFIG_HOME", "")
				t.Setenv("AppData", "")
				env["OPENLINKER_CONFIG_DIR"] = ""
			case "expired", "unreadable-record":
				if runtime.GOOS == "windows" {
					t.Skip("POSIX file fixture")
				}
				s, _ := credentials.New(testEnv(env))
				unlock, err := s.Lock(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Save(credentials.Record{API: server.URL, Store: "file", UserID: "user", TokenID: "token", Token: "ol_user_expired", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
					t.Fatal(err)
				}
				unlock()
				if scenario == "unreadable-record" {
					// An invalid backend simulates an unreadable stored record without
					// touching or prompting for the user's actual system keyring.
					files, _ := filepath.Glob(filepath.Join(s.Dir, "*.json"))
					data, _ := os.ReadFile(files[0])
					data = bytes.ReplaceAll(data, []byte(`"file"`), []byte(`"unavailable"`))
					if err := os.WriteFile(files[0], data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, args := range [][]string{{"agents", "search"}, {"help"}, {"completion", "bash"}, {"context"}} {
				var out, diagnostics bytes.Buffer
				if runCLI(append([]string{"--api", server.URL}, args...), nil, &out, &diagnostics, testEnv(env)) != 0 {
					t.Fatal(args, diagnostics.String())
				}
				if bytes.Contains(out.Bytes(), []byte("ol_user_")) {
					t.Fatal("credential printed")
				}
			}
		})
	}
}
