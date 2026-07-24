package browserprovider

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const (
	DefaultSuccessfulProbeTTL = 15 * time.Minute
	DefaultFailedProbeTTL     = time.Minute
)

type ProbeKey struct {
	AgentID                  string
	ExecutionProfile         string
	Provider                 string
	RouterOrigin             string
	Model                    string
	ProviderConfigGeneration uint64
}

func (key ProbeKey) Valid() bool {
	for _, value := range []string{
		key.AgentID,
		key.ExecutionProfile,
		key.Provider,
		key.RouterOrigin,
		key.Model,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 2048 {
			return false
		}
	}
	return key.ExecutionProfile == "native_browser" &&
		key.ProviderConfigGeneration > 0
}

type ProbeResult struct {
	Supported bool
	Failure   *browserprotocol.Failure
	CheckedAt time.Time
	Duration  time.Duration
	Cached    bool
}

type ProbeFunc func(context.Context) *browserprotocol.Failure

type ProbeCache struct {
	mu         sync.Mutex
	entries    map[ProbeKey]probeEntry
	flights    map[ProbeKey]*probeFlight
	successTTL time.Duration
	failureTTL time.Duration
	now        func() time.Time
}

type probeEntry struct {
	result    ProbeResult
	expiresAt time.Time
}

type probeFlight struct {
	done   chan struct{}
	result ProbeResult
}

func NewProbeCache(
	successTTL time.Duration,
	failureTTL time.Duration,
) *ProbeCache {
	if successTTL <= 0 {
		successTTL = DefaultSuccessfulProbeTTL
	}
	if failureTTL <= 0 {
		failureTTL = DefaultFailedProbeTTL
	}
	return &ProbeCache{
		entries:    make(map[ProbeKey]probeEntry),
		flights:    make(map[ProbeKey]*probeFlight),
		successTTL: successTTL,
		failureTTL: failureTTL,
		now:        time.Now,
	}
}

func (cache *ProbeCache) Check(
	ctx context.Context,
	key ProbeKey,
	probe ProbeFunc,
) ProbeResult {
	if cache == nil || !key.Valid() || probe == nil {
		return ProbeResult{
			Failure: browserprotocol.NewFailure(
				browserprotocol.ErrorProtocolInvalid,
				"browser capability probe configuration is invalid",
				false,
			),
		}
	}
	now := cache.now()
	cache.mu.Lock()
	if entry, ok := cache.entries[key]; ok && now.Before(entry.expiresAt) {
		result := cloneProbeResult(entry.result)
		result.Cached = true
		cache.mu.Unlock()
		return result
	}
	delete(cache.entries, key)
	if flight := cache.flights[key]; flight != nil {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return ProbeResult{Failure: ContextFailure(ctx.Err())}
		case <-flight.done:
			result := cloneProbeResult(flight.result)
			result.Cached = true
			return result
		}
	}
	flight := &probeFlight{done: make(chan struct{})}
	cache.flights[key] = flight
	cache.mu.Unlock()

	started := cache.now()
	failure := probe(ctx)
	finished := cache.now()
	result := ProbeResult{
		Supported: failure == nil,
		Failure:   cloneFailure(failure),
		CheckedAt: finished.UTC(),
		Duration:  finished.Sub(started),
	}
	if result.Duration < 0 {
		result.Duration = 0
	}
	ttl := cache.successTTL
	if failure != nil {
		ttl = cache.failureTTL
	}

	cache.mu.Lock()
	flight.result = cloneProbeResult(result)
	delete(cache.flights, key)
	if ctx.Err() == nil {
		cache.entries[key] = probeEntry{
			result:    cloneProbeResult(result),
			expiresAt: finished.Add(ttl),
		}
	}
	close(flight.done)
	cache.mu.Unlock()
	return result
}

func (cache *ProbeCache) Invalidate(key ProbeKey) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	delete(cache.entries, key)
	cache.mu.Unlock()
}

func cloneProbeResult(result ProbeResult) ProbeResult {
	result.Failure = cloneFailure(result.Failure)
	return result
}

func cloneFailure(failure *browserprotocol.Failure) *browserprotocol.Failure {
	if failure == nil {
		return nil
	}
	value := *failure
	return &value
}
