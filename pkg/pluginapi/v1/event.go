package v1

import "time"

// Event is an immutable notification published through the EventBus. Events
// have stable IDs for idempotency and carry a typed payload.
type Event struct {
	// ID is a stable, unique event identifier (ULID). Observers and event
	// handlers use this for idempotency.
	ID string `json:"id"`

	// Type is a dotted event-type string (e.g. "library.import.created").
	Type string `json:"type"`

	// Source identifies the plugin or core component that published the
	// event.
	Source string `json:"source"`

	// Timestamp is the Unix-millisecond time when the event was created.
	Timestamp int64 `json:"timestamp"`

	// Payload is the event-specific data. Observers and handlers receive
	// an immutable copy.
	Payload ImmutableBytes `json:"payload"`
}

// NewEvent creates an Event with the current time. The caller retains
// ownership of payload; it is copied.
func NewEvent(id, eventType, source string, payload []byte) Event {
	return Event{
		ID:        id,
		Type:      eventType,
		Source:    source,
		Timestamp: time.Now().UnixMilli(),
		Payload:   NewImmutableBytes(payload),
	}
}
