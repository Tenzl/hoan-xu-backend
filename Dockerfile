FROM golang:1.27-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOMAXPROCS=2 go build -p 2 -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 GOMAXPROCS=2 go build -p 2 -trimpath -ldflags="-s -w" -o /out/admin ./cmd/admin

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates openssh-client tini gosu curl \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 hoanxu \
 && useradd --uid 10001 --gid hoanxu --create-home hoanxu \
 && mkdir -p /app /var/data/files \
 && chown -R hoanxu:hoanxu /app /var/data
WORKDIR /app
COPY --from=build /out/api /out/admin /app/
COPY database/migrations /app/database/migrations
COPY deploy/docker /app/deploy
RUN sed -i 's/\r$//' /app/deploy/*.sh \
 && chmod 755 /app/deploy/*.sh
ENV HOST=0.0.0.0 PORT=10000 PRIVATE_DIR=/var/data/files \
    REMOTE_BROWSER_UPSTREAM=http://127.0.0.1:6080 \
    REMOTE_BROWSER_ENABLED=true
# Only Go is public. CDP and the display arrive through a loopback SSH tunnel.
EXPOSE 10000
HEALTHCHECK --interval=30s --timeout=5s --start-period=45s --retries=3 \
 CMD curl --fail --silent "http://127.0.0.1:${PORT}/readyz" > /dev/null || exit 1
# Bootstrap disk permissions, then immediately drop privileges for all services.
ENTRYPOINT ["/app/deploy/entrypoint.sh"]
