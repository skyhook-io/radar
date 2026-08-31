.PHONY: build install clean dev frontend backend test test-e2e test-chart lint help restart restart-fe kill watch-backend watch-frontend loadtest
.PHONY: calico-demo calico-demo-down calico-demo-status
.PHONY: cilium-demo cilium-demo-down cilium-demo-status
.PHONY: gpu-ecosystem-demo gpu-ecosystem-demo-down gpu-ecosystem-demo-status
.PHONY: jobset-demo jobset-demo-down jobset-demo-reset jobset-demo-status jobset-demo-verify
.PHONY: release release-binaries-dry docker docker-test docker-multiarch docker-push
.PHONY: desktop desktop-binary desktop-dev desktop-package-darwin desktop-package-windows desktop-package-linux

VERSION ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo "dev")
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

# Build a probe image from the CURRENT code and load it into a kind cluster, for
# developing the in-cluster reachability probe. A -dirty local build's version is
# not a published tag, so the default image won't exist - load a local one and run
# radar with --reachability-image $(PROBE_IMAGE). Binary path /radar + ENTRYPOINT
# match the official image so the probe Job's `["/radar","probe",...]` command works.
KIND_CLUSTER ?= test
PROBE_IMAGE  ?= radar-probe:dev
kind-load-probe:
	@echo "Building probe binary for linux/$$(go env GOARCH)..."
	GOOS=linux GOARCH=$$(go env GOARCH) CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o /tmp/radar-probe-bin ./cmd/explorer
	@echo 'FROM gcr.io/distroless/static-debian12:nonroot' > /tmp/Dockerfile.probe
	@echo 'COPY radar-probe-bin /radar' >> /tmp/Dockerfile.probe
	@echo 'ENTRYPOINT ["/radar"]' >> /tmp/Dockerfile.probe
	docker build -t $(PROBE_IMAGE) -f /tmp/Dockerfile.probe /tmp
	kind load docker-image $(PROBE_IMAGE) --name $(KIND_CLUSTER)
	@echo "Loaded $(PROBE_IMAGE) into kind/$(KIND_CLUSTER). Run radar with: --reachability-image $(PROBE_IMAGE)"

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
	@test -f node_modules/typescript-7/bin/tsc || (echo "Installing npm dependencies..." && npm install)
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

# Kill any running radar process (on configured port and by process name)
kill:
	@lsof -ti:$(PORT) | xargs kill -9 2>/dev/null || true
	@pkill -9 -f './radar' 2>/dev/null || true

# Install all dependencies
deps:
	go mod download
	go mod tidy
	npm install

# Install dev tools
install-tools:
	go install github.com/air-verse/air@latest
	npm install

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

# Smoke-test the Helm chart's template rendering (requires `helm` on PATH)
test-chart:
	./scripts/test-chart.sh

# Bootstrap a kind cluster pre-loaded with curated GitOps scenarios
# (Argo CD + Flux + healthy/suspended/app-of-apps/ApplicationSet/etc).
# Useful for visual-testing GitOps UI changes against realistic state.
# See scripts/gitops-demo/README.md for the full coverage matrix.
gitops-demo:
	./scripts/gitops-demo.sh up

gitops-demo-down:
	./scripts/gitops-demo.sh down

gitops-demo-status:
	./scripts/gitops-demo.sh status

gitops-demo-drift:
	./scripts/gitops-demo.sh drift

# Bootstrap a kind cluster pre-loaded with Kyverno 1.18.2 and curated policy
# fixtures (both API families, the four enforcement-posture cases, dual-API
# PolicyExceptions, and ~40 real PolicyReports across several sources).
# Useful for visual-testing policy UI changes against realistic state.
# See scripts/kyverno-demo/README.md for the coverage matrix — and read the
# 1.20-simulation warning before running `kyverno-demo.sh modern-only`.
kyverno-demo:
	./scripts/kyverno-demo.sh up

kyverno-demo-down:
	./scripts/kyverno-demo.sh down

kyverno-demo-status:
	./scripts/kyverno-demo.sh status

# Bootstrap a kind cluster pre-loaded with Argo Rollouts scenarios
# (paused canary, inconclusive analysis, blue-green, aborted, workloadRef).
# Useful for visual-testing the Rollout control surface against real state.
# See scripts/rollouts-demo/README.md for the full coverage matrix.
rollouts-demo:
	./scripts/rollouts-demo.sh up

rollouts-demo-down:
	./scripts/rollouts-demo.sh down

rollouts-demo-status:
	./scripts/rollouts-demo.sh status

rollouts-demo-roll:
	./scripts/rollouts-demo.sh roll

# Bootstrap a kind cluster pre-loaded with curated Crossplane fixtures
# (core + provider-kubernetes + function-patch-and-transform + XRD/Composition/XRs).
# Useful for visual-testing Crossplane UI changes against realistic state.
# See scripts/crossplane-demo/README.md for the full coverage matrix.
crossplane-demo:
	./scripts/crossplane-demo.sh up

crossplane-demo-down:
	./scripts/crossplane-demo.sh down

crossplane-demo-status:
	./scripts/crossplane-demo.sh status

# Load-test the UI at high object counts against a synthetic, resource-free fake
# cluster (no real workloads, no kubelet, no etcd). Radar treats the fakes as a
# real cluster. Seeds a topology-realistic Deployment->ReplicaSet->Pod population
# with matching Services/ConfigMaps/Secrets, spread across nodes and namespaces.
# The pod count is live-controllable via the admin listener (default <port>+1):
#   curl -XPOST localhost:9282/loadtest/scale -d '{"pods":10000}'
#   curl localhost:9282/loadtest/status
# Override the count with PODS and the port with LOADTEST_PORT.
LOADTEST_PORT ?= 9281
PODS ?= 50000
loadtest: frontend embed
	go run ./cmd/testserver -port $(LOADTEST_PORT) -pods $(PODS)

