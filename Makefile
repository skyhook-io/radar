.PHONY: build install clean dev frontend backend test test-e2e lint help restart restart-fe kill watch-backend watch-frontend
.PHONY: release release-binaries-dry docker docker-test docker-multiarch docker-push
.PHONY: desktop desktop-binary desktop-dev desktop-package-darwin desktop-package-windows desktop-package-linux

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)
DOCKER_REPO ?= ghcr.io/skyhook-io/radar
RADAR_FLAGS ?=
PORT ?= 9280

## Quick in-cluster test deploy (full build: frontend + embed + Go binary)
# Usage: make deploy-test   (or make deploy-test TEST_IMAGE=... CLUSTER_NS=... CLUSTER_DEPLOY=...)
TEST_IMAGE   ?= gcr.io/koalabackend/radar:auth-rbac
CLUSTER_NS   ?= radar
CLUSTER_DEPLOY ?= radar

deploy-test: frontend embed
	@echo "=== Fast test deploy: Go build → push → rollout ==="
	@echo "Building Go binary for linux/amd64..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o /tmp/radar-linux ./cmd/explorer
	@echo "Building minimal Docker image..."
	@echo 'FROM gcr.io/distroless/static-debian12:nonroot' > /tmp/Dockerfile.test
	@echo 'COPY radar-linux /app/radar' >> /tmp/Dockerfile.test
	@echo 'ENTRYPOINT ["/app/radar"]' >> /tmp/Dockerfile.test
	docker build -t $(TEST_IMAGE) -f /tmp/Dockerfile.test /tmp
	docker push $(TEST_IMAGE)
	kubectl rollout restart deploy/$(CLUSTER_DEPLOY) -n $(CLUSTER_NS)
	kubectl rollout status deploy/$(CLUSTER_DEPLOY) -n $(CLUSTER_NS) --timeout=60s
	@echo "=== Done. Tail logs: kubectl logs -n $(CLUSTER_NS) -l app.kubernetes.io/name=$(CLUSTER_DEPLOY) -f ==="

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
	rm -rf internal/static/dist
	@mkdir -p internal/static/dist
	cp -r web/dist/* internal/static/dist/

## Development targets

# Quick rebuild and restart
restart: frontend embed backend kill
	@sleep 1
	./radar --kubeconfig ~/.kube/config --no-browser --port $(PORT) $(RADAR_FLAGS) &
	@sleep 4
	@echo "Server running at http://localhost:$(PORT)"

# Frontend-only rebuild and restart (faster - no Go recompile, serves from web/dist via --dev)
restart-fe: frontend kill
	@sleep 1
	./radar --dev --kubeconfig ~/.kube/config --no-browser --port $(PORT) $(RADAR_FLAGS) &
	@sleep 4
	@echo "Server running at http://localhost:$(PORT)"

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
	@lsof -ti:$(PORT) | xargs kill -9 2>/dev/null || true

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
	rm -f radar radar-desktop
	rm -rf web/dist
	rm -f internal/static/dist/index.html
	rm -rf internal/static/dist/assets

# Run tests
test:
	go test -v ./...

# Run e2e tests against the current kubeconfig cluster (on-demand, not in CI)
test-e2e:
	go test -tags e2e -v -timeout 5m ./internal/k8s/

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
# Desktop (Wails) Targets
# ============================================================================

# Build desktop app: frontend + Go desktop binary
desktop: frontend embed desktop-binary
	@echo "Desktop build complete: ./radar-desktop"

# Build desktop binary only (assumes frontend is already in internal/static/dist)
desktop-binary:
	@echo "Building desktop binary..."
	CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags production -ldflags "$(LDFLAGS)" -o radar-desktop ./cmd/desktop

# Run desktop app in Wails dev mode with Go hot reload.
# wails.json lives in cmd/desktop/ (Wails requires it next to the main package).
# Requires wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@latest
desktop-dev:
	@command -v wails >/dev/null 2>&1 || { echo "Error: wails CLI not found. Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"; exit 1; }
	cd cmd/desktop && wails dev -ldflags "$(LDFLAGS)"

# Package macOS .app bundle
desktop-package-darwin:
	@command -v wails >/dev/null 2>&1 || { echo "Error: wails CLI not found"; exit 1; }
	cd cmd/desktop && wails build -platform darwin/universal -ldflags "$(LDFLAGS)"

# Package Windows .exe
desktop-package-windows:
	@command -v wails >/dev/null 2>&1 || { echo "Error: wails CLI not found"; exit 1; }
	cd cmd/desktop && wails build -platform windows/amd64 -ldflags "$(LDFLAGS)"

# Package Linux binary
desktop-package-linux:
	@command -v wails >/dev/null 2>&1 || { echo "Error: wails CLI not found"; exit 1; }
	cd cmd/desktop && wails build -platform linux/amd64 -ldflags "$(LDFLAGS)"

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
	@echo "Desktop:"
	@echo "  make desktop                - Build desktop app (frontend + Wails binary)"
	@echo "  make desktop-binary         - Build desktop binary only"
	@echo "  make desktop-dev            - Run desktop in Wails dev mode"
	@echo "  make desktop-package-darwin - Package macOS .app bundle"
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
