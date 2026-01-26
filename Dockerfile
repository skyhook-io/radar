# Multi-stage Dockerfile for Skyhook Explorer

# Stage 1: Build frontend
FROM node:20-alpine AS frontend-builder

WORKDIR /app/web

# Install dependencies
COPY web/package*.json ./
RUN npm ci --prefer-offline --no-audit

# Build frontend
COPY web/ ./
RUN npm run build

# Stage 2: Build Go backend
FROM golang:1.25-alpine AS backend-builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Download Go modules first (cacheable layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY internal/ internal/

# Copy built frontend into embed location
COPY --from=frontend-builder /app/web/dist internal/static/dist/

# Build arguments
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION}" \
    -o /explorer ./cmd/explorer

# Stage 3: Final minimal image
FROM gcr.io/distroless/static-debian12:nonroot

# Labels
LABEL org.opencontainers.image.title="Skyhook Explorer"
LABEL org.opencontainers.image.description="Kubernetes cluster visualization and management tool"
LABEL org.opencontainers.image.source="https://github.com/skyhook-io/explorer"
LABEL org.opencontainers.image.vendor="Skyhook"

# Copy the binary
COPY --from=backend-builder /explorer /explorer

# Expose port
EXPOSE 9280

# Run as non-root user (distroless nonroot user is 65532)
USER nonroot:nonroot

# Health check compatible with K8s probes
# Note: distroless doesn't have curl, K8s probes will be used instead

ENTRYPOINT ["/explorer"]
CMD ["--no-browser"]
