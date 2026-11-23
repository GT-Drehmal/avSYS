# The avSYS panel: controller, API, web UI, SSH gateway and router in one binary.
#
# The build stages run on the machine doing the build and cross-compile for
# the target, so a multi-architecture build does not emulate the compilers.
#
# Images are named by digest as well as tag, so that a commit builds from
# the same images each time. Dependabot moves the digests.

FROM --platform=$BUILDPLATFORM oven/bun:1@sha256:3aa040e5445efa09d75bb428a64ba50db13e2b3ac9b6b0c7f145615f62b2ee18 AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM --platform=$BUILDPLATFORM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags embedweb -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /avsys ./cmd/avsys

# The panel keeps each organization's snapshot repository and prunes it.
# restic is built from source with the Go above, see images/restic/build.sh.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS restic
COPY images/restic/build.sh /build-restic.sh
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    sh /build-restic.sh /restic

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source="https://github.com/GT-Drehmal/avSYS" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="avSYS: a Kubernetes-native Minecraft server panel"
COPY --from=build /avsys /avsys
COPY --from=restic /restic /usr/local/bin/restic
COPY LICENSE /licenses/LICENSE
# The database lives here; mount a volume.
VOLUME /data
ENV AVSYS_DATA_DIR=/data
EXPOSE 8080 2222 25565
ENTRYPOINT ["/avsys"]
