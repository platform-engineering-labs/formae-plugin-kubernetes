# Formae Plugin Makefile
#
# Targets:
#   build   - Build the plugin binary
#   test    - Run tests
#   lint    - Run linter
#   clean   - Remove build artifacts
#   install - Build and install plugin locally (binary + schema + manifest)

# Plugin metadata - extracted from formae-plugin.pkl
PLUGIN_NAME := $(shell pkl eval -x 'name' formae-plugin.pkl 2>/dev/null || echo "example")
PLUGIN_VERSION := $(shell pkl eval -x 'version' formae-plugin.pkl 2>/dev/null || echo "0.0.0")
PLUGIN_NAMESPACE := $(shell pkl eval -x 'namespace' formae-plugin.pkl 2>/dev/null || echo "EXAMPLE")

# Build settings
GO := go
GOFLAGS := -trimpath
BINARY := $(PLUGIN_NAME)

# Formae binary - auto-detect from local build or allow override
# Usage: make conformance-test FORMAE_BINARY=/path/to/formae
# Looks for: ../../formae/bin/formae (sibling to plugins/), then formae in $PATH
FORMAE_BINARY ?= $(shell realpath $(firstword $(wildcard $(CURDIR)/../../formae/bin/formae) $(shell command -v formae 2>/dev/null)) 2>/dev/null)

# Installation paths
# Plugin discovery expects lowercase directory names matching the plugin name
PLUGIN_BASE_DIR := $(HOME)/.pel/formae/plugins
INSTALL_DIR := $(PLUGIN_BASE_DIR)/$(PLUGIN_NAME)/v$(PLUGIN_VERSION)

.PHONY: all build test test-unit test-integration lint verify-schema clean install install-versioned help setup-credentials clean-environment conformance-test conformance-test-crud conformance-test-discovery conformance-test-crud-run conformance-test-discovery-run conformance-test-resources conformance-test-charts generate-schema chart-test drift-test

all: build

## build: Build the plugin binary and update manifest
build:
	@mkdir -p schema/pkl && echo "$(PLUGIN_VERSION)" > schema/pkl/VERSION
	$(GO) build $(GOFLAGS) -o bin/$(BINARY) .
	@MIN_VERSION=$$($(GO) list -m -f '{{.Dir}}' github.com/platform-engineering-labs/formae/pkg/plugin 2>/dev/null | xargs -I{} grep 'MinFormaeVersion' {}/version.go 2>/dev/null | grep -oE '"[0-9]+\.[0-9]+\.[0-9]+"' | tr -d '"'); \
	if [ -n "$$MIN_VERSION" ]; then \
		echo "Updating minFormaeVersion to $$MIN_VERSION"; \
		if [ "$$(uname)" = "Darwin" ]; then \
			sed -i '' 's/^minFormaeVersion = .*/minFormaeVersion = "'"$$MIN_VERSION"'"/' formae-plugin.pkl; \
		else \
			sed -i 's/^minFormaeVersion = .*/minFormaeVersion = "'"$$MIN_VERSION"'"/' formae-plugin.pkl; \
		fi; \
	fi

## test: Run all tests
test:
	$(GO) test -v ./...

## test-unit: Run unit tests only (tests with //go:build unit tag)
test-unit:
	$(GO) test -v -tags=unit ./...

## test-integration: Run integration tests (requires cloud credentials)
## Add tests with //go:build integration tag
test-integration:
	$(GO) test -v -tags=integration ./...

## lint: Run golangci-lint
lint:
	golangci-lint run

## verify-schema: Validate PKL schema files
## Checks that schema files are well-formed and follow formae conventions.
verify-schema:
	$(GO) run github.com/platform-engineering-labs/formae/pkg/plugin/testutil/cmd/verify-schema --namespace $(PLUGIN_NAMESPACE) ./schema/pkl/main

## generate-versioned-schemas: Emit per-K8s-version schema trees from schema/pkl/main/.
## Uses pkl:reflect to discover every `@K8sVersion`-annotated property/class/
## module, then drops anything outside each target's window. Output goes to
## schema/pkl/generated/v<minor>/. The list of K8s minors is read from
## tools/gen-versioned-reflect/versions.pkl (override with K8S_VERSIONS=...).
K8S_VERSIONS ?= $(shell pkl eval tools/gen-versioned-reflect/versions.pkl | tr '\n' ' ')
generate-versioned-schemas: generate-versioned-pkl-schemas generate-versioned-testdata

