#!/bin/sh
# Builds restic for both images, from its release's source, with the Go of
# the golang image this runs in. restic's own builds use an older Go and
# older modules, with known advisories in both.
#
#   build.sh <output>    for $TARGETOS/$TARGETARCH, cross-compiled
#
# Moving to a newer restic: change VERSION and SHA256, and drop each module
# below that the new release already has at that version or newer.
set -eu

VERSION=0.19.1
# From the release's SHA256SUMS, which is signed with restic's release key,
# CF8F 18F2 8445 7597 3F79 D4E1 91A6 868B D3F7 A907.
SHA256=bb9b1a19040744d26d8a79be029d4e6b189c45ccc9d8831d7fe367d3c33df725

# Modules newer than the release has, for advisories against the versions
# it has: the first version with the fix, or for x/crypto and x/text what
# that x/net needs. Only ever raised: one the release already has at that
# version or newer is left as it is.
MODULES="
golang.org/x/net@v0.62.0
golang.org/x/crypto@v0.58.0
golang.org/x/text@v0.43.0
google.golang.org/grpc@v1.83.2
github.com/klauspost/compress@v1.18.7
"

out=$1
work=$(mktemp -d)
cd "$work"
curl -fsSL --retry 3 -o restic.tar.gz "https://github.com/restic/restic/releases/download/v$VERSION/restic-$VERSION.tar.gz"
echo "$SHA256  restic.tar.gz" | sha256sum -c -
tar -xzf restic.tar.gz --strip-components=1
rm restic.tar.gz

raise=
for module in $MODULES; do
  have=$(go list -m -f '{{.Version}}' "${module%@*}")
  want=${module#*@}
  [ "$(printf '%s\n%s\n' "$have" "$want" | sort -V | tail -n1)" = "$have" ] || raise="$raise $module"
done
# shellcheck disable=SC2086 # one argument each
[ -z "$raise" ] || go get $raise

# As restic's releases are built (helpers/build-release-binaries), but
# without its self-update command, which has no place in an image. The
# version is in the source, so `restic version` says 0.19.1 and the Go it
# was built with.
CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
  go build -trimpath -tags disable_grpc_modules -ldflags "-s -w" -o "$out" ./cmd/restic
go version -m "$out"

cd /
rm -rf "$work"
