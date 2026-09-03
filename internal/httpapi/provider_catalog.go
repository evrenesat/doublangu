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
// never discards the last good models.
type ProviderCatalogService struct {
	mu       sync.Mutex
	ttl      time.Duration
	registry ProviderLookup
	entries  map[string]*providerCatalogEntry
}

type providerCatalogEntry struct {
	models      []annotator.Model
	retrievedAt time.Time
	lastErr     string
}

// NewProviderCatalogService creates the shared catalog over one registry with
// the five-minute freshness window.
func NewProviderCatalogService(registry ProviderLookup) *ProviderCatalogService {
	return &ProviderCatalogService{
		ttl: 5 * time.Minute, registry: registry, entries: make(map[string]*providerCatalogEntry),
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

// Snapshot returns the catalog for one provider. With refresh false and a
// fresh successful cache entry, no provider call is made. A refresh failure
// retains the last-good models and marks the snapshot stale with the
// sanitized error; without any last-good state the provider error itself is
// returned.
func (c *ProviderCatalogService) Snapshot(ctx context.Context, providerID string, refresh bool) (ProviderCatalogSnapshot, error) {
	if c == nil || c.registry == nil {
		return ProviderCatalogSnapshot{}, errors.New("provider catalog: nil registry")
	}
	provider, ok := c.registry.Provider(providerID)
	if !ok {
		return ProviderCatalogSnapshot{}, ErrProviderNotFound
	}
	c.mu.Lock()
	entry := c.entries[providerID]
	cached := !refresh && entry != nil && entry.lastErr == "" && time.Since(entry.retrievedAt) < c.ttl
	c.mu.Unlock()
	if cached {
		return c.snapshotLocked(providerID), nil
	}
	models, err := provider.ListModels(ctx)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry = c.entries[providerID]
	if err == nil {
		c.entries[providerID] = &providerCatalogEntry{models: models, retrievedAt: now}
		return c.snapshotLocked(providerID), nil
	}
	if entry != nil && len(entry.models) > 0 {
		// Last-good retention: keep the models, record the sanitized failure.
		entry.lastErr = sanitizedProviderError(err)
		c.entries[providerID] = entry
		return c.snapshotLocked(providerID), nil
	}
	return ProviderCatalogSnapshot{}, err
}

func (c *ProviderCatalogService) snapshotLocked(providerID string) ProviderCatalogSnapshot {
	entry := c.entries[providerID]
	if entry == nil {
		return ProviderCatalogSnapshot{}
	}
	return ProviderCatalogSnapshot{
		Models:      entry.models,
		RetrievedAt: entry.retrievedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		Stale:       entry.lastErr != "",
		LastError:   entry.lastErr,
	}
}
