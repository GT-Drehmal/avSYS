#!/bin/bash
# What the panel lists for content against what the server image downloads
# itself (MODRINTH_DOWNLOAD_DEPENDENCIES=required), on the real Modrinth and
# the real itzg/minecraft-server, in plain docker: see
# compare-dependencies.py. Needs docker, python3 and network; Go, or a test
# binary of internal/api built for this machine in API_TEST.
#   hack/compare-dependencies.sh [set...]     sets from compare-dependencies.json
# Prints a Markdown report; exits 1 where the panel's is not as good.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=${AVSYS_COMPARE_WORK:-$(mktemp -d)}
if [ -z "${API_TEST:-}" ]; then
  API_TEST=$work/api.test
  (cd "$here/.." && go test -c -o "$API_TEST" ./internal/api)
fi
exec python3 -I "$here/compare-dependencies.py" --plans "$here/compare-dependencies.json" --api-test "$API_TEST" --work "$work" "$@"
