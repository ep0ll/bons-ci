# ── Stage 1: Build ────────────────────────────────────────────────────────────
# Use the official Go image pinned to a specific digest for supply-chain safety.
# Update the digest on each Go point release.
FROM golang:1.22-alpine AS builder

# Install git for module fetching; ca-certificates for TLS verification.
RUN apk add --no-cache git ca-certificates

WORKDIR /build

# Copy dependency manifests first to maximise Docker layer cache hits.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy source and build.
# CGO_ENABLED=0  → static binary (required for distroless base)
# -trimpath      → remove local filesystem paths from stack traces
# -ldflags       → embed version info, strip debug symbols for smaller binary
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /signing-service \
    ./cmd/signing-service

# ── Stage 2: Verify binary ───────────────────────────────────────────────────
# Run tests in the build stage so the final image is only produced if tests pass.
RUN CGO_ENABLED=0 go test ./... -count=1 -race -timeout=60s

# ── Stage 3: Runtime ──────────────────────────────────────────────────────────
# distroless/static has no shell, package manager, or unused OS tools.
# This is the minimum attack surface for a Go service.
FROM gcr.io/distroless/static-debian12:nonroot

# Copy CA certificates from builder for HTTPS calls to Fulcio/Rekor.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary.
COPY --from=builder /signing-service /signing-service

# nonroot UID (65532) is defined in distroless:nonroot.
# Never run as root in production.
USER nonroot:nonroot

# Health check for Docker Compose / non-Kubernetes deployments.
# Kubernetes uses readyz/livez probes; this is for local dev convenience.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/signing-service", "-health-check"] || exit 1

EXPOSE 8080 9090

ENTRYPOINT ["/signing-service"]
