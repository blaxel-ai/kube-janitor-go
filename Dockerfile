# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -X main.version=$(git describe --tags --always --dirty) \
    -X main.commit=$(git rev-parse HEAD) \
    -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o kube-janitor-go cmd/kube-janitor-go/main.go

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 janitor

# Copy binary from builder
COPY --from=builder /app/kube-janitor-go /usr/local/bin/kube-janitor-go

# Use non-root user
USER janitor

# Expose metrics port
EXPOSE 8080

# Set entrypoint
ENTRYPOINT ["kube-janitor-go"] 