## generate-versioned-pkl-schemas: Emit unified PKL schema tree containing
## all supported K8s versions under one PklProject.
generate-versioned-pkl-schemas:
	@# Master PklProject.deps.json is gitignored — regenerate so the
	@# pkl:reflect-driven discover.pkl can resolve `@formae` imports.
	@pkl project resolve schema/pkl/main >/dev/null
	@rm -rf schema/pkl/generated
	$(GO) run ./tools/gen-versioned-reflect $(addprefix --target=,$(K8S_VERSIONS)) \
		--in=schema/pkl/main --out-dir=schema/pkl/generated
	@pkl project resolve schema/pkl/generated >/dev/null

## generate-versioned-testdata: Emit per-K8s-version conformance fixtures.
## Depends on generate-versioned-pkl-schemas (the testdata PklProject points
## at schema/pkl/generated/v<X.Y>, so schemas must exist first).
generate-versioned-testdata:
	@rm -rf testdata/generated
	$(GO) run ./tools/gen-versioned-testdata $(addprefix --target=,$(K8S_VERSIONS)) \
		--in=testdata/main --out-dir=testdata/generated

## list-k8s-versions: Print the K8s minors that will be generated.
list-k8s-versions:
	@echo $(K8S_VERSIONS)

## verify-generated-schemas: Regenerate and confirm no diff vs committed output.
## Used in CI to ensure schema/pkl/generated/ stays in sync with schema/pkl/main/.
## Fails if the generator produces output different from what's committed.
verify-generated-schemas: generate-versioned-schemas
	@if ! git diff --exit-code --quiet schema/pkl/generated/ testdata/generated/; then \
		echo "ERROR: schema/pkl/generated/ or testdata/generated/ is out of date. Run 'make generate-versioned-schemas' and commit." >&2; \
		git diff --stat schema/pkl/generated/ testdata/generated/ >&2; \
		exit 1; \
	fi
	@echo "schema/pkl/generated/ and testdata/generated/ are up to date"

## verify-fixtures: Pkl-eval every generated feature fixture across every K8s
## version. Catches schema drift between main/ and a feature .pkl that
## references a removed/renamed type. Skipped pre-existing CRD fixtures
## have a known evaluation issue unrelated to the version-pilot work.
verify-fixtures:
	@FORMAE_TEST_RUN_ID=verify-local; \
	export FORMAE_TEST_RUN_ID; \
	failed=0; total=0; \
	for v in $(K8S_VERSIONS); do \
		for f in $$(find testdata/generated/v$$v/features -name '*.pkl' 2>/dev/null); do \
			total=$$((total+1)); \
			if ! pkl eval --project-dir testdata/generated/v$$v "$$f" >/dev/null 2>&1; then \
				echo "FAIL v$$v: $$f"; \
				failed=$$((failed+1)); \
			fi; \
		done; \
	done; \
	echo "Evaluated $$total feature fixtures across $(words $(K8S_VERSIONS)) K8s versions; $$failed failures"; \
	[ $$failed -eq 0 ]

## package-versioned-schemas: Build the unified Pkl package zip for upload
## to the package hub. Output goes to dist/pkl-packages/.
## One zip per plugin install — all K8s versions are subtrees of the same
## package, so per-minor zips are no longer produced.
package-versioned-schemas: generate-versioned-schemas
	@rm -rf dist/pkl-packages
	@mkdir -p dist/pkl-packages
	@pkl project package --output-path dist/pkl-packages/ schema/pkl/generated >/dev/null
	@ls dist/pkl-packages/

## clean: Remove build artifacts
clean:
	rm -rf bin/ dist/

## install: Build and install plugin locally (binary + schema + manifest)
## Installs to ~/.pel/formae/plugins/<name>/v<version>/
## Removes any existing versions of the plugin first to ensure clean state.
## INSTALL_K8S_VERSION: the generated K8s schema version installed at the top
## level of $(INSTALL_DIR)/schema/pkl/. Defaults to the highest version in
## tools/gen-versioned-reflect/versions.pkl. Override at the command line
## (e.g., `make install INSTALL_K8S_VERSION=1.32`) or via the matrix CI step
## to install a specific version's PKL schema.
INSTALL_K8S_VERSION ?= $(lastword $(K8S_VERSIONS))

