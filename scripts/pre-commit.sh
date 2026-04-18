#!/usr/bin/env bash
set -euo pipefail

# Let `go` auto-download a newer toolchain when go.mod requires one the
# host hasn't installed yet (the devcontainer pins GOTOOLCHAIN=local).
export GOTOOLCHAIN=auto

staged_go=$(git diff --cached --name-only --diff-filter=ACM | grep '\.go$' || true)
if [ -z "$staged_go" ]; then
    exit 0
fi

go vet ./...

if command -v golangci-lint >/dev/null 2>&1; then
    golangci-lint run
else
    echo "pre-commit: golangci-lint not found on PATH; run 'make tools'" >&2
    exit 1
fi
