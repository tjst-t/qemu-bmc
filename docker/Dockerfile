# Build stage
FROM golang:1.23-bookworm AS builder

ARG VERSION=dev
ARG NOVNC_VERSION=1.5.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Download noVNC static assets for embedding
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/* && \
    mkdir -p internal/novnc/static && \
    curl -sL https://github.com/novnc/noVNC/archive/refs/tags/v${NOVNC_VERSION}.tar.gz | \
    tar xz --strip-components=1 -C internal/novnc/static \
        noVNC-${NOVNC_VERSION}/vnc.html \
        noVNC-${NOVNC_VERSION}/vnc_lite.html \
        noVNC-${NOVNC_VERSION}/core \
        noVNC-${NOVNC_VERSION}/vendor \
        noVNC-${NOVNC_VERSION}/app
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w -X main.version=${VERSION}" -o /qemu-bmc ./cmd/qemu-bmc

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    qemu-system-x86 \
    qemu-utils \
    iproute2 \
    ovmf \
    ipmitool \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /vm /iso /var/run/qemu /var/log/qemu

COPY --from=builder /qemu-bmc /usr/local/bin/qemu-bmc
COPY docker/entrypoint.sh /scripts/
COPY docker/setup-network.sh /scripts/
RUN chmod +x /scripts/*.sh

EXPOSE 5900/tcp 623/udp 443/tcp

VOLUME ["/vm", "/iso"]

HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD ipmitool -I lanplus -H 127.0.0.1 -U ${IPMI_USER:-admin} -P ${IPMI_PASS:-password} mc info || exit 1

ENTRYPOINT ["/scripts/entrypoint.sh"]
