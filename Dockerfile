# Use the official Golang image to build the weightless
FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags "-X main.version=${VERSION} -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o weightless ./cmd/tracker/

# Use a lightweight alpine image for the final container
FROM alpine:latest

# Install Litestream (multi-arch: amd64 or arm64).
# TARGETARCH is auto-set by BuildKit; default to amd64 so a plain `docker build` still works.
ARG TARGETARCH=amd64
RUN apk add --no-cache ca-certificates curl && \
    curl -fsSL "https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-linux-${TARGETARCH}.tar.gz" \
      -o /tmp/litestream.tar.gz && \
    tar -C /usr/local/bin -xzf /tmp/litestream.tar.gz && \
    rm /tmp/litestream.tar.gz

# Create data directory
RUN mkdir -p /data

# Copy weightless binary and configuration
COPY --from=builder /app/weightless /usr/local/bin/weightless
COPY .env.local /usr/local/bin/.env.local
COPY litestream.yml /etc/litestream.yml
COPY scripts/run.sh /usr/local/bin/run.sh
RUN chmod +x /usr/local/bin/run.sh

# Set DB path for the application
ENV DB_PATH=/data/weightless.db
ENV PORT=8080

EXPOSE 8080

CMD ["/usr/local/bin/run.sh"]
