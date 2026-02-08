.PHONY: build install clean dev frontend backend test lint help restart restart-fe kill watch-backend watch-frontend
.PHONY: release release-binaries-dry docker docker-test docker-multiarch docker-push

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)
DOCKER_REPO ?= ghcr.io/skyhook-io/radar
RADAR_FLAGS ?=

## Build targets

# Build the complete application (frontend + embedded binary)
build: frontend embed backend
	@echo "Build complete: ./radar"

# Build and install to /usr/local/bin
install: build
	@echo "Installing to /usr/local/bin/kubectl-radar..."
	@cp radar /usr/local/bin/kubectl-radar || sudo cp radar /usr/local/bin/kubectl-radar
	@echo "Installed! Run 'kubectl radar' or 'kubectl-radar'"

# Build Go backend with embedded frontend
backend:
	@echo "Building Go backend..."
	go build -ldflags "$(LDFLAGS)" -o radar ./cmd/explorer

# Build frontend (auto-installs deps if needed)
frontend:
	@echo "Building frontend..."
	@test -d web/node_modules || (echo "Installing npm dependencies..." && cd web && npm install)
	cd web && npm run build

# Copy built frontend to embed directory
embed:
	@echo "Copying frontend to static..."
	@mkdir -p internal/static/dist
	cp -r web/dist/* internal/static/dist/

## Development targets

# Quick rebuild and restart
restart: frontend embed backend kill
	@sleep 1
	./radar --kubeconfig ~/.kube/config --no-browser &
	@sleep 4
	@echo "Server running at http://localhost:9280"

# Frontend-only rebuild and restart (faster - no Go recompile)
restart-fe: frontend embed kill
	@sleep 1
	./radar --kubeconfig ~/.kube/config --no-browser &
	@sleep 4
	@echo "Server running at http://localhost:9280"

# Hot reload development (run both in separate terminals)
# Terminal 1: make watch-frontend
# Terminal 2: make watch-backend
dev:
	@echo "=== Development Mode ==="
	@echo ""
	@echo "Run these in separate terminals:"
	@echo "  Terminal 1: make watch-frontend  (Vite dev server on :9273)"
	@echo "  Terminal 2: make watch-backend   (Go with air on :9280)"
	@echo ""
	@echo "Frontend proxies API calls to backend automatically."

# Frontend with Vite hot reload
watch-frontend:
	cd web && npm run dev

# Backend with air hot reload
# Pass extra flags: make watch-backend RADAR_FLAGS="--fake-in-cluster"
watch-backend:
	@command -v air >/dev/null 2>&1 || { echo "Installing air..."; go install github.com/air-verse/air@latest; }
	air -- $(RADAR_FLAGS)

# Run built binary
run:
	./radar --kubeconfig ~/.kube/config

# Run in dev mode (serve frontend from web/dist instead of embedded)
run-dev:
	./radar --kubeconfig ~/.kube/config --dev

## Utility targets

# Kill any running radar process
kill:
	@lsof -ti:9280 | xargs kill -9 2>/dev/null || true

# Install all dependencies
deps:
	go mod download
	go mod tidy
	cd web && npm install

# Install dev tools
install-tools:
	go install github.com/air-verse/air@latest
	cd web && npm install

# Clean build artifacts
clean:
	rm -f radar
	rm -rf web/dist
	rm -f internal/static/dist/index.html
	rm -rf internal/static/dist/assets

# Run tests
test:
	go test -v ./...

# Run linter
lint:
	go vet ./...

# Type check frontend
tsc:
	cd web && npm run tsc

# Format code
fmt:
	go fmt ./...

# ============================================================================
# Docker & Helm
# ============================================================================

# Docker build (single arch, for local testing)
# Uses --target full to build from source (the default 'release' target requires pre-built binaries)
docker:
	docker build --target full -t $(DOCKER_REPO):$(VERSION) -t $(DOCKER_REPO):latest .

# Test Docker image with read-only filesystem (simulates in-cluster with readOnlyRootFilesystem)
# Requires ~/.kube/config for cluster access; runs on port 9280
docker-test: docker
	@echo "Starting Radar with read-only filesystem (simulating in-cluster)..."
	@echo "Press Ctrl+C to stop"
	docker run --rm \
		--read-only \
		--tmpfs /tmp \
		-e HELM_CACHE_HOME=/tmp/helm/cache \
		-e HELM_CONFIG_HOME=/tmp/helm/config \
		-e HELM_DATA_HOME=/tmp/helm/data \
		-v $(HOME)/.kube/config:/home/nonroot/.kube/config:ro \
		-p 9280:9280 \
		$(DOCKER_REPO):$(VERSION) --no-browser

# Docker build multi-arch (amd64 + arm64, for production)
docker-multiarch:
	@docker buildx inspect radar-builder &>/dev/null || docker buildx create --name radar-builder --use
	docker buildx use radar-builder
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-t $(DOCKER_REPO):$(VERSION) \
		-t $(DOCKER_REPO):latest \
		--push \
		.

docker-push:
	docker push $(DOCKER_REPO):$(VERSION)
	docker push $(DOCKER_REPO):latest

# ============================================================================
# Release Targets
# ============================================================================

# Dry run goreleaser (no publish)
release-binaries-dry:
	@command -v goreleaser >/dev/null 2>&1 || { echo "Error: goreleaser not found"; exit 1; }
	goreleaser release --snapshot --clean

# Interactive release (remote via CI or local)
release:
	./scripts/release.sh

# ============================================================================
# Help
# ============================================================================

help:
	@echo "Radar - Kubernetes Cluster Visualization"
	@echo ""
	@echo "Development:"
	@echo "  make build           - Build CLI binary (frontend + embedded)"
	@echo "  make watch-frontend  - Vite dev server with HMR (port 9273)"
	@echo "  make watch-backend   - Go with air hot reload (port 9280)"
	@echo "  make run             - Run built binary"
	@echo "  make test            - Run tests"
	@echo ""
	@echo "Docker & In-Cluster:"
	@echo "  make docker           - Build Docker image (local arch)"
	@echo "  make docker-test      - Build and run with read-only filesystem (simulates in-cluster)"
	@echo "  make docker-multiarch - Build multi-arch image (amd64+arm64) and push"
	@echo "  make docker-push      - Push to GHCR"
	@echo ""
	@echo "Release:"
	@echo "  make release              - Interactive release (remote via CI or local)"
	@echo "  make release-binaries-dry - Dry run goreleaser (no publish)"
	@echo ""
	@echo "Utility:"
	@echo "  make deps       - Install all dependencies"
	@echo "  make install    - Install CLI to /usr/local/bin"
	@echo "  make clean      - Clean build artifacts"
	@echo "  make kill       - Kill running server"
