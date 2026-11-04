# Elena build and test entry points.
#
# Local and CI run the same commands. Override the Go binary when the
# PATH-visible toolchain is older than the one required by go.mod, e.g.:
#   make GO=/usr/local/go/bin/go test

GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: lint test race cov layering

# lint runs the standard golangci-lint linter set.
lint:
	$(GOLANGCI_LINT) run

# layering asserts the import direction of the layered architecture
# with go list:
#   - internal/semantic imports no repo-internal package except the engine,
#   - the root elena facade imports no internal package except semantic,
#   - cmd/ and examples/ import no internal package at all (they use the
#     public elena library).
# Absent directories (cmd, examples do not exist yet) are skipped until
# they appear; once they do, they are guarded like everything else.
layering:
	@fail=0; \
	semantic_bad="$$($(GO) list -f '{{join .Imports "\n"}}' ./internal/semantic | grep '^elena/internal/' | grep -vx elena/internal/engine || true)"; \
	if [ -n "$$semantic_bad" ]; then \
		echo "layering violation: internal/semantic imports non-engine internal packages:"; \
		echo "$$semantic_bad"; \
		fail=1; \
	fi; \
	root_bad="$$($(GO) list -f '{{join .Imports "\n"}}' . | grep '^elena/internal/' | grep -vx elena/internal/semantic || true)"; \
	if [ -n "$$root_bad" ]; then \
		echo "layering violation: root elena imports internal packages other than semantic:"; \
		echo "$$root_bad"; \
		fail=1; \
	fi; \
	for area in cmd examples; do \
		if [ -d $$area ]; then \
			area_bad="$$($(GO) list -f '{{join .Imports "\n"}}' ./$$area/... | grep '^elena/internal/' || true)"; \
			if [ -n "$$area_bad" ]; then \
				echo "layering violation: $$area imports internal packages:"; \
				echo "$$area_bad"; \
				fail=1; \
			fi; \
		fi; \
	done; \
	if [ $$fail -ne 0 ]; then exit 1; fi
	@echo "layering guard passed"

# test runs the full test suite.
test:
	$(GO) test ./...

# race runs the full test suite with the race detector enabled.
race:
	$(GO) test -race ./...

# cov runs the race suite with coverage and prints the total.
# -coverpkg attributes hits across packages: the root-package facade
# tests are the seam for internal/semantic, so their execution of the
# implementation must land in the profile for the gate to be honest.
cov:
	$(GO) test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1
