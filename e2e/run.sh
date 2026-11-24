#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
make web
mkdir -p e2e/.bin
"${GO:-go}" build -o e2e/.bin/rua ./cmd/rua
npm --prefix e2e ci
npm --prefix e2e test -- "$@"
