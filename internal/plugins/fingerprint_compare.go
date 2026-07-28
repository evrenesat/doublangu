package manifest

import (
	v1 "doublangu/pkg/pluginapi/v1"
)

// artifactParityComparator compares embedded and sidecar manifests for
// artifact parity. It excludes ArtifactChecksum from the comparison because
// that field is self-referential: the plugin binary cannot embed its own
// SHA-256 without changing it. The artifact checksum is validated pre-open
// against the file bytes independently.
type artifactParityComparator struct{}

func (artifactParityComparator) Equal(embedded, sidecar v1.Manifest) bool {
	return manifestEqualExcludingChecksum(embedded, sidecar)
}

// manifestEqualExcludingChecksum compares two manifests for equality in every
// field except ArtifactChecksum. BuildSettings, ModuleGraph, SourceRevision,
// and all identification and metadata fields must match.
func manifestEqualExcludingChecksum(a, b v1.Manifest) bool {
	// Zero out the self-referential field before comparison.
	a.ArtifactChecksum = ""
	b.ArtifactChecksum = ""
	return v1.CanonicalEquals(a, b)
}
