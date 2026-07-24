package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
)

func TestAdapterRunsIndependentMessagesComputerLoop(t *testing.T) {
	t.Parallel()
	var requests []map[string]any
	var mu sync.Mutex
	server := newLoopbackServer(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Authorization") != "Bearer "+testLocalAuthorization() {
			t.Error("local broker authorization is missing")
		}
		if request.Header.Get("anthropic-version") != defaultAPIVersion ||
			request.Header.Get("anthropic-beta") != defaultBetaVersion {
			t.Errorf("Anthropic protocol headers are missing: %v", request.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, payload)
		number := len(requests)
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if number == 1 {
			_, _ = io.WriteString(writer, `{
				"id":"msg_first",
				"type":"message",
				"role":"assistant",
				"stop_reason":"tool_use",
				"content":[{
					"type":"tool_use",
					"id":"tool_1",
					"name":"computer",
					"input":{"action":"left_click","coordinate":[12,34]}
				}]
			}`)
			return
		}
		_, _ = io.WriteString(writer, `{
			"id":"msg_final",
			"type":"message",
			"role":"assistant",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"done"}]
		}`)
	}))
	adapter := testAdapter(t, server.URL, false)
	session := &Session{}
	var actions []browserprotocol.Action
	var progress int
	executor := browserprovider.ExecuteFunc(func(
		_ context.Context,
		action browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure) {
		actions = append(actions, action)
		observation := browserprotocol.Observation{
			PageStateID: fmt.Sprintf("page-%d", len(actions)),
		}
		if action.Kind == browserprotocol.ActionScreenshot {
			observation.Screenshot = &browserprotocol.Screenshot{
				MIMEType: "image/jpeg",
				Data:     []byte("fixture-image"),
			}
		}
		return observation, nil
	})
	result, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{
			Task: "inspect page",
			OnProgress: func(context.Context, browserprovider.Progress) {
				progress++
			},
		},
		session,
		executor,
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if result.FinalText != "done" || result.Turns != 2 || result.Actions != 2 {
		t.Fatalf("result = %#v", result)
	}
	if progress != 2 ||
		len(actions) != 2 ||
		actions[0].Kind != browserprotocol.ActionClick ||
		actions[1].Kind != browserprotocol.ActionScreenshot {
		t.Fatalf("actions=%#v progress=%d", actions, progress)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	raw, err := json.Marshal(requests[1])
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"type":"tool_result"`) ||
		!strings.Contains(text, `"tool_use_id":"tool_1"`) ||
		!strings.Contains(text, `"media_type":"image/jpeg"`) ||
		!strings.Contains(text, `"data":"Zml4dHVyZS1pbWFnZQ=="`) {
		t.Fatalf("follow-up payload = %s", text)
	}
	if len(session.messages) != 4 {
		t.Fatalf("session messages = %d", len(session.messages))
	}
}

func TestAdapterParsesIndependentAnthropicStream(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := newLoopbackServer(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			writeSSE(t, writer, []string{
				`event: message_start
data: {"type":"message_start","message":{"id":"msg_stream_1","type":"message","role":"assistant","content":[]}}`,
				`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_stream","name":"computer","input":{}}}`,
				`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"action\":\"screenshot\"}"}}`,
				`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
				`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				`event: message_stop
data: {"type":"message_stop"}`,
			})
			return
		}
		writeSSE(t, writer, []string{
			`event: message_start
data: {"type":"message_start","message":{"id":"msg_stream_2","type":"message","role":"assistant","content":[]}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed"}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		})
	}))
	adapter := testAdapter(t, server.URL, true)
	result, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "inspect"},
		&Session{},
		browserprovider.ExecuteFunc(func(
			_ context.Context,
			action browserprotocol.Action,
		) (browserprotocol.Observation, *browserprotocol.Failure) {
			if action.Kind != browserprotocol.ActionScreenshot {
				t.Errorf("action = %#v", action)
			}
			return browserprotocol.Observation{
				PageStateID: "page-stream",
				Screenshot: &browserprotocol.Screenshot{
					MIMEType: "image/png",
					Data:     []byte("png"),
				},
			}, nil
		}),
	)
	if failure != nil || result.FinalText != "streamed" ||
		result.Turns != 2 || result.Actions != 1 {
		t.Fatalf("result=%#v failure=%#v", result, failure)
	}
}

func TestAdapterRejectsUnsupportedPermissionSensitiveAction(t *testing.T) {
	t.Parallel()
	server := responseServer(t, `{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"stop_reason":"tool_use",
		"content":[{
			"type":"tool_use",
			"id":"tool_1",
			"name":"computer",
			"input":{"action":"right_click","coordinate":[1,2]}
		}]
	}`)
	adapter := testAdapter(t, server.URL, false)
	session := &Session{messages: []message{{
		Role: "user", Content: "stable history",
	}}}
	_, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "task"},
		session,
		browserprovider.ExecuteFunc(unexpectedExecute(t)),
	)
	if failure == nil || failure.Code != browserprotocol.ErrorActionRejected {
		t.Fatalf("failure = %#v", failure)
	}
	if len(session.messages) != 1 {
		t.Fatalf("failed Run mutated session: %#v", session)
	}
}

func TestAdapterCapabilityProbeRequiresScreenshotComputerCall(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		body      string
		supported bool
	}{
		{
			name:      "supported",
			body:      `{"id":"msg_probe","type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool_probe","name":"computer","input":{"action":"screenshot"}}]}`,
			supported: true,
		},
		{
			name: "text only",
			body: `{"id":"msg_probe","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"no tool"}]}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := responseServer(t, test.body)
			adapter := testAdapter(t, server.URL, false)
			failure := adapter.Probe(context.Background())
			if test.supported && failure != nil {
				t.Fatal(failure)
			}
			if !test.supported &&
				(failure == nil || failure.Code != browserprotocol.ErrorProviderCapability) {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestAdapterUsesBoundedSessionHistory(t *testing.T) {
	t.Parallel()
	adapter, err := New(Config{
		Endpoint:           "http://127.0.0.1:12345/invoke",
		LocalAuthorization: testLocalAuthorization(),
		Model:              "fixture-model",
		MaxHistoryBytes:    1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{messages: []message{{
		Role: "user", Content: strings.Repeat("x", (1<<20)+1),
	}}}
	_, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "task"},
		session,
		browserprovider.ExecuteFunc(unexpectedExecute(t)),
	)
	if failure == nil || failure.Code != browserprotocol.ErrorConversationRecovery {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestAdapterRetriesRateLimitAndCancellation(t *testing.T) {
	t.Parallel()
	t.Run("retry", func(t *testing.T) {
		var calls atomic.Int32
		server := newLoopbackServer(t, http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			if calls.Add(1) == 1 {
				writer.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = io.WriteString(writer, `{
				"id":"msg_final","type":"message","role":"assistant",
				"stop_reason":"end_turn",
				"content":[{"type":"text","text":"ok"}]
			}`)
		}))
		adapter := testAdapter(t, server.URL, false)
		result, failure := adapter.Run(
			context.Background(),
			browserprovider.RunInput{Task: "task"},
			&Session{},
			browserprovider.ExecuteFunc(unexpectedExecute(t)),
		)
		if failure != nil || result.FinalText != "ok" || calls.Load() != 2 {
			t.Fatalf("result=%#v failure=%#v calls=%d", result, failure, calls.Load())
		}
	})
	t.Run("cancel", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		server := newLoopbackServer(t, http.HandlerFunc(func(
			_ http.ResponseWriter,
			_ *http.Request,
		) {
			close(started)
			<-release
		}))
		adapter := testAdapter(t, server.URL, false)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan *browserprotocol.Failure, 1)
		go func() {
			_, failure := adapter.Run(
				ctx,
				browserprovider.RunInput{Task: "task"},
				&Session{},
				browserprovider.ExecuteFunc(unexpectedExecute(t)),
			)
			done <- failure
		}()
		<-started
		cancel()
		select {
		case failure := <-done:
			if failure == nil || failure.Code != browserprotocol.ErrorCanceled {
				t.Fatalf("failure = %#v", failure)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled Anthropic request did not return")
		}
	})
}

func TestAdapterNeverFollowsBrokerRedirect(t *testing.T) {
	t.Parallel()
	var redirected atomic.Int32
	target := newLoopbackServer(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		redirected.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	source := newLoopbackServer(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	adapter := testAdapter(t, source.URL, false)
	_, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "task"},
		&Session{},
		browserprovider.ExecuteFunc(unexpectedExecute(t)),
	)
	if failure == nil || redirected.Load() != 0 {
		t.Fatalf("failure=%#v redirected=%d", failure, redirected.Load())
	}
}

func TestNewRequiresLoopbackBrokerAndExplicitModel(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"https://api.anthropic.com/v1/messages",
		"http://localhost:1234/invoke",
		"http://127.0.0.1/invoke",
		"http://127.0.0.1:1234",
	} {
		if _, err := New(Config{
			Endpoint:           endpoint,
			LocalAuthorization: testLocalAuthorization(),
			Model:              "fixture-model",
		}); err == nil {
			t.Fatalf("New(%q) succeeded", endpoint)
		}
	}
}

func TestSessionStateIsVersionedBoundedAndRejectsCrossProviderData(t *testing.T) {
	t.Parallel()
	adapter, err := New(Config{
		Endpoint:           "http://127.0.0.1:12345/invoke",
		LocalAuthorization: testLocalAuthorization(),
		Model:              "fixture-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{messages: []message{{
		Role: "user", Content: "local history",
	}}}
	raw, failure := adapter.MarshalSession(session)
	if failure != nil {
		t.Fatal(failure)
	}
	restored, failure := adapter.RestoreSession(raw)
	if failure != nil || len(restored.messages) != 1 {
		t.Fatalf("restored=%#v failure=%#v", restored, failure)
	}
	if strings.Contains(fmt.Sprintf("%#v", restored), "local history") {
		t.Fatal("Anthropic Session formatting exposed Provider message history")
	}
	_, failure = adapter.RestoreSession([]byte(
		`{"contract_id":"openlinker.browser.provider.openai.session.v1","previous_response_id":"resp"}`,
	))
	if failure == nil || failure.Code != browserprotocol.ErrorConversationRecovery {
		t.Fatalf("failure = %#v", failure)
	}
}

func testAdapter(t *testing.T, endpoint string, stream bool) *Adapter {
	t.Helper()
	adapter, err := New(Config{
		Endpoint:           endpoint,
		LocalAuthorization: testLocalAuthorization(),
		Model:              "fixture-model",
		Stream:             stream,
		MaxAttempts:        2,
		RetryDelay:         time.Millisecond,
		Client:             &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func testLocalAuthorization() string {
	return strings.Repeat("l", 48)
}

func newLoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	server.URL += "/invoke"
	return server
}

func responseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return newLoopbackServer(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, body)
	}))
}

func unexpectedExecute(t *testing.T) func(
	context.Context,
	browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	t.Helper()
	return func(
		context.Context,
		browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure) {
		t.Error("Browser executor was called unexpectedly")
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"unexpected Browser execution",
			false,
		)
	}
}

func writeSSE(t *testing.T, writer io.Writer, events []string) {
	t.Helper()
	for _, event := range events {
		if _, err := io.WriteString(writer, event+"\n\n"); err != nil {
			t.Error(err)
			return
		}
	}
}
