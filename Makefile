# Elena build and test entry points.
#
# Local and CI run the same commands. Override the Go binary when the
# PATH-visible toolchain is older than the one required by go.mod, e.g.:
#   make GO=/usr/local/go/bin/go test

GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: lint test race cov

# lint runs the standard golangci-lint linter set.
lint:
	$(GOLANGCI_LINT) run

# test runs the full test suite.
test:
	$(GO) test ./...

# race runs the full test suite with the race detector enabled.
race:
	$(GO) test -race ./...

# cov runs the race suite with coverage and prints the total.
cov:
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1
