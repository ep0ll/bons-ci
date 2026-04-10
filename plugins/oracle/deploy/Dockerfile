# ─────────────────────────────────────────────────────────────────────────────
# Stage 1: Build
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

ARG VERSION=dev
ARG BUILD_TIME=unknown

WORKDIR /build

# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build \
      -trimpath \
      -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -s -w" \
      -o /bin/oci-migrator \
      ./cmd/migrator

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: Runtime
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# CRIU ≥ 3.15, iSCSI, filesystem tools, iptables for network restore.
RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends \
      criu \
      open-iscsi \
      e2fsprogs \
      iptables \
      iproute2 \
      procps \
      numactl \
      linux-tools-common \
      ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Grant CRIU required capabilities.
RUN setcap cap_sys_ptrace,cap_sys_admin,cap_net_admin,cap_dac_read_search+ep /usr/sbin/criu

# Enable I/O scheduler tuning for the iSCSI device (noop gives best latency
# with OCI's NVMe-backed block storage).
RUN echo 'ACTION=="add|change", SUBSYSTEM=="block", ENV{ID_BUS}=="scsi", \
    ATTR{queue/scheduler}="none"' > /etc/udev/rules.d/60-iscsi-scheduler.rules

COPY --from=builder /bin/oci-migrator /usr/local/bin/oci-migrator

ENTRYPOINT ["/usr/local/bin/oci-migrator"]
