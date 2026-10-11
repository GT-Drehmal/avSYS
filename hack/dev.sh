#!/bin/sh
# Builds the panel and runs it on this machine against the current kubectl
# context, reaching pods through the API server. State lives in .dev/.
set -eu
cd "$(dirname "$0")/.."
mkdir -p .dev
docker build -q -t avsys-workspace:dev images/workspace >/dev/null
go build -o .dev/avsys ./cmd/avsys
exec ./.dev/avsys \
  --data-dir .dev/data \
  --workspace-image avsys-workspace:dev \
  --workspace-image-pull-policy Never \
  --pod-dial forward \
  --web-dir web/dist \
  "$@"