install: build
	@echo "Installing $(PLUGIN_NAME) v$(PLUGIN_VERSION) (namespace: $(PLUGIN_NAMESPACE))..."
	@if [ ! -d schema/pkl/generated/v$(INSTALL_K8S_VERSION) ]; then \
		echo "Generating versioned schemas (need v$(INSTALL_K8S_VERSION))..."; \
		$(MAKE) generate-versioned-pkl-schemas; \
	fi
	@rm -rf $(PLUGIN_BASE_DIR)/$(PLUGIN_NAME)
	@mkdir -p $(INSTALL_DIR)/schema/pkl
	@cp bin/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@# Install schema/pkl/generated/v<INSTALL_K8S_VERSION>/. at the top level so
	@# formae's extract sees a clean schema (no @K8sVersion annotations).
	@# We deliberately do NOT install schema/pkl/generated/ alongside —
	@# formae's extract walks the entire schema dir to resolve type modules,
	@# and a second copy of k8s.pkl under generated/v<X.Y>/ produces an
	@# import alias like 'v1.34_k8s' which has a '.' that breaks Pkl
	@# identifier rules. Per-version generated trees are for external
	@# consumers (Pkl package registry), not runtime install.
	@cp -r schema/pkl/generated/v$(INSTALL_K8S_VERSION)/. $(INSTALL_DIR)/schema/pkl/
	@# The installed PklProject must use package name "k8s" (matching the
	@# plugin namespace) so formae's extract resolves type modules under
	@# the canonical name. The versioned name "k8s-v<X-Y>" is for external
	@# consumers (Pkl package registry); inside the install dir it would
	@# break extract's name → identifier resolution.
	@if [ "$$(uname)" = "Darwin" ]; then \
		sed -i '' 's/name = "k8s-v[0-9]*-[0-9]*"/name = "k8s"/' $(INSTALL_DIR)/schema/pkl/PklProject; \
	else \
		sed -i 's/name = "k8s-v[0-9]*-[0-9]*"/name = "k8s"/' $(INSTALL_DIR)/schema/pkl/PklProject; \
	fi
	@# Re-resolve deps after the rewrite so PklProject.deps.json reflects
	@# the canonical package name.
	@pkl project resolve $(INSTALL_DIR)/schema/pkl >/dev/null
	@if [ -f schema/Config.pkl ]; then cp schema/Config.pkl $(INSTALL_DIR)/schema/; fi
	@cp formae-plugin.pkl $(INSTALL_DIR)/
	@echo "Installed to $(INSTALL_DIR) (K8s schema: v$(INSTALL_K8S_VERSION))"
	@echo "  - Binary: $(INSTALL_DIR)/$(BINARY)"
	@echo "  - Schema: $(INSTALL_DIR)/schema/"
	@echo "  - Manifest: $(INSTALL_DIR)/formae-plugin.pkl"

## install-versioned: Install plugin with all-versions schema layout under
## a single PklProject. Replaces the per-install pinning of `install`.
## Generator emits the install layout directly; this target is a thin
## `cp -R` of schema/pkl/generated/ into the install dir. Available
## schema versions are inferred at runtime by formae from the v*/
## subdirectory layout — no manifest file needed.
install-versioned: build
	@echo "Installing $(PLUGIN_NAME) v$(PLUGIN_VERSION) (versioned schema layout)..."
	@if [ ! -d schema/pkl/generated ]; then \
		echo "Generating versioned schemas..."; \
		$(MAKE) generate-versioned-pkl-schemas; \
	fi
	@rm -rf $(PLUGIN_BASE_DIR)/$(PLUGIN_NAME)
	@mkdir -p $(INSTALL_DIR)/schema/pkl
	@cp bin/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@cp -R schema/pkl/generated/. $(INSTALL_DIR)/schema/pkl/
	@cp formae-plugin.pkl $(INSTALL_DIR)/
	@if [ -f schema/Config.pkl ]; then mkdir -p $(INSTALL_DIR)/schema && cp schema/Config.pkl $(INSTALL_DIR)/schema/; fi
	@pkl project resolve $(INSTALL_DIR)/schema/pkl >/dev/null
	@echo "Installed to $(INSTALL_DIR)"
	@echo "Versions: $$(ls -d $(INSTALL_DIR)/schema/pkl/v*/ 2>/dev/null | xargs -n1 basename | tr '\n' ' ')"

## help: Show this help message
help:
	@echo "Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## setup-credentials: Provision cloud provider credentials
## Edit scripts/ci/setup-credentials.sh to configure for your provider.
setup-credentials:
	@./scripts/ci/setup-credentials.sh

## clean-environment: Clean up test resources in cloud environment
## Called before and after conformance tests. Edit scripts/ci/clean-environment.sh
## to configure for your provider.
clean-environment:
	@./scripts/ci/clean-environment.sh

