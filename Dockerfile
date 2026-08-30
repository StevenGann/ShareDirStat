# syntax=docker/dockerfile:1.7
# Multi-arch build: docker buildx build --platform linux/arm64,linux/amd64 .
# Stages run on the build host ($BUILDPLATFORM) and cross-compile for $TARGETARCH.

ARG GO_VERSION=1.27
ARG NODE_VERSION=22

# ---- web UI ---------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Go binary ------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/StevenGann/ShareDirStat/internal/version.Version=${VERSION} \
        -X github.com/StevenGann/ShareDirStat/internal/version.Commit=${COMMIT} \
        -X github.com/StevenGann/ShareDirStat/internal/version.Date=${DATE}" \
      -o /out/sharedirstat ./cmd/sharedirstat

# ---- runtime --------------------------------------------------------------
# distroless/static ships CA certs and tzdata, no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="ShareDirStat" \
      org.opencontainers.image.description="Disk usage analyser for mounted shares — WinDirStat for your homelab" \
      org.opencontainers.image.source="https://github.com/StevenGann/ShareDirStat" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/sharedirstat /sharedirstat
# Default identity 1000:1000 (FR-DEP-03). Override with `user:` / securityContext
# to match the UID that owns the share data.
USER 1000:1000
ENV SDS_SERVER__LISTEN=":8080" \
    SDS_DATA_DIR="/data" \
    TZ="UTC"
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/sharedirstat", "healthcheck"]
ENTRYPOINT ["/sharedirstat"]
CMD ["serve"]
