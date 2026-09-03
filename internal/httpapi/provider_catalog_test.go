package httpapi_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"doublangu/internal/annotator"
	"doublangu/internal/httpapi"
)

// countingCatalogProvider records ListModels calls and can delay, fail, or
// serve models on demand.
type countingCatalogProvider struct {
	descriptor annotator.ProviderDescriptor
	calls      atomic.Int64
	delay      time.Duration
	listErr    error
	models     []annotator.Model
}

func (p *countingCatalogProvider) Descriptor() annotator.ProviderDescriptor {
	return p.descriptor
}

func (p *countingCatalogProvider) ListModels(context.Context) ([]annotator.Model, error) {
	p.calls.Add(1)
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	if p.listErr != nil {
		return nil, p.listErr
	}
	if p.models != nil {
		return p.models, nil
	}
	return []annotator.Model{{ID: "model-a", DisplayName: "Model A"}}, nil
}

func (p *countingCatalogProvider) OpenSession(context.Context, annotator.ResolvedBinding) (annotator.Session, error) {
	return nil, errors.New("no sessions in catalog tests")
}

func countingCatalogRegistry(provider *countingCatalogProvider) *apiFakeRegistry {
	return &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{provider.descriptor},
		providers:   map[string]annotator.Provider{provider.descriptor.ID: provider},
	}
}