## conformance-test: Run all conformance tests (CRUD + discovery)
## Usage: make conformance-test [VERSION=0.80.0] [TEST=namespace] [PARALLEL=10] [TIMEOUT=15] [FORMAE_BINARY=/path/to/formae]
## Auto-detects local formae binary from ../formae/bin/formae or $PATH.
## Calls setup-credentials and clean-environment automatically.
##
## Parameters:
##   VERSION      - Formae version to test against (default: latest, skipped if FORMAE_BINARY set)
##   TEST         - Filter tests by name pattern (e.g., TEST=namespace)
##   PARALLEL     - Max parallel tests (default: 1 = sequential)
##   TIMEOUT      - Timeout in minutes for long-running operations (default: 5)
##   FORMAE_BINARY - Path to formae binary (auto-detected from ../formae/bin/ or $PATH)
conformance-test: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-crud-run conformance-test-discovery-run VERSION=$(VERSION) TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-crud: Run CRUD tests with cleanup (convenience for local dev)
conformance-test-crud: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-crud-run VERSION=$(VERSION) TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-discovery: Run discovery tests with cleanup (convenience for local dev)
conformance-test-discovery: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-discovery-run VERSION=$(VERSION) TEST=$(TEST) PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-crud-run: Run only CRUD lifecycle tests (no cleanup)
## Used by CI matrix jobs where cleanup is managed separately.
conformance-test-crud-run:
	@echo "Running CRUD conformance tests..."
	@FORMAE_BINARY="$(FORMAE_BINARY)" FORMAE_TEST_FILTER="$(TEST)" FORMAE_TEST_TYPE=crud FORMAE_TEST_PARALLEL="$(PARALLEL)" FORMAE_TEST_TIMEOUT="$(TIMEOUT)" ./scripts/run-conformance-tests.sh $(VERSION)

## conformance-test-discovery-run: Run only discovery tests (no cleanup)
## Used by CI matrix jobs where cleanup is managed separately.
conformance-test-discovery-run:
	@echo "Running discovery conformance tests..."
	@FORMAE_BINARY="$(FORMAE_BINARY)" FORMAE_TEST_FILTER="$(TEST)" FORMAE_TEST_TYPE=discovery FORMAE_TEST_PARALLEL="$(PARALLEL)" FORMAE_TEST_TIMEOUT="$(TIMEOUT)" ./scripts/run-conformance-tests.sh $(VERSION)

## conformance-test-resources: Run conformance tests for non-chart resource types only
## Excludes *-chart tests via RE2 regex (no negative lookahead in Go regexp).
## Usage: make conformance-test-resources [TIMEOUT=10] [PARALLEL=1]
conformance-test-resources: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@$(MAKE) conformance-test-crud-run conformance-test-discovery-run TEST='/^([^c]|c[^h]|ch[^a]|cha[^r]|char[^t])*$$$$/' PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## conformance-test-charts: Run conformance tests for chart-based test cases only
## Uses regex filter to select only chart tests. Higher discovery timeout (10min)
## because charts produce many resources that take longer to discover.
## Usage: make conformance-test-charts [DISCOVERY_TIMEOUT=10] [TIMEOUT=10] [PARALLEL=1]
DISCOVERY_TIMEOUT ?= 10
conformance-test-charts: install setup-credentials
	@echo "Pre-test cleanup..."
	@./scripts/ci/clean-environment.sh || true
	@echo ""
	@FORMAE_TEST_DISCOVERY_TIMEOUT="$(DISCOVERY_TIMEOUT)" $(MAKE) conformance-test-crud-run conformance-test-discovery-run TEST='/.*-chart/' PARALLEL=$(PARALLEL) TIMEOUT=$(TIMEOUT); \
	TEST_EXIT=$$?; \
	echo ""; \
	echo "Post-test cleanup..."; \
	./scripts/ci/clean-environment.sh || true; \
	exit $$TEST_EXIT

## chart-test: Run chart smoke tests (deploy + verify + cleanup)
## Usage: make chart-test [CHART=nginx] [TIMEOUT=10]
## Deploys each chart via formae apply, waits for pods, then destroys.
##
## Parameters:
##   CHART   - Filter charts by name (e.g., CHART=nginx)
##   TIMEOUT - Pod readiness timeout in seconds (default: 120)
chart-test: install
	@echo "Running chart smoke tests..."
	@FORMAE_BINARY="$(FORMAE_BINARY)" CHART_TEST_TIMEOUT="$(or $(TIMEOUT),120)" ./scripts/run-chart-tests.sh $(CHART)

## drift-test: Run drift detection + reconciliation test
## Deploys drift-demo.pkl, introduces drift via kubectl, force reconciles,
## and verifies state was restored.
drift-test: install
	@echo "Running drift detection test..."
	@FORMAE_BINARY="$(FORMAE_BINARY)" ./scripts/run-drift-test.sh
