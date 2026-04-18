#!/usr/bin/env bash
set -euo pipefail

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