// TestProviderCatalogConcurrentReadAndRefresh proves the cached snapshot path
// is safe against a concurrent provider refresh: cached reads must observe a
// stable entry even while refreshes replace it. Run with -race.
func TestProviderCatalogConcurrentReadAndRefresh(t *testing.T) {
	registry := &apiFakeRegistry{
		descriptors: []annotator.ProviderDescriptor{codexDescriptor()},
		providers:   map[string]annotator.Provider{"codex-app-server": &apiFakeProvider{descriptor: codexDescriptor()}},
	}
	catalog := httpapi.NewProviderCatalogService(registry)
	ctx := context.Background()

	// Prime the cache with one successful refresh.
	if _, err := catalog.Snapshot(ctx, "codex-app-server", true); err != nil {
		t.Fatalf("prime catalog: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				snapshot, err := catalog.Snapshot(ctx, "codex-app-server", false)
				if err != nil {
					t.Errorf("cached snapshot: %v", err)
					return
				}
				if len(snapshot.Models) != 1 || snapshot.RetrievedAt == "" {
					t.Errorf("cached snapshot = %+v", snapshot)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := catalog.Snapshot(ctx, "codex-app-server", true); err != nil {
					t.Errorf("refresh snapshot: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestProviderCatalogCachesFailedAttempts proves an offline provider is not
// contacted on every read: a cold failure is retained and served without
// another provider call until the window expires, and refresh=true stays the
// explicit bypass.
func TestProviderCatalogCachesFailedAttempts(t *testing.T) {
	provider := &countingCatalogProvider{
		descriptor: codexDescriptor(), listErr: errors.New("catalog offline"),
	}
	catalog := httpapi.NewProviderCatalogService(countingCatalogRegistry(provider))
	catalog.SetTTL(50 * time.Millisecond)
	ctx := context.Background()

	if _, err := catalog.Snapshot(ctx, "codex-app-server", false); err == nil {
		t.Fatal("cold failure = nil, want error")
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	// Immediate rereads serve the retained cold failure without redialing.
	for i := 0; i < 3; i++ {
		if _, err := catalog.Snapshot(ctx, "codex-app-server", false); err == nil {
			t.Fatal("reread = nil, want retained error")
		}
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("calls after rereads = %d, want 1", got)
	}
	// refresh=true bypasses the retained failure.
	if _, err := catalog.Snapshot(ctx, "codex-app-server", true); err == nil {
		t.Fatal("refresh = nil, want error")
	}
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("calls after refresh = %d, want 2", got)
	}
	// After the window expires, reads retry the provider once.
	time.Sleep(60 * time.Millisecond)
	if _, err := catalog.Snapshot(ctx, "codex-app-server", false); err == nil {
		t.Fatal("expired reread = nil, want error")
	}
	if got := provider.calls.Load(); got != 3 {
		t.Fatalf("calls after expiry = %d, want 3", got)
	}
}

// TestProviderCatalogCoalescesConcurrentRefreshes proves concurrent refreshes
// for one provider share a single ListModels call instead of each spawning
// its own provider round trip. The provider delay dwarfs goroutine wakeup
// skew, so every waiter joins the in-flight refresh; without coalescing each
// waiter would start its own call.
func TestProviderCatalogCoalescesConcurrentRefreshes(t *testing.T) {
	provider := &countingCatalogProvider{descriptor: codexDescriptor(), delay: 300 * time.Millisecond}
	catalog := httpapi.NewProviderCatalogService(countingCatalogRegistry(provider))
	ctx := context.Background()

	const waiters = 8
	started := make(chan struct{})
	results := make([]error, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-started
			_, results[index] = catalog.Snapshot(ctx, "codex-app-server", true)
		}(i)
	}
	close(started)
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Fatalf("waiter %d: %v", i, err)
		}
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 coalesced refresh", got)
	}
}

// TestProviderCatalogCanceledJoinerKeepsSharedRefresh proves a joiner that
// disconnects mid-refresh neither fails the shared provider call nor stores
// anything: it reports its own cancellation while the finisher still
// succeeds, exactly one provider call runs, and the entry carries the
// success.
func TestProviderCatalogCanceledJoinerKeepsSharedRefresh(t *testing.T) {
	provider := &countingCatalogProvider{descriptor: codexDescriptor(), delay: 300 * time.Millisecond}
	catalog := httpapi.NewProviderCatalogService(countingCatalogRegistry(provider))

	finisherDone := make(chan error, 1)
	go func() {
		_, err := catalog.Snapshot(context.Background(), "codex-app-server", true)
		finisherDone <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for provider.calls.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not start")
		}
		time.Sleep(time.Millisecond)
	}
	joinerCtx, joinerCancel := context.WithCancel(context.Background())
	joinerDone := make(chan error, 1)
	go func() {
		_, err := catalog.Snapshot(joinerCtx, "codex-app-server", true)
		joinerDone <- err
	}()
	// The joiner reaches the in-flight refresh far inside the provider
	// delay, then disconnects while the finisher is still running.
	time.Sleep(100 * time.Millisecond)
	joinerCancel()
	if err := <-joinerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("joiner = %v, want context.Canceled", err)
	}
	if err := <-finisherDone; err != nil {
		t.Fatalf("finisher = %v, want success", err)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	snapshot, err := catalog.Snapshot(context.Background(), "codex-app-server", false)
	if err != nil || len(snapshot.Models) != 1 {
		t.Fatalf("retained snapshot = %+v err=%v", snapshot, err)
	}
}

// TestProviderCatalogOverlappingRefreshesShareOneResult proves a second
// refresh started while the first is in flight joins it instead of storing a
// newer result the first could overwrite: both waiters observe the same
// models from the single provider call.
func TestProviderCatalogOverlappingRefreshesShareOneResult(t *testing.T) {
	provider := &countingCatalogProvider{
		descriptor: codexDescriptor(),
		delay:      200 * time.Millisecond,
		models:     []annotator.Model{{ID: "model-a", DisplayName: "Model A"}},
	}
	catalog := httpapi.NewProviderCatalogService(countingCatalogRegistry(provider))
	ctx := context.Background()

	started := make(chan struct{})
	snapshots := make([]httpapi.ProviderCatalogSnapshot, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-started
			snapshot, err := catalog.Snapshot(ctx, "codex-app-server", true)
			if err != nil {
				t.Errorf("waiter %d: %v", index, err)
				return
			}
			snapshots[index] = snapshot
		}(i)
	}
	close(started)
	wg.Wait()
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	for i, snapshot := range snapshots {
		if len(snapshot.Models) != 1 || snapshot.Models[0].ID != "model-a" {
			t.Fatalf("waiter %d snapshot = %+v", i, snapshot)
		}
	}
}
