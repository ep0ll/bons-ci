# ────────────────────────────────────────────────────────────────────────────
# Stage 1: build
# Uses the official Go image for reproducible builds.
# ────────────────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

# Copy dependency manifests first to leverage Docker layer cache.
COPY go.mod go.sum* ./

# Download dependencies (cached unless go.mod/go.sum change).
RUN go env -w GONOSUMDB='*' GOPROXY='direct' && \
    go mod download || true

# Copy all source code.
COPY . .

# Build both binaries with link-time variable injection and strip debug info.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/accelregistry \
      ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/accelctl \
      ./cmd/accelctl

# ────────────────────────────────────────────────────────────────────────────
# Stage 2: runtime — distroless/static for minimal attack surface
# ────────────────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# OCI image labels
LABEL org.opencontainers.image.title="AccelRegistry"
LABEL org.opencontainers.image.description="OCI-compatible registry for accelerated container images (Nydus, eStargz, SOCI, OverlayBD)"
LABEL org.opencontainers.image.source="https://github.com/bons/bons-ci/plugins/rbe/registry"
LABEL org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /app

# Copy binaries from builder stage.
COPY --from=builder /out/accelregistry /usr/local/bin/accelregistry
COPY --from=builder /out/accelctl      /usr/local/bin/accelctl

# Snapshot persistence directory (mount a volume here for production).
RUN mkdir -p /data/snapshots

# ── Runtime configuration (via environment variables) ─────────────────────
ENV ACCELREG_ADDR=:5000
ENV ACCELREG_METRICS_ADDR=:9090
ENV ACCELREG_LOG_LEVEL=info
ENV ACCELREG_EXPECTED_SOURCES=100000
ENV ACCELREG_READ_TIMEOUT=30s
ENV ACCELREG_WRITE_TIMEOUT=60s
ENV ACCELREG_IDLE_TIMEOUT=120s

# ── Ports ─────────────────────────────────────────────────────────────────
# 5000: OCI Distribution API + Accel API
# 9090: Prometheus metrics
EXPOSE 5000 9090

# ── Health check ──────────────────────────────────────────────────────────
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/accelctl", "health"]

# ── Entrypoint ────────────────────────────────────────────────────────────
USER nonroot
ENTRYPOINT ["/usr/local/bin/accelregistry"]
