package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"doublangu/internal/annotator"
)

// ProviderLookup is the minimal registry view the provider catalog needs.
type ProviderLookup interface {
	Provider(id string) (annotator.Provider, bool)
}

// ErrProviderNotFound reports a catalog request for a provider the registry
// does not resolve (unknown or disabled).
var ErrProviderNotFound = errors.New("provider not found")

// ProviderCatalogSnapshot is the current owner-visible catalog state for one
// provider: the last-good models, when they were retrieved, whether the most
// recent refresh failed (stale last-good), and the sanitized refresh error.
type ProviderCatalogSnapshot struct {
	Models      []annotator.Model
	RetrievedAt string
	Stale       bool
	LastError   string
}

// ProviderCatalogService keeps one five-minute last-good model catalog per
// provider id. Listing, profile validation, activation, and fresh-profile
// resolution all consume the same cache, so no caller performs a live
// provider round trip on every owner request and a transient refresh failure
// never discards the last good models. Failed attempts (including cold
// failures with no last-good state) are retained for the same window so an
// offline provider is not contacted on every read, and concurrent refreshes
// for one provider coalesce onto a single provider call.
type ProviderCatalogService struct {
	mu       sync.Mutex
	ttl      time.Duration
	registry ProviderLookup
	entries  map[string]*providerCatalogEntry
	inflight map[string]*inflightRefresh
}

type providerCatalogEntry struct {
	models      []annotator.Model
	retrievedAt time.Time
	lastErr     string
	// attemptedAt is the last refresh attempt, successful or not. It bounds
	// retries against an unhealthy provider: reads within the window serve
	// the retained success or failure state without another provider call.
	attemptedAt time.Time
}

// refreshOutcome is one coalesced refresh result published to every waiter.
type refreshOutcome struct {
	models      []annotator.Model
	err         error
	attemptedAt time.Time
}

// inflightRefresh coalesces concurrent refreshes for one provider onto the
// single ListModels call the first waiter started.
type inflightRefresh struct {
	done    chan struct{}
	outcome refreshOutcome
}

// NewProviderCatalogService creates the shared catalog over one registry with
// the five-minute freshness window.
func NewProviderCatalogService(registry ProviderLookup) *ProviderCatalogService {
	return &ProviderCatalogService{
		ttl: 5 * time.Minute, registry: registry,
		entries: make(map[string]*providerCatalogEntry), inflight: make(map[string]*inflightRefresh),
	}
}

// SetTTL overrides the freshness window (test control).
func (c *ProviderCatalogService) SetTTL(ttl time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ttl = ttl
	c.mu.Unlock()
}

