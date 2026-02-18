.PHONY: build install clean dev frontend backend test test-unit test-frontend test-build test-e2e test-all lint help restart restart-fe kill watch-backend watch-frontend
.PHONY: release release-binaries-dry docker docker-test docker-multiarch docker-push
.PHONY: desktop desktop-binary desktop-dev desktop-package-darwin desktop-package-windows desktop-package-linux

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)
DOCKER_REPO ?= ghcr.io/skyhook-io/radar
RADAR_FLAGS ?=

# Load .env if present (exports vars for AI provider auto-detection, etc.)
-include .env
export

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
	rm -f radar radar-desktop
	rm -rf web/dist
	rm -f internal/static/dist/index.html
	rm -rf internal/static/dist/assets

# ============================================================================
# Testing
# ============================================================================

# Run all tests (unit + build + type check) — use after every feature
test-all: test-unit test-build
	@echo ""
	@echo "=== All tests passed ==="

# Run Go unit tests
test-unit:
	@echo "=== Running Go unit tests ==="
	go test ./...

# Run Go unit tests (verbose)
test:
	go test -v ./...

# Build verification (Go build + frontend build + TypeScript type check)
test-build:
	@echo "=== Verifying Go build ==="
	go build ./cmd/explorer
	go build ./cmd/desktop
	@echo "=== Verifying frontend build ==="
	@test -d web/node_modules || (cd web && npm install)
	cd web && npx tsc --noEmit
	cd web && npm run build
	@echo "=== Build verification passed ==="

# Type check frontend only
test-frontend:
	@echo "=== TypeScript type check ==="
	cd web && npx tsc --noEmit

# E2E smoke test: starts the server, hits all key endpoints, then shuts down.
# Requires a valid kubeconfig with cluster access and ANTHROPIC_API_KEY or OPENAI_API_KEY in .env.
# Usage: make test-e2e
test-e2e:
	@echo "=== Running E2E smoke tests ==="
	@echo "Starting server..."
	@lsof -ti:19280 | xargs kill -9 2>/dev/null || true
	@go build -o ./tmp/radar-e2e ./cmd/explorer
	@./tmp/radar-e2e --port 19280 --no-browser --dev &
	@E2E_PID=$$!; \
	cleanup() { kill $$E2E_PID 2>/dev/null; lsof -ti:19280 | xargs kill -9 2>/dev/null; rm -f ./tmp/radar-e2e; }; \
	trap cleanup EXIT; \
	echo "Waiting for server to be ready..."; \
	for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do \
		if curl -sf http://localhost:19280/api/health >/dev/null 2>&1; then break; fi; \
		if [ $$i -eq 20 ]; then echo "FAIL: Server did not start in time"; exit 1; fi; \
		sleep 2; \
	done; \
	echo "Server ready. Waiting for cluster connection..."; \
	for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do \
		STATE=$$(curl -sf http://localhost:19280/api/connection 2>/dev/null | python3 -c "import json,sys; print(json.load(sys.stdin).get('state',''))" 2>/dev/null); \
		if [ "$$STATE" = "connected" ]; then echo "Cluster connected."; break; fi; \
		if [ $$i -eq 30 ]; then echo "WARNING: Cluster not connected after 60s, running checks anyway..."; fi; \
		sleep 2; \
	done; \
	PASS=0; FAIL=0; \
	check() { \
		if curl -sf "http://localhost:19280$$1" >/dev/null 2>&1; then \
			echo "  PASS: $$1"; PASS=$$((PASS+1)); \
		else \
			echo "  FAIL: $$1"; FAIL=$$((FAIL+1)); \
		fi; \
	}; \
	echo "--- Core endpoints ---"; \
	check /api/health; \
	check /api/connection; \
	check /api/capabilities; \
	check /api/cluster-info; \
	check /api/dashboard; \
	check /api/namespaces; \
	check /api/api-resources; \
	check /api/sessions; \
	echo "--- Resource endpoints ---"; \
	check /api/topology; \
	check /api/resources/pods; \
	check /api/resources/deployments; \
	check /api/resources/services; \
	check /api/resources/nodes; \
	check /api/events; \
	check /api/changes; \
	echo "--- Helm endpoints ---"; \
	check /api/helm/releases; \
	echo "--- Metrics endpoints ---"; \
	check /api/metrics/top/pods; \
	check /api/metrics/top/nodes; \
	echo "--- AI endpoints ---"; \
	if curl -sf http://localhost:19280/api/ai/providers >/dev/null 2>&1; then \
		echo "  PASS: /api/ai/providers"; PASS=$$((PASS+1)); \
		check /api/ai/models; \
		echo "  Testing AI chat streaming..."; \
		CHAT_RESP=$$(curl -sf -X POST http://localhost:19280/api/ai/chat \
			-H "Content-Type: application/json" \
			-d '{"messages":[{"role":"user","content":"Say hello in one word"}]}' \
			--max-time 15 2>&1 | head -5); \
		if echo "$$CHAT_RESP" | grep -q "event: chunk"; then \
			echo "  PASS: /api/ai/chat (streaming)"; PASS=$$((PASS+1)); \
		else \
			echo "  FAIL: /api/ai/chat (streaming)"; FAIL=$$((FAIL+1)); \
		fi; \
	else \
		echo "  SKIP: /api/ai/* (no AI provider configured — set ANTHROPIC_API_KEY or OPENAI_API_KEY in .env)"; \
	fi; \
	echo "--- Debug endpoints ---"; \
	check /api/debug/informers; \
	echo ""; \
	echo "=== E2E Results: $$PASS passed, $$FAIL failed ==="; \
	if [ $$FAIL -gt 0 ]; then exit 1; fi

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
	@echo "Terrasible (Radar) - AI-Powered Kubernetes Visibility Platform"
	@echo ""
	@echo "Development:"
	@echo "  make build           - Build CLI binary (frontend + embedded)"
	@echo "  make watch-frontend  - Vite dev server with HMR (port 9273)"
	@echo "  make watch-backend   - Go with air hot reload (port 9280)"
	@echo "  make run             - Run built binary"
	@echo ""
	@echo "Testing (run after every feature!):"
	@echo "  make test-all        - Full test suite (unit + build verification)"
	@echo "  make test-unit       - Go unit tests only"
	@echo "  make test-build      - Build verification (Go + frontend + TypeScript)"
	@echo "  make test-frontend   - TypeScript type check only"
	@echo "  make test-e2e        - E2E smoke tests (requires cluster + optional AI key)"
	@echo "  make test            - Go unit tests (verbose)"
	@echo "  make lint            - Run Go vet"
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
