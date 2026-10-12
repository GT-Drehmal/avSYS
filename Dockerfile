# The avSYS panel: controller, API, web UI, SSH gateway and router in one binary.
#
# Both build stages run on the machine doing the build and cross-compile for
# the target, so a multi-architecture build does not emulate the compilers.

FROM --platform=$BUILDPLATFORM oven/bun:1 AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags embedweb -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /avsys ./cmd/avsys

# The panel keeps each organization's snapshot repository and prunes it.
FROM restic/restic:0.19.1 AS restic

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/GT-Drehmal/avSYS" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="avSYS: a Kubernetes-native Minecraft server panel"
COPY --from=build /avsys /avsys
COPY --from=restic /usr/bin/restic /usr/local/bin/restic
COPY LICENSE /licenses/LICENSE
# The database lives here; mount a volume.
VOLUME /data
ENV AVSYS_DATA_DIR=/data
EXPOSE 8080 2222 25565
ENTRYPOINT ["/avsys"]
