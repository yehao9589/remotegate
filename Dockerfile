FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

FROM build AS packages
ARG APP_COMMIT=development
ARG APP_BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p dist /out/packages && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X github.com/local/remotegate/internal/buildinfo.Commit=$APP_COMMIT -X github.com/local/remotegate/internal/buildinfo.BuiltAt=$APP_BUILD_TIME" -o dist/remotegate-agent-linux-amd64 ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X github.com/local/remotegate/internal/buildinfo.Commit=$APP_COMMIT -X github.com/local/remotegate/internal/buildinfo.BuiltAt=$APP_BUILD_TIME" -o dist/remotegate-agent-linux-arm64 ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="-s -w -X github.com/local/remotegate/internal/buildinfo.Commit=$APP_COMMIT -X github.com/local/remotegate/internal/buildinfo.BuiltAt=$APP_BUILD_TIME" -o dist/remotegate-agent-linux-armv7 ./cmd/agent && \
    go run -ldflags="-X github.com/local/remotegate/internal/buildinfo.Commit=$APP_COMMIT -X github.com/local/remotegate/internal/buildinfo.BuiltAt=$APP_BUILD_TIME" ./scripts/package-istore.go && \
    cp dist/RemoteGate-*-istore.run* /out/packages/

FROM build AS server
ARG TARGETOS
ARG TARGETARCH
ARG APP_COMMIT=development
ARG APP_BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/local/remotegate/internal/buildinfo.Commit=$APP_COMMIT -X github.com/local/remotegate/internal/buildinfo.BuiltAt=$APP_BUILD_TIME" -o /out/remotegate-server ./cmd/server

FROM alpine:3.24
RUN apk add --no-cache ca-certificates curl su-exec libcap && adduser -D -H -u 10001 remotegate
COPY --from=server /out/remotegate-server /usr/local/bin/remotegate-server
COPY --from=packages /out/packages/ /opt/remotegate/packages/
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh && \
    setcap cap_net_bind_service=+ep /usr/local/bin/remotegate-server
VOLUME ["/data"]
EXPOSE 443
ENV LISTEN_ADDR=127.0.0.1:18088 HTTPS_LISTEN_ADDR=:443 STATE_PATH=/data/state.json \
    HEALTHCHECK_URL=http://127.0.0.1:18088/healthz
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl --fail --silent --max-time 3 "$HEALTHCHECK_URL" >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/remotegate-server"]
