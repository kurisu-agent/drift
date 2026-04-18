# drift — development entrypoints. All targets assume the devcontainer
# (Go 1.25 + docker-in-docker) or an equivalent host.

GO ?= go
GOLANGCI_LINT_VERSION ?= v2.6.0
GOLANGCI_LINT ?= $(shell $(GO) env GOPATH)/bin/golangci-lint
GOVULNCHECK ?= $(shell $(GO) env GOPATH)/bin/govulncheck

.PHONY: all
all: test

.PHONY: build
build:
	$(GO) build ./...

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

# Integration tests. Build-tag-gated so `make test` stays fast. Requires
# docker (devcontainer provides it); builds a throwaway circuit image and
# exercises drift over a real SSH transport. See integration/harness.go.
.PHONY: integration
integration:
	$(GO) test -tags=integration -count=1 ./integration/...

.PHONY: fuzz-wire
fuzz-wire:
	$(GO) test -run=^$$ -fuzz=FuzzDecodeRequest -fuzztime=30s ./internal/wire

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: tools
tools: $(GOLANGCI_LINT) $(GOVULNCHECK)

$(GOLANGCI_LINT):
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK):
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run

.PHONY: vuln
vuln: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

.PHONY: ci
ci: tidy vet test-race lint vuln

.PHONY: install-hooks
install-hooks:
	@chmod +x scripts/pre-commit.sh
	@ln -sf ../../scripts/pre-commit.sh .git/hooks/pre-commit
	@echo "pre-commit hook installed -> scripts/pre-commit.sh"
