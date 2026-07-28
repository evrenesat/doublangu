.PHONY: verify test test-manifest test-core-no-feature-plugins test-plugin-loader vet check-no-network

# verify runs all static checks and manifest/schema tests.
verify: vet check-no-network test-manifest

# vet runs go vet on all packages.
vet:
	go vet ./pkg/pluginapi/v1 ./internal/plugins ./cmd/doublangu-server

# test-manifest runs the manifest and schema validation tests.
test-manifest:
	go test ./pkg/pluginapi/v1 ./internal/plugins -run 'Manifest|Schema' -count=1

# test-core-no-feature-plugins verifies diagnostics, builds the binary, and
# launches it on an ephemeral loopback listener for an exact /health smoke test.
test-core-no-feature-plugins:
	go test ./internal/plugins -run 'Diagnostic|ZeroPlugin' -count=1
	go build -o /dev/null ./cmd/doublangu-server
	go test ./cmd/doublangu-server -run '^TestZeroPluginServerSmoke$$' -count=1

# test-plugin-loader runs trace-based loader tables and the real helper-process protocol.
test-plugin-loader:
	go test ./internal/plugins -run 'Loader|PreOpen|Symbol|Subprocess|Diagnostic' -count=1

# check-no-network ensures no network-dependent tooling is referenced in scoped files.
# The JSON Schema $schema and $id URLs are standard draft-07 metadata, not tool downloads.
# The Makefile itself is excluded from the npx check since it defines the check pattern.
check-no-network:
	@if rg -n 'npx' pkg/pluginapi/v1 internal/plugins contracts cmd/doublangu-server 2>/dev/null; then \
		echo "ERROR: npx references found in scoped files"; \
		exit 1; \
	fi
	@if rg -n 'https?://' pkg/pluginapi/v1 internal/plugins cmd/doublangu-server 2>/dev/null; then \
		echo "ERROR: URL references found in Go source files"; \
		exit 1; \
	fi
	@echo "No network-dependent references found."
