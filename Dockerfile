# The build stage always runs on the machine doing the build and cross-compiles,
# so an arm64 image is not compiled under emulation (which is many times slower).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/nextupdate ./cmd/nextupdate

FROM alpine:3.22
ARG VERSION=dev
RUN apk add --no-cache docker-cli docker-cli-compose ca-certificates tzdata
COPY --from=build /out/nextupdate /usr/local/bin/nextupdate
LABEL org.opencontainers.image.title="nextupdate" \
      org.opencontainers.image.description="Update cockpit for Docker containers" \
      org.opencontainers.image.source="https://github.com/jordibrouwer/nextupdate" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
ENV NEXTUPDATE_DATA=/data
VOLUME /data
EXPOSE 8099
# The port follows NEXTUPDATE_LISTEN (default :8099).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD sh -c 'p="${NEXTUPDATE_LISTEN:-:8099}"; wget -q -O /dev/null "http://127.0.0.1:${p##*:}/api/status"'
ENTRYPOINT ["nextupdate"]
CMD ["serve"]
