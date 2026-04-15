# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Download dependencies first (layer-cached unless go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -extldflags=-static" \
    -o /byoc-server ./cmd/server

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
# distroless/static has no shell, no package manager — minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /byoc-server /byoc-server

EXPOSE 8080

# Run as non-root (distroless nonroot image defaults to uid 65532).
USER nonroot:nonroot

ENTRYPOINT ["/byoc-server"]
