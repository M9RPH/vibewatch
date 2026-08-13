# syntax=docker/dockerfile:1.7
FROM node:22-alpine AS web-build
WORKDIR /src/web
COPY web/package.json ./
RUN npm install
COPY web/ ./
RUN npm run build

FROM golang:1.23-alpine AS go-build
WORKDIR /src
COPY go.mod ./
COPY VERSION ./VERSION
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN VIBEWATCH_VERSION="$(cat VERSION)" && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=$VIBEWATCH_VERSION" -o /out/vibewatch ./cmd/server

FROM alpine:3.20
ARG VIBEWATCH_VERSION=0.9.2.7
LABEL org.opencontainers.image.title="Vibewatch" \
      org.opencontainers.image.version="$VIBEWATCH_VERSION" \
      org.opencontainers.image.description="Multi-host Docker update control powered by Watchtower"
RUN apk add --no-cache ca-certificates tzdata sqlite docker-cli openssh-client sshpass && addgroup -S vibewatch && adduser -S -G vibewatch vibewatch
WORKDIR /app
COPY --from=go-build /out/vibewatch /usr/local/bin/vibewatch
COPY --from=web-build /src/web/dist /app/web
RUN mkdir -p /data/logs && chown -R vibewatch:vibewatch /data /app
# The Docker socket normally requires root/docker-group access. The container
# therefore starts as root by default; Vibewatch itself does not expose a shell.
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/vibewatch"]