// Snapshot returns the catalog for one provider. With refresh false and an
// entry attempted within the window, no provider call is made: successes
// return the last-good models, and recent failures return the retained stale
// or error state. A refresh failure retains the last-good models and marks
// the snapshot stale with the sanitized error; without any last-good state
// the sanitized failure itself is returned. Only refresh=true forces a live
// provider call, and concurrent refreshes for one provider share a single
// ListModels call.
func (c *ProviderCatalogService) Snapshot(ctx context.Context, providerID string, refresh bool) (ProviderCatalogSnapshot, error) {
	if c == nil || c.registry == nil {
		return ProviderCatalogSnapshot{}, errors.New("provider catalog: nil registry")
	}
	provider, ok := c.registry.Provider(providerID)
	if !ok {
		return ProviderCatalogSnapshot{}, ErrProviderNotFound
	}
	if snapshot, err, done := c.cached(providerID, refresh); done {
		return snapshot, err
	}
	_, ok = c.refreshCoalesced(ctx, provider, providerID)
	if !ok {
		// Our own request went away while joining the shared refresh: leave
		// it to its remaining waiters and report our cancellation. This
		// caller stores nothing.
		return ProviderCatalogSnapshot{}, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resultLocked(providerID)
}

// cached serves the retained entry when it covers this read: refresh=true
// always misses, and entries attempted outside the window always miss. All
// reads build the snapshot while holding the lock, since a concurrent
// refresh may replace the map entry or record a failure at any time.
func (c *ProviderCatalogService) cached(providerID string, refresh bool) (ProviderCatalogSnapshot, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[providerID]
	if refresh || entry == nil || time.Since(entry.attemptedAt) >= c.ttl {
		return ProviderCatalogSnapshot{}, nil, false
	}
	if entry.lastErr == "" {
		return c.snapshotLocked(providerID), nil, true
	}
	if len(entry.models) > 0 {
		return c.snapshotLocked(providerID), nil, true
	}
	return ProviderCatalogSnapshot{}, errors.New(entry.lastErr), true
}

// refreshCoalesced runs one ListModels call per provider as a service-owned
// operation detached from any single waiter, so one canceled request cannot
// fail the refresh for the others. The provider call runs asynchronously and
// every waiter — including the refresh's creator — selects between shared
// completion and its own cancellation. The operation is explicitly bounded by
// the provider's advertised request timeout; provider implementations enforce
// their own entry-derived timeouts beneath it.
func (c *ProviderCatalogService) refreshCoalesced(ctx context.Context, provider annotator.Provider, providerID string) (refreshOutcome, bool) {
	c.mu.Lock()
	if in, ok := c.inflight[providerID]; ok {
		c.mu.Unlock()
		return waitRefresh(in, ctx)
	}
	in := &inflightRefresh{done: make(chan struct{})}
	c.inflight[providerID] = in
	c.mu.Unlock()
	go c.runRefresh(provider, providerID, in)
	return waitRefresh(in, ctx)
}

// waitRefresh selects between the shared refresh completion and the
// caller's own cancellation.
func waitRefresh(in *inflightRefresh, ctx context.Context) (refreshOutcome, bool) {
	select {
	case <-in.done:
		return in.outcome, true
	case <-ctx.Done():
		return refreshOutcome{}, false
	}
}

// runRefresh executes the single coalesced provider call, commits it under
// the catalog mutex, then publishes to all waiters.
func (c *ProviderCatalogService) runRefresh(provider annotator.Provider, providerID string, in *inflightRefresh) {
	ctx := context.Background()
	cancel := context.CancelFunc(func() {})
	if timeoutMS := provider.Descriptor().RequestTimeoutMS; timeoutMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	}
	defer cancel()
	models, err := provider.ListModels(ctx)
	outcome := refreshOutcome{models: models, err: err, attemptedAt: time.Now()}
	c.mu.Lock()
	// Commit before publishing: the entry carries this refresh before any
	// waiter (or a newer refresh, which can only start after the marker is
	// gone) observes the provider, so an older refresh can never overwrite a
	// newer one.
	c.storeRefreshLocked(providerID, outcome.models, outcome.err, outcome.attemptedAt)
	in.outcome = outcome
	delete(c.inflight, providerID)
	close(in.done)
	c.mu.Unlock()
}

// storeRefreshLocked records one refresh outcome. Success replaces the
// entry; failure retains the last-good models with a sanitized error, or
// retains the cold failure itself when there is nothing to keep.
func (c *ProviderCatalogService) storeRefreshLocked(providerID string, models []annotator.Model, refreshErr error, attemptedAt time.Time) {
	if refreshErr == nil {
		c.entries[providerID] = &providerCatalogEntry{models: models, retrievedAt: attemptedAt, attemptedAt: attemptedAt}
		return
	}
	if entry := c.entries[providerID]; entry != nil && len(entry.models) > 0 {
		// Last-good retention: keep the models, record the sanitized failure.
		entry.lastErr = sanitizedProviderError(refreshErr)
		entry.attemptedAt = attemptedAt
		c.entries[providerID] = entry
		return
	}
	c.entries[providerID] = &providerCatalogEntry{lastErr: sanitizedProviderError(refreshErr), attemptedAt: attemptedAt}
}

// resultLocked converts the stored entry to its owner-visible result. It
// runs under the lock so the snapshot never races a concurrent refresh.
func (c *ProviderCatalogService) resultLocked(providerID string) (ProviderCatalogSnapshot, error) {
	entry := c.entries[providerID]
	if entry == nil {
		return ProviderCatalogSnapshot{}, errors.New("provider catalog: refresh produced no state")
	}
	if entry.lastErr == "" || len(entry.models) > 0 {
		return c.snapshotLocked(providerID), nil
	}
	return ProviderCatalogSnapshot{}, errors.New(entry.lastErr)
}

func (c *ProviderCatalogService) snapshotLocked(providerID string) ProviderCatalogSnapshot {
	entry := c.entries[providerID]
	if entry == nil {
		return ProviderCatalogSnapshot{}
	}
	// Copy the slice header so callers never alias the cached entry: a later
	// refresh replaces the entry wholesale, and the returned snapshot must
	// stay stable. (Model values themselves are never mutated in place.)
	return ProviderCatalogSnapshot{
		Models:      append([]annotator.Model(nil), entry.models...),
		RetrievedAt: entry.retrievedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		Stale:       entry.lastErr != "",
		LastError:   entry.lastErr,
	}
}
