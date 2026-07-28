// Sample plugin for Doublangu. This is a minimal plugin that demonstrates
// the contract: it exports DoublanguPlugin, returns its Manifest, and
// registers a single provider during Register.
//
// The fingerprint and target fields are injected at build time by the
// pluginbuild tool via -ldflags -X. ArtifactChecksum is self-referential
// and remains a zero placeholder; the build tool writes the real checksum
// to the sidecar only.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	v1 "doublangu/pkg/pluginapi/v1"
)

// Injected at build time by pluginbuild via ldflags -X.
var (
	buildSettingsHash = "0000000000000000000000000000000000000000000000000000000000000000"
	moduleGraphHash   = "0000000000000000000000000000000000000000000000000000000000000000"
	artifactChecksum  = "0000000000000000000000000000000000000000000000000000000000000000"
	sourceRevision    = "unknown"
	targetJSON        = `["agent","server"]`
)

var DoublanguPlugin v1.Plugin = SamplePlugin{}

type SamplePlugin struct{}

func (SamplePlugin) Manifest() v1.Manifest {
	var target []string
	if err := json.Unmarshal([]byte(targetJSON), &target); err != nil {
		target = []string{"agent", "server"}
	}
	return v1.Manifest{
		ID:               "sample",
		Version:          "0.1.0",
		APIVersion:       v1.APIVersion,
		GoVersion:        v1.GoVersion,
		Target:           target,
		SourceRevision:   sourceRevision,
		ArtifactChecksum: artifactChecksum,
		BuildSettings:    buildSettingsHash,
		ModuleGraph:      moduleGraphHash,
		Name:             "sample",
		Description:      "sample plugin (built by pluginbuild)",
		Author:           "Doublangu",
	}
}

func (SamplePlugin) Register(ctx context.Context, host v1.Host) error {
	if host == nil {
		return fmt.Errorf("host must not be nil")
	}
	return host.RegisterProvider(v1.ProviderRegistration{
		ID:         "sample.greeting",
		Capability: v1.CapTranslation,
		Name:       "Sample Greeting Provider",
		Priority:   100,
		Provider:   nil,
	})
}
