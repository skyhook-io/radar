.PHONY: build install clean dev frontend backend test lint help restart restart-fe kill watch-backend watch-frontend release

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)

## Build targets

# Build the complete application (frontend + embedded binary)
build: frontend embed backend
	@echo "Build complete: ./explorer"

# Build and install to /usr/local/bin
install: build
	@echo "Installing to /usr/local/bin/kubectl-explore..."
	@cp explorer /usr/local/bin/kubectl-explore || sudo cp explorer /usr/local/bin/kubectl-explore
	@echo "Installed! Run 'kubectl explore' or 'kubectl-explore'"

# Build Go backend with embedded frontend
backend:
	@echo "Building Go backend..."
	go build -ldflags "$(LDFLAGS)" -o explorer ./cmd/explorer

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
	./explorer --kubeconfig ~/.kube/config --no-browser --persist-history &
	@sleep 4
	@echo "Server running at http://localhost:8080"

# Frontend-only rebuild and restart (faster - no Go recompile)
restart-fe: frontend embed kill
	@sleep 1
	./explorer --kubeconfig ~/.kube/config --no-browser --persist-history &
	@sleep 4
	@echo "Server running at http://localhost:8080"

# Hot reload development (run both in separate terminals)
# Terminal 1: make watch-frontend
# Terminal 2: make watch-backend
dev:
	@echo "=== Development Mode ==="
	@echo ""
	@echo "Run these in separate terminals:"
	@echo "  Terminal 1: make watch-frontend  (Vite dev server on :5173)"
	@echo "  Terminal 2: make watch-backend   (Go with air on :8080)"
	@echo ""
	@echo "Frontend proxies API calls to backend automatically."

# Frontend with Vite hot reload
watch-frontend:
	cd web && npm run dev

# Backend with air hot reload
watch-backend:
	@command -v air >/dev/null 2>&1 || { echo "Installing air..."; go install github.com/air-verse/air@latest; }
	air

# Run built binary
run:
	./explorer --kubeconfig ~/.kube/config --persist-history

# Run in dev mode (serve frontend from web/dist instead of embedded)
run-dev:
	./explorer --kubeconfig ~/.kube/config --dev --persist-history

## Utility targets

# Kill any running explorer process
kill:
	@lsof -ti:8080 | xargs kill -9 2>/dev/null || true

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
	rm -f explorer
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

# Docker build
docker:
	docker build -t skyhook/explorer:$(VERSION) .

# Release
release:
	./release.sh



# Help
help:
	@echo "Skyhook Explorer - Kubernetes Topology Visualizer"
	@echo ""
	@echo "Build:"
	@echo "  make build      - Build everything (frontend + embedded binary)"
	@echo "  make install    - Build and install to /usr/local/bin"
	@echo "  make frontend   - Build frontend only"
	@echo "  make backend    - Build backend only"
	@echo "  make restart    - Rebuild and restart server"
	@echo ""
	@echo "Development (hot reload):"
	@echo "  make watch-frontend  - Vite dev server with HMR (port 5173)"
	@echo "  make watch-backend   - Go with air hot reload (port 8080)"
	@echo ""
	@echo "Run:"
	@echo "  make run        - Run built binary"
	@echo "  make run-dev    - Run in dev mode (frontend from filesystem)"
	@echo ""
	@echo "Utility:"
	@echo "  make deps       - Install all dependencies"
	@echo "  make kill       - Kill running server"
	@echo "  make clean      - Clean build artifacts"
	@echo "  make test       - Run tests"
