package v1

import (
	"context"
	"net/http"
)

// Host provides the plugin's connection to core services and registration
// surfaces. It is passed to Plugin.Register and must not be retained beyond
// the lifetime of that call.
type Host interface {
	// Settings returns a namespaced key-value store scoped to the calling
	// plugin. The namespace is derived from the plugin ID; it may be empty
	// for shared defaults.
	Settings() Settings

	// Library provides read access to the library catalog (titles, authors,
	// languages, media references). Write access is mediated through import
	// and job handlers.
	Library() Library

	// Blobs provides content-addressed immutable blob storage. Blobs are
	// deduplicated by SHA-256; storing the same bytes twice returns the
	// existing digest.
	Blobs() BlobStore

	// Logger returns a structured logger for the calling plugin. Log entries
	// are automatically tagged with the plugin ID.
	Logger() Logger

	// HTTPClient returns an HTTP client configured with host-level timeouts,
	// redirect policies, and TLS settings. Plugins must use this client
	// rather than creating their own.
	HTTPClient() *http.Client

	// EventBus returns a publish-only event bus. Plugins may publish events
	// but not subscribe directly; event handler registration occurs through
	// RegisterEventHandler.
	EventBus() EventBus

	// RegisterProvider registers a capability provider. The provider is
	// identified by a stable ID and bound to a single CapabilityID.
	// Duplicate provider IDs or conflicting capabilities cause a rollback.
	RegisterProvider(reg ProviderRegistration) error

	// RegisterTransformer registers a data transformer. Transformers are
	// ordered by ascending priority, then plugin ID, then handler ID.
	RegisterTransformer(reg TransformerRegistration) error

	// RegisterValidator registers a validator. All validators run for each
	// relevant input; failures are aggregated and sorted by plugin ID and
	// validator ID.
	RegisterValidator(reg ValidatorRegistration) error

	// RegisterObserver registers a post-commit observer. Observers fire
	// after the transaction commits; idempotency is guaranteed through the
	// tuple (pluginID, observerID, eventID).
	RegisterObserver(reg ObserverRegistration) error

	// RegisterJobHandler registers a durable job handler. Job handlers are
	// identified by a stable job-type ID and participate in the same
	// plugin transaction.
	RegisterJobHandler(reg JobHandlerRegistration) error

	// RegisterEventHandler registers an event subscription. The subscription
	// participates in the same plugin transaction; duplicate subscriptions
	// cause a rollback.
	RegisterEventHandler(reg EventHandlerRegistration) error

	// RegisterCommand registers a named command that can be invoked through
	// the API or UI.
	RegisterCommand(reg CommandRegistration) error

	// RegisterUI registers a UI contribution (panel, view, or widget) that
	// is rendered in the web shell.
	RegisterUI(reg UIRegistration) error
}

// Settings is a namespaced persistent key-value store. Each plugin has its
// own isolated namespace; keys are arbitrary strings and values are opaque
// byte slices.
type Settings interface {
	// Get returns the value for key, or nil if not set.
	Get(key string) (ImmutableBytes, error)

	// Set stores value under key. The stored bytes are a copy; the caller
	// retains ownership of the input slice.
	Set(key string, value []byte) error

	// Delete removes the key. Deleting a missing key is a no-op.
	Delete(key string) error

	// List returns all keys in the namespace, sorted lexicographically.
	List() ([]string, error)
}

// Library provides read access to the media library.
type Library interface {
	// ListTitles returns library titles matching the optional filter.
	// A nil filter matches all titles; pagination uses offset/limit.
	ListTitles(ctx context.Context, filter *LibraryFilter, offset, limit int) ([]LibraryTitle, error)

	// GetTitle returns a single title by its core ID (ULID).
	GetTitle(ctx context.Context, id string) (*LibraryTitle, error)
}

// LibraryFilter constrains a library title query.
type LibraryFilter struct {
	// SourceLanguage, if non-empty, filters titles whose primary source
	// language equals this BCP-47 tag.
	SourceLanguage Language `json:"source_language,omitempty"`

	// TargetLanguage, if non-empty, filters titles whose primary target
	// language equals this BCP-47 tag.
	TargetLanguage Language `json:"target_language,omitempty"`

	// Query is a free-text search against title and author fields.
	Query string `json:"query,omitempty"`
}

// LibraryTitle is a row from the library catalog.
type LibraryTitle struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Author         string   `json:"author"`
	SourceLanguage Language `json:"source_language"`
	TargetLanguage Language `json:"target_language"`
	MediaCount     int      `json:"media_count"`
	CreatedAt      int64    `json:"created_at"` // Unix milliseconds
	UpdatedAt      int64    `json:"updated_at"` // Unix milliseconds
}

// BlobStore provides content-addressed immutable blob storage.
type BlobStore interface {
	// Put stores data and returns its SHA-256 hex digest. Storing the same
	// bytes twice returns the existing digest without rewriting.
	Put(ctx context.Context, data []byte) (digest string, err error)

	// Get retrieves the blob identified by digest.
	Get(ctx context.Context, digest string) (ImmutableBytes, error)

	// Exists reports whether a blob with the given digest is stored.
	Exists(ctx context.Context, digest string) (bool, error)
}

// Logger is a structured logging interface. All methods accept a message and
// optional key-value pairs (which must alternate key, value).
type Logger interface {
	Debug(msg string, keyvals ...interface{})
	Info(msg string, keyvals ...interface{})
	Warn(msg string, keyvals ...interface{})
	Error(msg string, keyvals ...interface{})
}

// EventBus is a publish-only channel for plugin events. Plugins publish
// events; subscription and dispatch are managed by the host.
type EventBus interface {
	// Publish emits an event. The event is delivered to matching
	// subscribers after the current transaction commits.
	Publish(ctx context.Context, event Event) error
}

// --- io interfaces used by handler types ---

// ReadCloser is a subset of io.ReadCloser used in handler signatures so
// plugins do not need to import io directly.
type ReadCloser interface {
	Read(p []byte) (n int, err error)
	Close() error
}
