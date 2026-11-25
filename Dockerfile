# syntax=docker/dockerfile:1.7

FROM node:22-bookworm-slim AS web-builder

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/index.html web/eslint.config.js ./
COPY web/favicon.ico web/favicon-16x16.png web/favicon-32x32.png web/apple-touch-icon.png web/icon-48.png web/icon-192.png web/icon-512.png ./
COPY web/scripts ./scripts
COPY web/src ./src
RUN npm run build

FROM golang:1.26.8-bookworm AS go-builder

WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
COPY --from=web-builder /src/web/dist ./internal/admin/dist

ARG BUILD_REVISION
RUN go build -tags with_utls -trimpath \
    -ldflags="-s -w -X chijie/internal/buildinfo.Revision=${BUILD_REVISION} -X chijie/internal/buildinfo.BuiltAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /out/chijie ./cmd/gateway/

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/chijie
COPY --from=go-builder /out/chijie /opt/chijie/chijie

EXPOSE 8080 9090 1080/tcp 1080/udp
VOLUME ["/config"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/opt/chijie/chijie"]
CMD ["-config", "/config"]
