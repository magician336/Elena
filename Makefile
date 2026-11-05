# Elena build and test entry points.
#
# Local and CI run the same commands. Override the Go binary when the
# PATH-visible toolchain is older than the one required by go.mod, e.g.:
#   make GO=/usr/local/go/bin/go test

GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: lint test race cov layering terms bench-test bench-cov bench-smoke

# lint runs the standard golangci-lint linter set over the main module,
# then the bench module — the same two surfaces CI lints, with the same
# default configuration and command in both places.
lint:
	$(GOLANGCI_LINT) run
	cd bench && $(GOLANGCI_LINT) run

# terms checks wording consistency over the files Git tracks:
# project terminology follows GLOSSARY.md and no file carries
# code-generation attribution trailers. The guard's own tests run
# under `make test`.
terms:
	$(GO) run ./tools/termcheck

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
	semantic_bad="$$($(GO) list -f '{{join .Imports "\n"}}' ./internal/semantic | grep '^github.com/magician336/elena/internal/' | grep -vx github.com/magician336/elena/internal/engine || true)"; \
	if [ -n "$$semantic_bad" ]; then \
		echo "layering violation: internal/semantic imports non-engine internal packages:"; \
		echo "$$semantic_bad"; \
		fail=1; \
	fi; \
	root_bad="$$($(GO) list -f '{{join .Imports "\n"}}' . | grep '^github.com/magician336/elena/internal/' | grep -vx github.com/magician336/elena/internal/semantic || true)"; \
	if [ -n "$$root_bad" ]; then \
		echo "layering violation: root elena imports internal packages other than semantic:"; \
		echo "$$root_bad"; \
		fail=1; \
	fi; \
	for area in cmd examples; do \
		if [ -d $$area ]; then \
			area_bad="$$($(GO) list -f '{{join .Imports "\n"}}' ./$$area/... | grep '^github.com/magician336/elena/internal/' || true)"; \
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

# The bench module is its own Go module (bench/go.mod); its gates run
# the same commands locally as the CI bench job does there.

# bench-test runs the bench module's suite with the race detector.
bench-test:
	cd bench && $(GO) test -race ./...

# bench-cov runs the bench module's suite with coverage and prints the
# total (CI enforces the same >= 80% gate on it).
bench-cov:
	cd bench && $(GO) test -race -covermode=atomic -coverprofile=coverage.out ./...
	cd bench && $(GO) tool cover -func=coverage.out | tail -n 1

# bench-smoke drives each engine through a seconds-scale micro workload
# end to end via the harness CLI, then every engine through one
# smoke-scale matrix cell each.
bench-smoke:
	cd bench && $(GO) run ./cmd/bench --driver elena --ops 50 --value-size 256 --dir "$$(mktemp -d)/elena"
	cd bench && $(GO) run ./cmd/bench --driver bbolt --ops 50 --value-size 256 --dir "$$(mktemp -d)/bbolt"
	cd bench && $(GO) run ./cmd/bench --driver badger --ops 50 --value-size 256 --dir "$$(mktemp -d)/badger"
	cd bench && $(GO) run ./cmd/bench --driver rosedb --ops 50 --value-size 256 --dir "$$(mktemp -d)/rosedb"
	cd bench && $(GO) run ./cmd/matrix --engines elena,bbolt,badger,rosedb --mixes a --values 64 --keys 8 --run-ops 8 --sync-period 5ms --root "$$(mktemp -d)/matrix"
