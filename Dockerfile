# Dockerfile for TokenFuse
# Multi-stage build for a small, secure production image

FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with optimizations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev') -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown') -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o tokenfuse \
    ./cmd/tokenfuse

# Final minimal image
FROM alpine:3.20

# Add ca-certificates for HTTPS calls to provider APIs
RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -g 1000 -S tokenfuse && \
    adduser -u 1000 -S tokenfuse -G tokenfuse

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/tokenfuse /usr/local/bin/tokenfuse

# Set ownership
RUN chown -R tokenfuse:tokenfuse /app

# Use non-root user
USER tokenfuse

# Expose metrics port (if used)
EXPOSE 9090

# Default entrypoint
ENTRYPOINT ["tokenfuse"]
CMD ["--help"]