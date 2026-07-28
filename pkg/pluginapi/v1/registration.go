package v1

import "context"

// --- Provider ---

// ProviderRegistration registers a capability provider with the host.
// The Provider field implements the capability-specific handler interface.
// Duplicate IDs or conflicting capabilities within a plugin transaction
// trigger a full rollback.
type ProviderRegistration struct {
	// ID is a stable, unique identifier for this provider instance.
	ID string `json:"id"`

	// Capability declares which capability this provider implements.
	Capability CapabilityID `json:"capability"`

	// Name is a human-readable label shown in the owner UI.
	Name string `json:"name"`

	// Priority orders providers when multiple exist for the same
	// capability; lower values are preferred.
	Priority Priority `json:"priority"`

	// Provider is the handler implementation. The concrete type must
	// match the capability-specific interface for Capability.
	// The host performs type-assertion during registration.
	Provider interface{} `json:"-"`
}

// --- Transformer ---

// TransformerRegistration registers a data transformer. Transformers receive
// immutable input and must produce new output without mutating the input.
type TransformerRegistration struct {
	// ID is a stable identifier for this transformer instance.
	ID string `json:"id"`

	// Capability declares which capability this transformer serves.
	Capability CapabilityID `json:"capability"`

	// Priority orders transformers; lower values run first. Ties are
	// broken by plugin ID, then transformer ID.
	Priority Priority `json:"priority"`

	// Transformer is the handler implementation. It receives immutable
	// input bytes and returns new output bytes.
	Transformer Transformer `json:"-"`
}

// Transformer transforms immutable input into new output. Implementations
// must not mutate the input slice.
type Transformer interface {
	Transform(ctx context.Context, input ImmutableBytes) (ImmutableBytes, error)
}

// --- Validator ---

// ValidatorRegistration registers a validator. All validators for a given
// capability run on each input; failures are aggregated.
type ValidatorRegistration struct {
	// ID is a stable identifier for this validator instance.
	ID string `json:"id"`

	// Capability declares which capability this validator applies to.
	Capability CapabilityID `json:"capability"`

	// Validator is the handler implementation.
	Validator Validator `json:"-"`
}

// ValidationResult describes a single validation finding.
type ValidationResult struct {
	// ValidatorID identifies the validator that produced this result.
	ValidatorID string `json:"validator_id"`

	// PluginID identifies the plugin that owns the validator.
	PluginID string `json:"plugin_id"`

	// Severity is one of "error", "warning", or "info".
	Severity string `json:"severity"`

	// Message describes the finding in human-readable form.
	Message string `json:"message"`

	// Span identifies the byte range [Start, End) in the input that
	// this finding applies to, or nil if not applicable.
	Span *ByteSpan `json:"span,omitempty"`
}

// ByteSpan is a half-open byte range [Start, End).
type ByteSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Validator inspects data and returns zero or more findings.
type Validator interface {
	Validate(ctx context.Context, input ImmutableBytes) ([]ValidationResult, error)
}

// --- Observer ---

// ObserverRegistration registers a post-commit observer. Observers fire
// after the plugin transaction commits. Idempotency is guaranteed through
// the tuple (pluginID, observerID, eventID).
type ObserverRegistration struct {
	// ID is a stable identifier for this observer instance.
	ID string `json:"id"`

	// EventTypes lists the event type strings this observer is
	// interested in. An empty list subscribes to all event types.
	EventTypes []string `json:"event_types,omitempty"`

	// Observer is the handler implementation.
	Observer Observer `json:"-"`
}

// Observer receives events after the originating transaction commits.
// Implementations must be idempotent: the host may redeliver events.
type Observer interface {
	OnEvent(ctx context.Context, event Event) error
}

// --- Job Handler ---

// JobHandlerRegistration registers a durable job handler. Job handlers are
// identified by a stable job-type ID.
type JobHandlerRegistration struct {
	// JobType is a stable identifier for the job type this handler
	// processes. The core routes jobs by type.
	JobType string `json:"job_type"`

	// Handler is the handler implementation.
	Handler JobHandler `json:"-"`
}

// JobHandler processes a durable job. Jobs are lease-based, idempotent,
// cancelable, and safe across server restart.
type JobHandler interface {
	// HandleJob processes a job. The payload contains job-specific data.
	// An error returned from HandleJob marks the job as failed; the host
	// may retry according to the job's retry policy.
	HandleJob(ctx context.Context, payload ImmutableBytes) (result ImmutableBytes, err error)
}

// --- Event Handler ---

// EventHandlerRegistration registers an event subscription. The subscription
// participates in the same plugin transaction; duplicate subscriptions cause
// a rollback.
type EventHandlerRegistration struct {
	// EventTypes lists the event type strings this handler subscribes to.
	// An empty list subscribes to all event types.
	EventTypes []string `json:"event_types"`

	// Handler is the handler implementation.
	Handler EventHandler `json:"-"`
}

// EventHandler receives events dispatched by the host. Unlike observers,
// event handlers receive events synchronously during dispatch.
type EventHandler interface {
	HandleEvent(ctx context.Context, event Event) error
}

// --- Command ---

// CommandRegistration registers a named command invocable through the API
// or UI.
type CommandRegistration struct {
	// ID is a stable, unique command identifier.
	ID string `json:"id"`

	// Label is a human-readable label for UI display.
	Label string `json:"label"`

	// Description explains what the command does.
	Description string `json:"description"`

	// Category groups commands in the UI (e.g. "import", "export").
	Category string `json:"category"`

	// Handler is the command implementation.
	Handler CommandHandler `json:"-"`
}

// CommandInput carries the parameters for a command invocation.
type CommandInput struct {
	// Args are positional string arguments.
	Args []string `json:"args,omitempty"`

	// Payload is an optional binary payload.
	Payload ImmutableBytes `json:"payload"`
}

// CommandOutput carries the result of a command invocation.
type CommandOutput struct {
	// Body is the command's text output.
	Body string `json:"body"`

	// Payload is an optional binary result.
	Payload ImmutableBytes `json:"payload"`
}

// CommandHandler executes a named command.
type CommandHandler interface {
	Execute(ctx context.Context, input CommandInput) (CommandOutput, error)
}

// --- UI ---

// UIRegistration registers a UI contribution rendered in the web shell.
type UIRegistration struct {
	// ID is a stable, unique identifier for this UI component.
	ID string `json:"id"`

	// Label is a human-readable label for navigation and headings.
	Label string `json:"label"`

	// Type declares the UI surface kind: "panel", "view", or "widget".
	Type UIType `json:"type"`

	// Container is the parent container ID, or empty for top-level.
	Container string `json:"container,omitempty"`

	// Priority orders UI contributions within a container; lower values
	// appear first.
	Priority Priority `json:"priority"`

	// Icon is an optional material-icon or SVG identifier.
	Icon string `json:"icon,omitempty"`

	// SourceURL is the URL of the Svelte component bundle that renders
	// this UI contribution. The host loads it as a same-origin ES module.
	SourceURL string `json:"source_url"`
}

// UIType declares the kind of UI surface being registered.
type UIType string

const (
	UITypePanel  UIType = "panel"
	UITypeView   UIType = "view"
	UITypeWidget UIType = "widget"
)
