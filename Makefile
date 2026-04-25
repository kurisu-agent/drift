# drift — development entrypoints. The toolchain comes from the nix
# flake (`nix develop`): golangci-lint, govulncheck, gcc (for -race),
# gnumake itself. `make ci` auto-reenters `nix develop` when invoked
# from a bare shell so one command works from anywhere.

GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck

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

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run ./...

.PHONY: vuln
vuln:
	$(GOVULNCHECK) ./...

# ci is the full CI-parity suite (tidy + vet + test-race + lint + vuln).
# When invoked from a bare shell it re-execs inside `nix develop` so the
# flake-pinned golangci-lint / govulncheck / gcc (for -race) are on
# PATH — that's exactly what GitHub CI runs. Inside `nix develop`
# (IN_NIX_SHELL is set by the shell hook) the re-exec is skipped and
# the pipeline runs directly, so `nix develop; make ci` doesn't nest.
.PHONY: ci
ifdef IN_NIX_SHELL
ci: tidy vet test-race lint vuln
else
ci:
	nix develop --command $(MAKE) ci
endif

.PHONY: install-hooks
install-hooks:
	@git config core.hooksPath .githooks
	@echo "pre-commit hook enabled (core.hooksPath = .githooks)"
	@echo "  applies to every worktree of this clone"
.PHONY: eval-frames eval-screens eval-loop

# eval-frames renders one PNG per dashboard tab against the demo
# fixtures (legacy single-scenario loop). Use eval-screens for the
# plan-16 multi-scenario matrix.
eval-frames:
	@mkdir -p docs/eval
	@CGO_ENABLED=0 go build -o /tmp/dashboard-frame ./cmd/dashboard-frame
	@for tab in status karts circuits chest characters tunes ports logs; do \
	    /tmp/dashboard-frame -tab $$tab -w 120 -h 30 \
	        | freeze /dev/stdin --output docs/eval/$$tab.png \
	            --language ansi --background "#0a0a0a" \
	            --padding 24 --window=false --font.size 14 \
	            >/dev/null; \
	    echo "  -> docs/eval/$$tab.png"; \
	done

# eval-screens is the plan-16 multi-scenario capture target. Output is
# docs/eval/<tab>-<scenario>.png. The list below mirrors plan 16
# lines 76-87; new scenarios land here as the underlying panel features
# (filter, expand, palette, ...) wire up. After capture, read the
# rubric + PNGs and grind toward rubric-clean per panel.
eval-screens:
	@mkdir -p docs/eval
	@CGO_ENABLED=0 go build -o /tmp/dashboard-frame ./cmd/dashboard-frame
	@set -e; \
	render() { \
	    tab=$$1; scenario=$$2; w=$${3:-120}; h=$${4:-30}; \
	    out=docs/eval/$$tab-$$scenario.png; \
	    /tmp/dashboard-frame -tab $$tab -scenario $$scenario -w $$w -h $$h \
	        | freeze /dev/stdin --output $$out \
	            --language ansi --background "#0a0a0a" \
	            --padding 24 --window=false --font.size 14 \
	            >/dev/null; \
	    echo "  -> $$out"; \
	}; \
	render status default; \
	render karts default; \
	render karts filter-active; \
	render circuits default; \
	render chest default; \
	render characters default; \
	render tunes default; \
	render ports default; \
	render logs default; \
	render cross-cut narrow-80c 80 30

# eval-loop is convenience: capture + print the rubric path so an
# agent can `Read docs/eval/rubric.md` plus `Read docs/eval/*.png` in
# one go. Pure ergonomics around the plan-16 loop.
eval-loop: eval-screens
	@echo
	@echo "rubric: docs/eval/rubric.md"
	@echo "frames: docs/eval/*.png"
