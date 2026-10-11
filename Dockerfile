# The avSYS panel: controller, API, web UI, SSH gateway and router in one binary.

FROM oven/bun:1 AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -tags embedweb -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /avsys ./cmd/avsys

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /avsys /avsys
# The database lives here; mount a volume.
VOLUME /data
ENV AVSYS_DATA_DIR=/data
EXPOSE 8080 2222 25565
ENTRYPOINT ["/avsys"]
