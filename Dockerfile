# Build stage
FROM golang:1.23-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /qemu-bmc ./cmd/qemu-bmc

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    qemu-system-x86 \
    qemu-utils \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /qemu-bmc /usr/local/bin/qemu-bmc

EXPOSE 443/tcp
EXPOSE 623/udp

ENTRYPOINT ["/usr/local/bin/qemu-bmc"]
