package browserprovider_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
	anthropicadapter "github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider/anthropic"
	openaiadapter "github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider/openai"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/providerbroker"
)

func TestOpenAIAdapterUsesBrokerWithoutReceivingRealCredential(t *testing.T) {
	t.Parallel()
	const realCredential = "openai-real-provider-credential-sentinel"
	var upstreamAuthorization, upstreamBody string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		upstreamAuthorization = request.Header.Get("Authorization")
		raw, _ := io.ReadAll(request.Body)
		upstreamBody = string(raw)
		_, _ = io.WriteString(writer, `{
			"id":"resp_final",
			"status":"completed",
			"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]
		}`)
	}))
	defer upstream.Close()
	broker := startIntegrationBroker(t, providerbroker.Config{
		Provider:  providerbroker.ProviderOpenAI,
		Upstream:  "https://openai.provider.example",
		Secret:    realCredential,
		Transport: integrationTransport(upstream),
	})
	adapter, err := openaiadapter.New(openaiadapter.Config{
		Endpoint:           broker.Endpoint(),
		LocalAuthorization: broker.LocalAuthorization(),
		Model:              "fixture-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "return a result"},
		nil,
		browserprovider.ExecuteFunc(unexpectedCredentialBoundaryExecute(t)),
	)
	if failure != nil || result.FinalText != "ok" {
		t.Fatalf("result=%#v failure=%#v", result, failure)
	}
	if upstreamAuthorization != "Bearer "+realCredential ||
		strings.Contains(upstreamBody, realCredential) ||
		strings.Contains(broker.Endpoint(), realCredential) ||
		strings.Contains(broker.LocalAuthorization(), realCredential) ||
		strings.Contains(fmt.Sprintf("%#v", adapter), broker.LocalAuthorization()) {
		t.Fatal("OpenAI Provider credential crossed the broker boundary")
	}
}

func TestAnthropicAdapterUsesIndependentBrokerHeaderPolicy(t *testing.T) {
	t.Parallel()
	const realCredential = "anthropic-real-provider-credential-sentinel"
	var authorization, apiKey, body string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		authorization = request.Header.Get("Authorization")
		apiKey = request.Header.Get("x-api-key")
		raw, _ := io.ReadAll(request.Body)
		body = string(raw)
		_, _ = io.WriteString(writer, `{
			"id":"msg_final",
			"type":"message",
			"role":"assistant",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}]
		}`)
	}))
	defer upstream.Close()
	broker := startIntegrationBroker(t, providerbroker.Config{
		Provider:  providerbroker.ProviderAnthropic,
		Upstream:  "https://anthropic.provider.example",
		Secret:    realCredential,
		Transport: integrationTransport(upstream),
	})
	adapter, err := anthropicadapter.New(anthropicadapter.Config{
		Endpoint:           broker.Endpoint(),
		LocalAuthorization: broker.LocalAuthorization(),
		Model:              "fixture-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, failure := adapter.Run(
		context.Background(),
		browserprovider.RunInput{Task: "return a result"},
		nil,
		browserprovider.ExecuteFunc(unexpectedCredentialBoundaryExecute(t)),
	)
	if failure != nil || result.FinalText != "ok" {
		t.Fatalf("result=%#v failure=%#v", result, failure)
	}
	if authorization != "" ||
		apiKey != realCredential ||
		strings.Contains(body, realCredential) ||
		strings.Contains(fmt.Sprintf("%#v", adapter), broker.LocalAuthorization()) {
		t.Fatal("Anthropic Provider credential crossed the broker boundary")
	}
}

func startIntegrationBroker(
	t *testing.T,
	config providerbroker.Config,
) *providerbroker.Server {
	t.Helper()
	server, err := providerbroker.Start(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	return server
}

type integrationRoundTripFunc func(*http.Request) (*http.Response, error)

func (function integrationRoundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return function(request)
}

func integrationTransport(upstream *httptest.Server) http.RoundTripper {
	target := upstream.Client().Transport
	return integrationRoundTripFunc(func(
		request *http.Request,
	) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clonedURL := *request.URL
		clonedURL.Scheme = "https"
		clonedURL.Host = strings.TrimPrefix(upstream.URL, "https://")
		clone.URL = &clonedURL
		return target.RoundTrip(clone)
	})
}

func unexpectedCredentialBoundaryExecute(
	t *testing.T,
) func(
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
