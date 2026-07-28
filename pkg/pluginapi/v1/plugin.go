package v1

import "context"

// Plugin is the entry-point symbol exported by a native Go plugin as
// "DoublanguPlugin". The loader accepts a non-nil value implementing this
// interface, or a non-nil *Plugin whose contained interface value is non-nil.
//
// Register is called exactly once during plugin initialisation. The host
// argument provides access to all host services and registration surfaces.
// Any error returned from Register causes the entire plugin transaction to
// roll back.
type Plugin interface {
	// Manifest returns the plugin's embedded manifest. After open, it must
	// equal the sidecar manifest in every field except ArtifactChecksum; that
	// checksum is verified from artifact bytes before native loading.
	Manifest() Manifest

	// Register subscribes the plugin's capabilities with the host. All
	// registrations participate in one atomic transaction: if any
	// registration fails or a duplicate/conflict is detected, every
	// registration from this call rolls back.
	Register(ctx context.Context, host Host) error
}
