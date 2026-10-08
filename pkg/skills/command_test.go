package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

const packageID = "00000000-0000-4000-8000-000000000001"
const versionID = "00000000-0000-4000-8000-000000000002"
const agentID = "00000000-0000-4000-8000-000000000003"

func runSkills(t *testing.T, base, token string, args ...string) (string, error) {
	t.Helper()
	out := new(bytes.Buffer)
	opts := shared.GlobalOptions{APIBase: base, UserToken: token, Timeout: time.Second}
	cmd := New(shared.IO{Stdout: out, Stderr: ioDiscard{}}, &opts)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
func digest(raw string) string                { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func outputDirectory(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestCommandsCallCorrectPlatformRoutes(t *testing.T) {
	for _, tc := range []struct {
		args               []string
		method, path, body string
		private            bool
	}{
		{[]string{"list", "--query", "space &", "--page", "2", "--limit", "5"}, "GET", "/api/v1/skill-packages?page=2&q=space+%26&size=5", "", false},
		{[]string{"list", "--owned"}, "GET", "/api/v1/creator/skill-packages", "", true},
		{[]string{"get", "--id", packageID}, "GET", "/api/v1/skill-packages/" + packageID, "", false},
		{[]string{"import", "--id", packageID, "--version", versionID, "--digest", strings.Repeat("a", 64)}, "POST", "/api/v1/creator/skill-packages/imports", `{"expected_digest":"` + strings.Repeat("a", 64) + `","source_package_id":"` + packageID + `","source_version_id":"` + versionID + `"}`, true},
		{[]string{"bindings", "--agent", agentID}, "GET", "/api/v1/creator/agents/" + agentID + "/skill-packages", "", true},
		{[]string{"bind", "--agent", agentID, "--id", packageID, "--version", versionID}, "PUT", "/api/v1/creator/agents/" + agentID + "/skill-packages/" + packageID, `{"version_id":"` + versionID + `"}`, true},
		{[]string{"unbind", "--agent", agentID, "--id", packageID}, "DELETE", "/api/v1/creator/agents/" + agentID + "/skill-packages/" + packageID, "", true},
	} {
		t.Run(strings.Join(tc.args[:1], "")+tc.path, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Method != tc.method || r.URL.RequestURI() != tc.path {
					t.Errorf("route %s %s", r.Method, r.URL)
				}
				want := ""
				if tc.private {
					want = "Bearer ol_user_synthetic"
				}
				if r.Header.Get("Authorization") != want {
					t.Errorf("unexpected auth on %s", tc.path)
				}
				if tc.body != "" {
					var v any
					if json.NewDecoder(r.Body).Decode(&v) != nil {
						t.Error("missing JSON")
					}
					raw, _ := json.Marshal(v)
					if string(raw) != tc.body {
						t.Errorf("payload %s", raw)
					}
				}
				if tc.method == "DELETE" {
					w.WriteHeader(204)
				} else {
					fmt.Fprint(w, `{"items":[]}`)
				}
			}))
			defer server.Close()
			out, err := runSkills(t, server.URL, "ol_user_synthetic", tc.args...)
			if err != nil || !called || !json.Valid([]byte(out)) {
				t.Fatalf("result %q %v called=%v", out, err, called)
			}
		})
	}
}
func TestFixedMetadataAndOwnedVersionSelection(t *testing.T) {
	count := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if strings.HasSuffix(r.URL.Path, "/metadata") {
			fmt.Fprintf(w, `{"id":%q,"digest":%q,"contents":{"name":"fixed"}}`, versionID, strings.Repeat("a", 64))
			return
		}
		fmt.Fprintf(w, `{"id":%q,"versions":[{"id":%q,"digest":%q}]}`, packageID, versionID, strings.Repeat("a", 64))
	}))
	defer s.Close()
	out, err := runSkills(t, s.URL, "", "get", "--id", packageID, "--version", versionID)
	if err != nil || count != 2 || !strings.Contains(out, "fixed") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = runSkills(t, s.URL, "ol_user_x", "get", "--id", packageID, "--version", versionID, "--owned")
	if err != nil || count != 3 || strings.Contains(out, "contents") {
		t.Fatalf("%s %v", out, err)
	}
	_, err = runSkills(t, s.URL, "ol_user_x", "get", "--id", packageID, "--version", agentID, "--owned")
	if err == nil {
		t.Fatal("foreign version accepted")
	}
}
func TestDownloadVerifiesExactBytesAndNeverReplacesOutput(t *testing.T) {
	raw := `{"files":{"SKILL.md":"synthetic"}}`
	hash := digest(raw)
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if owned != (r.Header.Get("Authorization") != "") {
					t.Error("credential routing")
				}
				if strings.HasSuffix(r.URL.Path, "/metadata") {
					fmt.Fprintf(w, `{"id":%q,"digest":%q}`, versionID, hash)
				} else if r.URL.Path == "/api/v1/creator/skill-packages/"+packageID {
					fmt.Fprintf(w, `{"versions":[{"id":%q,"digest":%q}]}`, versionID, hash)
				} else {
					fmt.Fprint(w, raw)
				}
			}))
			defer s.Close()
			file := filepath.Join(outputDirectory(t), "bundle.json")
			args := []string{"download", "--id", packageID, "--version", versionID, "--digest", hash, "--output", file}
			if owned {
				args = append(args, "--owned")
			}
			out, err := runSkills(t, s.URL, "ol_user_x", args...)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if json.Unmarshal([]byte(out), &result) != nil || result["digest"] != hash {
				t.Fatal(out)
			}
			saved, err := os.ReadFile(file)
			if err != nil || string(saved) != raw {
				t.Fatal("bytes changed")
			}
			info, _ := os.Stat(file)
			if info.Mode().Perm() != 0600 {
				t.Fatal("public private-bundle permissions")
			}
			if _, err := runSkills(t, s.URL, "ol_user_x", args...); err == nil {
				t.Fatal("existing file overwritten")
			}
			saved, _ = os.ReadFile(file)
			if string(saved) != raw {
				t.Fatal("existing output changed")
			}
		})
	}
}
func TestDownloadFailuresLeaveNoOutput(t *testing.T) {
	for _, mode := range []string{"tampered", "expected", "oversized", "nonjson", "wrong-version", "metadata-digest"} {
		t.Run(mode, func(t *testing.T) {
			raw := `{"files":{}}`
			hash := digest(raw)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/metadata") {
					id := versionID
					d := hash
					if mode == "wrong-version" {
						id = agentID
					}
					if mode == "metadata-digest" {
						d = "bad"
					}
					fmt.Fprintf(w, `{"id":%q,"digest":%q}`, id, d)
					return
				}
				switch mode {
				case "tampered":
					raw = `{"files":{"x":"changed"}}`
				case "oversized":
					raw = strings.Repeat(" ", maxBundle+1)
				case "nonjson":
					raw = "invalid"
				}
				fmt.Fprint(w, raw)
			}))
			defer s.Close()
			file := filepath.Join(outputDirectory(t), "bundle.json")
			args := []string{"download", "--id", packageID, "--version", versionID, "--output", file}
			if mode == "expected" {
				args = append(args, "--digest", strings.Repeat("a", 64))
			}
			out, err := runSkills(t, s.URL, "", args...)
			if err == nil || out != "" {
				t.Fatalf("%s %v", out, err)
			}
			if _, err = os.Lstat(file); !os.IsNotExist(err) {
				t.Fatal("file exists on failed verification")
			}
		})
	}
}
func TestRedirectAndHostileResponseCannotLeakCredential(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	for _, mode := range []string{"redirect", "hostile-error", "oversized", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, 307)
				case "hostile-error":
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"message":"ol_user_synthetic"}}`)
				case "oversized":
					fmt.Fprint(w, strings.Repeat(" ", maxResponse+1))
				default:
					fmt.Fprint(w, "ol_user_synthetic")
				}
			}))
			defer s.Close()
			out, err := runSkills(t, s.URL, "ol_user_synthetic", "list", "--owned")
			if err == nil || hit || out != "" || strings.Contains(err.Error(), "ol_user_synthetic") {
				t.Fatalf("secret/redirect handling: %v", err)
			}
		})
	}
}
func TestOutputSymlinksAndAncestorsRejected(t *testing.T) {
	dir := outputDirectory(t)
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep"), 0600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, err := saveNewBundle(link, []byte("changed")); err == nil {
		t.Fatal("symlink followed")
	}
	parentLink := filepath.Join(dir, "alias")
	os.Symlink(dir, parentLink)
	if _, err := saveNewBundle(filepath.Join(parentLink, "new"), []byte("changed")); err == nil {
		t.Fatal("parent symlink followed")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "keep" {
		t.Fatal("target changed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".openlinker-skill-") {
			t.Fatal("temporary link leaked")
		}
	}
}
func TestInvalidInputsNeverReachAPI(t *testing.T) {
	hit := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer s.Close()
	for _, args := range [][]string{{"get", "--id", "../bad"}, {"bind", "--agent", agentID, "--id", packageID}, {"import", "--id", packageID, "--version", versionID}, {"list", "--limit", "51"}, {"list", "--owned", "--query", "q"}} {
		if _, err := runSkills(t, s.URL, "ol_user_x", args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := runSkills(t, s.URL, "browser-jwt", "list", "--owned"); err == nil {
		t.Fatal("JWT accepted as platform token")
	}
	if hit {
		t.Fatal("API called with invalid input")
	}
}