# Bootstrap a kind cluster pre-loaded with curated Velero fixtures (all 13
# Backup phases, the supersession series, a paused+invalid Schedule, an
# unavailable BSL, a restic repository, and the rancher plural collision).
# The Velero controller is deliberately scaled to 0, so the fixtures' own
# status survives and all thirteen phases are on screen at once. Run
# ./scripts/velero-demo.sh live for the other half: MinIO plus a running
# controller, which earns the states instead of asserting them.
# See scripts/velero-demo/README.md for the coverage matrix.
velero-demo:
	./scripts/velero-demo.sh up

velero-demo-live:
	./scripts/velero-demo.sh live

velero-demo-down:
	./scripts/velero-demo.sh down

velero-demo-status:
	./scripts/velero-demo.sh status

# Bootstrap a kind cluster pre-loaded with curated CloudNativePG fixtures
# (four clusters all 2/2 Ready with four different badges, a real WAL-archiving
# failure, Pooler/ScheduledBackup/Backups, plus Velero + KubeBlocks CRs for the
# shared `backups`/`clusters` plurals).
# See scripts/cnpg-demo/README.md for the coverage matrix and, more importantly,
# why the WAL failure is induced rather than patched.
cnpg-demo:
	./scripts/cnpg-demo.sh up

cnpg-demo-down:
	./scripts/cnpg-demo.sh down

cnpg-demo-status:
	./scripts/cnpg-demo.sh status

# Live CNPG operator for real failovers and backup runs. Frozen-only terminal
# phases and Backup rows are omitted because the controller owns them.
cnpg-demo-live:
	./scripts/cnpg-demo.sh live

# Grafana Beyla on kind: eBPF loaded, a minimal Prometheus scraping it, and two
# conversations to observe. Which labels Beyla exports depends on configuration —
# dst_port and transport are off by default, direction is on and doubles every
# edge — so read scripts/beyla-demo/README.md before changing the Beyla source.
# `attrs` and `no-network` switch between the three configurations that matter.
beyla-demo:
	./scripts/beyla-demo.sh up

beyla-demo-down:
	./scripts/beyla-demo.sh down

beyla-demo-status:
	./scripts/beyla-demo.sh status

# Bootstrap a kind cluster with Cilium + Hubble Relay and traffic workloads,
# for exercising every Hubble connection lane: direct in-cluster dial
# (plaintext and TLS/SAN-discovery via `tls`), and the port-forward fallback
# when `netpol` blocks the direct path. `install-radar` builds the current
# tree into the cluster with DEFAULT chart RBAC to prove pods/portforward
# stays unneeded. See scripts/cilium-demo/README.md.
cilium-demo:
	./scripts/cilium-demo.sh up

cilium-demo-down:
	./scripts/cilium-demo.sh down

cilium-demo-status:
	./scripts/cilium-demo.sh status

# Pinned upstream CRDs + deterministic fixtures for Radar's 37 curated GPU,
# batch, distributed-training, and inference resource identities.
gpu-ecosystem-demo:
	./scripts/gpu-ecosystem-demo.sh up

gpu-ecosystem-demo-down:
	./scripts/gpu-ecosystem-demo.sh down

gpu-ecosystem-demo-status:
	./scripts/gpu-ecosystem-demo.sh status

# Focused live JobSet controller lane. Complements the controller-free GPU
# breadth fixtures with role/index lineage, dependency gating, and a terminal
# failure. See scripts/jobset-demo/README.md for the proof boundary.
jobset-demo:
	./scripts/jobset-demo.sh up

jobset-demo-down:
	./scripts/jobset-demo.sh down

jobset-demo-reset:
	./scripts/jobset-demo.sh reset

jobset-demo-status:
	./scripts/jobset-demo.sh status

jobset-demo-verify:
	./scripts/jobset-demo.sh verify

# Bootstrap a kind cluster running real Calico with its aggregated API server,
# plus the policy shapes the Calico surfaces render (both API groups serving the
# same objects, all three staged kinds including a staged deletion, a non-default
# tier, IP pools and a HostEndpoint).
# See scripts/calico-demo/README.md for the coverage matrix and the two Calico
# behaviours that are easy to get wrong without a cluster to check against.
calico-demo:
	./scripts/calico-demo.sh up

calico-demo-down:
	./scripts/calico-demo.sh down

calico-demo-status:
	./scripts/calico-demo.sh status

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
	@echo "Demo clusters:"
	@echo "  make gitops-demo      - GitOps fixtures (Argo CD + Flux)"
	@echo "  make crossplane-demo  - Crossplane fixtures"
	@echo "  make kyverno-demo     - Live Kyverno policy + report fixtures"
	@echo "  make rollouts-demo    - Argo Rollouts progression fixtures"
	@echo "  make cnpg-demo        - Frozen CNPG rendering fixtures"
	@echo "  make cnpg-demo-live   - CNPG fixtures with the operator running"
	@echo "  make velero-demo      - Velero fixtures, all 13 backup phases at once"
	@echo "  make velero-demo-live - Velero with real object storage; states produced by the controller"
	@echo "  make beyla-demo       - Grafana Beyla eBPF traffic fixtures"
	@echo "  make cilium-demo      - Cilium + Hubble Relay, all Radar connection lanes"
	@echo "  make gpu-ecosystem-demo - 37 GPU, batch, and AI/ML resource fixtures"
	@echo "  make jobset-demo      - Live JobSet role, dependency, and failure fixtures"
	@echo "  make calico-demo      - Real Calico, both API groups, staged policies"
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
