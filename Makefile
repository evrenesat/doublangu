.PHONY: verify test test-manifest vet check-no-network

# verify runs all static checks and manifest/schema tests.
verify: vet check-no-network test-manifest

# vet runs go vet on all packages.
vet:
	go vet ./pkg/pluginapi/v1 ./internal/plugins

# test-manifest runs the manifest and schema validation tests.
test-manifest:
	go test ./pkg/pluginapi/v1 ./internal/plugins -run 'Manifest|Schema' -count=1

# check-no-network ensures no network-dependent tooling is referenced in scoped files.
# The JSON Schema $schema and $id URLs are standard draft-07 metadata, not tool downloads.
# The Makefile itself is excluded from the npx check since it defines the check pattern.
check-no-network:
	@if rg -n 'npx' pkg/pluginapi/v1 internal/plugins contracts 2>/dev/null; then \
		echo "ERROR: npx references found in scoped files"; \
		exit 1; \
	fi
	@if rg -n 'https?://' pkg/pluginapi/v1 internal/plugins 2>/dev/null; then \
		echo "ERROR: URL references found in Go source files"; \
		exit 1; \
	fi
	@echo "No network-dependent references found."
