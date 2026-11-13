package workflows

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
)

type recorded struct {
	method, path, query, auth string
	body                      map[string]any
}

func execWorkflows(t *testing.T, handler http.HandlerFunc, args ...string) (string, error, []recorded) {
	t.Helper()
	var calls []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &call.body)
		}
		calls = append(calls, call)
		handler(w, r)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	opts := &shared.GlobalOptions{APIBase: server.URL + "/api/v1", UserToken: "olu_test", Timeout: 5 * time.Second}
	cmd := New(shared.IO{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, opts)
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String(), err, calls
}

func TestWorkflowsCommandsCallThePlatformAPI(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r1","status":"pending"}`))
	}
	cases := []struct {
		args         []string
		method, path string
	}{
		{[]string{"list", "--status", "active"}, "GET", "/api/v1/workflows"},
		{[]string{"get", "wf1"}, "GET", "/api/v1/workflows/wf1"},
		{[]string{"get", "wf1", "--version", "2"}, "GET", "/api/v1/workflows/wf1/versions/2"},
		{[]string{"runs", "wf1"}, "GET", "/api/v1/workflows/wf1/runs"},
		{[]string{"status", "r1"}, "GET", "/api/v1/workflow-runs/r1"},
		{[]string{"cancel", "r1"}, "POST", "/api/v1/workflow-runs/r1/cancel"},
		{[]string{"run", "wf1", "--input", `{"q":1}`, "--version", "3"}, "POST", "/api/v1/workflows/wf1/runs"},
	}
	for _, tc := range cases {
		_, err, calls := execWorkflows(t, ok, tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if len(calls) != 1 || calls[0].method != tc.method || calls[0].path != tc.path || calls[0].auth != "Bearer olu_test" {
			t.Fatalf("%v: calls = %#v", tc.args, calls)
		}
		if tc.args[0] == "run" {
			if calls[0].body["version"] != float64(3) || calls[0].body["input"].(map[string]any)["q"] != float64(1) {
				t.Fatalf("run body = %#v", calls[0].body)
			}
		}
		if tc.args[0] == "list" && calls[0].query != "status=active" {
			t.Fatalf("list query = %q", calls[0].query)
		}
	}
}

func TestWorkflowRunWaitFollowsTheRunToTheEnd(t *testing.T) {
	var polls atomic.Int32
	out, err, calls := execWorkflows(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"r1","status":"pending"}`))
			return
		}
		if polls.Add(1) < 2 {
			_, _ = w.Write([]byte(`{"id":"r1","status":"running"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r1","status":"success","output":{"ok":true}}`))
	}, "run", "wf1", "--wait", "--poll-interval", "10ms")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.Contains(out, `"success"`) {
		t.Fatalf("calls=%d out=%s", len(calls), out)
	}

	_, err, _ = execWorkflows(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"r2","status":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r2","status":"failed"}`))
	}, "run", "wf1", "--wait", "--poll-interval", "10ms")
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("failed run error = %v", err)
	}
}

func TestWorkflowsErrorsCarryThePlatformCode(t *testing.T) {
	_, err, _ := execWorkflows(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"WORKFLOW_ARCHIVED","message":"archived"}}`))
	}, "run", "wf1")
	if err == nil || !strings.Contains(err.Error(), "WORKFLOW_ARCHIVED") {
		t.Fatalf("err = %v", err)
	}
	_, err, _ = execWorkflows(t, func(w http.ResponseWriter, _ *http.Request) {}, "run", "wf1", "--input", `[1]`)
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("array input err = %v", err)
	}
	_, err, _ = execWorkflows(t, func(w http.ResponseWriter, _ *http.Request) {}, "get")
	if err == nil {
		t.Fatal("get without id succeeded")
	}
}
