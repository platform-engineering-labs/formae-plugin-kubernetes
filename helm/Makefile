# © 2026 Platform Engineering Labs Inc.
#
# SPDX-License-Identifier: FSL-1.1-ALv2
#
# formae-helm — per-K8s-version PKL wrappers around pkl-readers/helm.
#
# `main/` is the source of truth (hand-edited). `generated/v<X.Y>/` is
# emitted by `make generate` for every supported K8s minor: each tree
# is a string-rewrite of `main/` with `@k8s/<group>/<Kind>.pkl` →
# `@k8s/v<X.Y>/<group>/<Kind>.pkl`, with mappers omitted when the
# matching K8s schema doesn't ship them. Users import the per-version
# subtree:
#
#     import "@formae-helm/v1.27/HelmChart.pkl"

.PHONY: all generate package clean test help

# Default target: regenerate per-version trees.
all: generate

## generate: Run gen-versioned-helm to emit generated/v<X.Y>/ for every
## supported K8s minor. Reads the K8s schema package (resolved via
## `pkl project resolve`) to learn which versions exist and which
## resource modules each ships.
generate:
	@echo "Resolving Pkl dependencies..."
	@pkl project resolve . >/dev/null
	@echo "Generating per-version helm wrappers..."
	@cd tools/gen-versioned-helm && go run .
	@echo "Generated: $$(ls -d v[0-9]*.[0-9]*/ 2>/dev/null | xargs -n1 basename | tr '\n' ' ')"

## package: Build the formae-helm Pkl package zip for hub publication.
## Runs generate first; the zip ships generated/ alongside main/ so
## consumers don't need to run codegen themselves.
package: generate
	@echo "Packaging formae-helm Pkl package..."
	@pkl project package .

## test: Smoke-analyze imports for every generated module. HelmChart and
## friends declare required-without-default fields (chart, releaseName,
## ...), so `pkl eval` against the bare module fails on user-input
## fields. `pkl analyze imports` parses + resolves but doesn't render,
## so it catches every broken import without needing fixture inputs.
## Full conformance lives in formae-plugin-k8s.
test: generate
	@for v in $$(ls -d v[0-9]*.[0-9]*/ 2>/dev/null | xargs -n1 basename); do \
	  for mod in HelmChart Generator StaticGenerator; do \
	    f="$$v/$$mod.pkl"; \
	    [ -f "$$f" ] || continue; \
	    pkl analyze imports --project-dir . "$$f" >/dev/null \
	      || (echo "FAIL: $$f" && exit 1); \
	  done; \
	  echo "OK: $$v"; \
	done
	@echo "All generated trees resolve cleanly."

## verify: Fail if helm/v*/ doesn't match what the codegen would emit
## from helm/main/. Wired into CI so PRs that touch main/ or
## tools/gen-versioned-helm/ without re-running codegen surface the
## stale trees in the diff.
verify: generate
	@if ! git diff --exit-code -- 'v[0-9]*.[0-9]*' >/dev/null 2>&1; then \
	  echo ""; \
	  echo "ERROR: helm/v*/ is stale relative to helm/main/."; \
	  echo "Run 'cd helm && make generate' and commit the diff."; \
	  echo ""; \
	  git diff --stat -- 'v[0-9]*.[0-9]*'; \
	  exit 1; \
	fi
	@echo "helm/v*/ matches codegen output."

## clean: Remove generated v*/ trees and the Pkl deps lockfile.
clean:
	@for d in v[0-9]*.[0-9]*/; do [ -d "$$d" ] && rm -rf "$$d" || true; done
	rm -f PklProject.deps.json

## help: Show available targets.
help:
	@grep -E '^## ' Makefile | sed 's/## /  /'
