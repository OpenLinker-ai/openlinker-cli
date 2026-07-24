package browserprovider

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

func TestProbeCacheUsesIndependentSuccessAndFailureTTLs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	cache := NewProbeCache(15*time.Minute, time.Minute)
	cache.now = func() time.Time { return now }
	var calls atomic.Int32
	key := validProbeKey()
	probe := func(context.Context) *browserprotocol.Failure {
		if calls.Add(1) == 1 {
			return browserprotocol.NewFailure(
				browserprotocol.ErrorProviderCapability,
				"fixture unsupported",
				false,
			)
		}
		return nil
	}

	first := cache.Check(context.Background(), key, probe)
	if first.Supported || first.Cached || calls.Load() != 1 {
		t.Fatalf("first=%#v calls=%d", first, calls.Load())
	}
	now = now.Add(59 * time.Second)
	second := cache.Check(context.Background(), key, probe)
	if second.Supported || !second.Cached || calls.Load() != 1 {
		t.Fatalf("second=%#v calls=%d", second, calls.Load())
	}
	now = now.Add(2 * time.Second)
	third := cache.Check(context.Background(), key, probe)
	if !third.Supported || third.Cached || calls.Load() != 2 {
		t.Fatalf("third=%#v calls=%d", third, calls.Load())
	}
	now = now.Add(14 * time.Minute)
	fourth := cache.Check(context.Background(), key, probe)
	if !fourth.Supported || !fourth.Cached || calls.Load() != 2 {
		t.Fatalf("fourth=%#v calls=%d", fourth, calls.Load())
	}
	now = now.Add(2 * time.Minute)
	fifth := cache.Check(context.Background(), key, probe)
	if !fifth.Supported || fifth.Cached || calls.Load() != 3 {
		t.Fatalf("fifth=%#v calls=%d", fifth, calls.Load())
	}
}

func TestProbeCacheCoalescesConcurrentCapabilityRequests(t *testing.T) {
	t.Parallel()
	cache := NewProbeCache(time.Minute, time.Minute)
	key := validProbeKey()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	probe := func(context.Context) *browserprotocol.Failure {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}
	const workers = 16
	results := make(chan ProbeResult, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			results <- cache.Check(context.Background(), key, probe)
		}()
	}
	<-started
	close(release)
	wait.Wait()
	close(results)
	for result := range results {
		if !result.Supported {
			t.Fatalf("result = %#v", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("probe calls = %d, want 1", calls.Load())
	}
}

func TestProbeCacheGenerationSeparatesAndInvalidateRemovesOnlyTarget(t *testing.T) {
	t.Parallel()
	cache := NewProbeCache(time.Hour, time.Hour)
	var calls atomic.Int32
	probe := func(context.Context) *browserprotocol.Failure {
		calls.Add(1)
		return nil
	}
	first := validProbeKey()
	second := first
	second.ProviderConfigGeneration++
	cache.Check(context.Background(), first, probe)
	cache.Check(context.Background(), second, probe)
	cache.Check(context.Background(), first, probe)
	if calls.Load() != 2 {
		t.Fatalf("probe calls = %d, want 2 generations", calls.Load())
	}
	cache.Invalidate(first)
	cache.Check(context.Background(), first, probe)
	cache.Check(context.Background(), second, probe)
	if calls.Load() != 3 {
		t.Fatalf("probe calls = %d after invalidate, want 3", calls.Load())
	}
}

func TestProbeCacheRejectsOrdinaryExecutionProfileWithoutProbing(t *testing.T) {
	t.Parallel()
	cache := NewProbeCache(time.Hour, time.Hour)
	key := validProbeKey()
	key.ExecutionProfile = "provider_cli"
	called := false
	result := cache.Check(
		context.Background(),
		key,
		func(context.Context) *browserprotocol.Failure {
			called = true
			return nil
		},
	)
	if called || result.Failure == nil ||
		result.Failure.Code != browserprotocol.ErrorProtocolInvalid {
		t.Fatalf("called=%t result=%#v", called, result)
	}
}

func validProbeKey() ProbeKey {
	return ProbeKey{
		AgentID:                  "11111111-1111-4111-8111-111111111111",
		ExecutionProfile:         "native_browser",
		Provider:                 "openai",
		RouterOrigin:             "https://router.example",
		Model:                    "fixture-model",
		ProviderConfigGeneration: 1,
	}
}
