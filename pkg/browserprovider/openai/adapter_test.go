package openai

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

func TestAdapterRunsIndependentResponsesComputerLoop(t *testing.T) {
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
				"id":"resp_first",
				"status":"completed",
				"output":[{
					"type":"computer_call",
					"call_id":"call_1",
					"actions":[
						{"type":"click","button":"left","x":12,"y":34},
						{"type":"type","text":"penguin"}
					]
				}]
			}`)
			return
		}
		_, _ = io.WriteString(writer, `{
			"id":"resp_final",
			"status":"completed",
			"output":[{
				"type":"message",
				"content":[{"type":"output_text","text":"done"}]
			}]
		}`)
	}))

	adapter := testAdapter(t, server.URL, false)
	var actions []browserprotocol.Action
	var progress int
	executor := browserprovider.ExecuteFunc(func(
		_ context.Context,
		action browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure) {
		actions = append(actions, action)
		observation := browserprotocol.Observation{PageStateID: fmt.Sprintf("page-%d", len(actions))}
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
			Task: "find a penguin",
			OnProgress: func(context.Context, browserprovider.Progress) {
				progress++
			},
		},
		&Session{},
		executor,
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if result.FinalText != "done" || result.Turns != 2 || result.Actions != 3 {
		t.Fatalf("result = %#v", result)
	}
	if progress != 3 {
		t.Fatalf("progress count = %d, want action actions plus screenshot", progress)
	}
	if len(actions) != 3 ||
		actions[0].Kind != browserprotocol.ActionClick ||
		actions[1].Kind != browserprotocol.ActionTypeNonSecret ||
		actions[2].Kind != browserprotocol.ActionScreenshot {
		t.Fatalf("actions = %#v", actions)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0]["previous_response_id"] != nil {
		t.Fatalf("initial request unexpectedly resumed: %#v", requests[0])
	}
	if requests[1]["previous_response_id"] != "resp_first" {
		t.Fatalf("follow-up did not chain response: %#v", requests[1])
	}
	raw, err := json.Marshal(requests[1])
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"type":"computer_call_output"`) ||
		!strings.Contains(text, `"call_id":"call_1"`) ||
		!strings.Contains(text, `data:image/jpeg;base64,Zml4dHVyZS1pbWFnZQ==`) {
		t.Fatalf("follow-up payload = %s", text)
	}
}

func TestAdapterParsesCompletedSSEAndReusesSession(t *testing.T) {
	t.Parallel()
	var previous string
	server := newLoopbackServer(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		previous, _ = payload["previous_response_id"].(string)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer,
			"event: response.completed\n"+
				`data: {"type":"response.completed","response":{"id":"resp_next","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"resumed"}]}]}}`+
				"\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	adapter := testAdapter(t, server.URL, true)
	session := &Session{previousResponseID: "resp_prior"}
	result, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "continue"},
		session,
		browserprovider.ExecuteFunc(unexpectedExecute(t)),
	)
	if failure != nil || result.FinalText != "resumed" {
		t.Fatalf("result=%#v failure=%#v", result, failure)
	}
	if previous != "resp_prior" || session.previousResponseID != "resp_next" {
		t.Fatalf("previous=%q session=%#v", previous, session)
	}
}

func TestAdapterSafetyCheckFailsClosedBeforeBrowserAction(t *testing.T) {
	t.Parallel()
	server := responseServer(t, `{
		"id":"resp_safety",
		"status":"completed",
		"output":[{
			"type":"computer_call",
			"call_id":"call_safety",
			"pending_safety_checks":[{"id":"safe_1","code":"confirmation","message":"confirm"}],
			"actions":[{"type":"click","button":"left","x":1,"y":2}]
		}]
	}`)
	adapter := testAdapter(t, server.URL, false)
	session := &Session{previousResponseID: "resp_stable"}
	_, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "perform action"},
		session,
		browserprovider.ExecuteFunc(unexpectedExecute(t)),
	)
	if failure == nil || failure.Code != browserprotocol.ErrorUserActionRequired {
		t.Fatalf("failure = %#v", failure)
	}
	if session.previousResponseID != "resp_stable" {
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
			body:      `{"id":"resp_probe","status":"completed","output":[{"type":"computer_call","call_id":"call_probe","actions":[{"type":"screenshot"}]}]}`,
			supported: true,
		},
		{
			name: "text only",
			body: `{"id":"resp_probe","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"no tool"}]}]}`,
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

func TestAdapterRejectsUnsupportedActionsAndEnforcesLimit(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
		code browserprotocol.ErrorCode
	}{
		{
			name: "unsupported drag",
			body: `{"id":"resp_1","status":"completed","output":[{"type":"computer_call","call_id":"call_1","actions":[{"type":"drag","path":[]}]}]}`,
			code: browserprotocol.ErrorActionRejected,
		},
		{
			name: "action limit",
			body: `{"id":"resp_1","status":"completed","output":[{"type":"computer_call","call_id":"call_1","actions":[{"type":"click","x":1,"y":1},{"type":"click","x":2,"y":2}]}]}`,
			code: browserprotocol.ErrorActionLimitExceeded,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := responseServer(t, test.body)
			adapter := testAdapter(t, server.URL, false)
			_, failure := adapter.Run(
				context.Background(),
				browserprovider.RunInput{Task: "task", MaxActions: 1},
				&Session{},
				browserprovider.ExecuteFunc(func(
					_ context.Context,
					action browserprotocol.Action,
				) (browserprotocol.Observation, *browserprotocol.Failure) {
					return browserprotocol.Observation{PageStateID: string(action.Kind)}, nil
				}),
			)
			if failure == nil || failure.Code != test.code {
				t.Fatalf("failure = %#v, want %s", failure, test.code)
			}
		})
	}
}

func TestAdapterRetriesTransientHTTPFailureWithoutExecutingAction(t *testing.T) {
	t.Parallel()
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
			"id":"resp_final",
			"status":"completed",
			"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]
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
}

func TestAdapterCancellationAbortsProviderRequest(t *testing.T) {
	t.Parallel()
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
		t.Fatal("canceled OpenAI request did not return")
	}
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

func TestNewRequiresLoopbackBrokerAndNeverAcceptsProviderKeySurface(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"https://api.openai.com/v1/responses",
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

func TestSessionStateIsVersionedOpaqueAndRejectsCrossProviderData(t *testing.T) {
	t.Parallel()
	adapter, err := New(Config{
		Endpoint:           "http://127.0.0.1:12345/invoke",
		LocalAuthorization: testLocalAuthorization(),
		Model:              "fixture-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, failure := adapter.MarshalSession(&Session{previousResponseID: "resp_local"})
	if failure != nil {
		t.Fatal(failure)
	}
	restored, failure := adapter.RestoreSession(raw)
	if failure != nil || restored.previousResponseID != "resp_local" {
		t.Fatalf("restored=%#v failure=%#v", restored, failure)
	}
	if strings.Contains(fmt.Sprintf("%#v", restored), "resp_local") {
		t.Fatal("OpenAI Session formatting exposed the Provider session identifier")
	}
	_, failure = adapter.RestoreSession([]byte(
		`{"contract_id":"openlinker.browser.provider.anthropic.session.v1","messages":[]}`,
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
