package v1

// Provenance records the origin and processing history of a derived
// artifact. Every generated item (translation, explanation, TTS audio,
// lesson) carries provenance so the owner can trace it back through the
// pipeline.
type Provenance struct {
	// PluginID identifies the plugin that produced the artifact.
	PluginID string `json:"plugin_id"`

	// PluginVersion is the SemVer of the plugin at production time.
	PluginVersion string `json:"plugin_version"`

	// Capability declares which capability was used.
	Capability CapabilityID `json:"capability"`

	// ProviderID identifies the specific provider within the plugin, if
	// applicable.
	ProviderID string `json:"provider_id,omitempty"`

	// ModelID identifies the AI model or algorithm version used, if
	// applicable.
	ModelID string `json:"model_id,omitempty"`

	// Parameters records provider-specific settings used for this
	// invocation (e.g. voice, speed, temperature).
	Parameters map[string]string `json:"parameters,omitempty"`

	// InputHashes lists the SHA-256 digests of the inputs that produced
	// this artifact. The order matches the provider's input order.
	InputHashes []string `json:"input_hashes,omitempty"`

	// Timestamp is the Unix-millisecond time when the artifact was
	// produced.
	Timestamp int64 `json:"timestamp"`
}

// StaleReason records why a previously generated artifact is now stale.
type StaleReason string

const (
	// StaleUpstreamEdit means an input transcript was edited.
	StaleUpstreamEdit StaleReason = "upstream_edit"

	// StaleProviderChange means the provider or its version changed.
	StaleProviderChange StaleReason = "provider_change"

	// StaleParameterChange means provider parameters changed.
	StaleParameterChange StaleReason = "parameter_change"

	// StaleManualInvalidation means the owner explicitly invalidated the
	// artifact.
	StaleManualInvalidation StaleReason = "manual_invalidation"
)

// StaleMarker is attached to an artifact when it becomes stale. The
// artifact history (previous provenance + content) is preserved; this
// marker is an additional record, not a replacement.
type StaleMarker struct {
	// Reason is the cause of invalidation.
	Reason StaleReason `json:"reason"`

	// Timestamp is when the artifact was marked stale.
	Timestamp int64 `json:"timestamp"`

	// PreviousProvenance references the provenance of the now-stale
	// artifact.
	PreviousProvenance Provenance `json:"previous_provenance"`
}
