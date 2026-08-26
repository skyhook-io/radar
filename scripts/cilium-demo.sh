#!/usr/bin/env bash
# Bootstrap a kind cluster running Cilium with Hubble Relay and a small
# traffic-generating workload pair, so Radar's Hubble traffic source can be
# exercised against real flows in every connection lane it has: direct
# in-cluster dial (plaintext and TLS/SAN-discovery), and the port-forward
# fallback when a NetworkPolicy blocks the direct path.
# Idempotent — re-run subcommands to change modes without recreating the cluster.
#
# Subcommands:
#   up             Create cluster (if missing), install Cilium + Hubble Relay
#                  (plaintext relay — the common self-managed configuration),
#                  deploy traffic workloads.
#   down           Delete the kind cluster.
#   reset          down + up.
#   status         Cluster/relay/workload inventory + which mode is active.
#   netpol         Restrict hubble-relay ingress to kube-system. In-cluster
#                  Radar (in ns "radar") loses the direct path; with default
#                  RBAC (no pods/portforward) its connect error must name both
#                  remediations. kubectl port-forward still works — forwarded
#                  traffic enters via the kubelet, not the CNI.
#   netpol-off     Remove that policy.
#   tls            Switch the relay to serve TLS (cilium upgrade). Exercises
#                  Radar's TLS lane + SAN discovery: the relay cert's SAN is
#                  *.hubble-relay.cilium.io, NOT the k8s service DNS name, so
#                  Radar must probe the cert and retry — the AKS-shaped path.
#   plaintext      Switch the relay back to plaintext.
#   install-radar  Build the CURRENT code into a linux image, load it into the
#                  cluster, install deploy/helm/radar with DEFAULT RBAC.
#                  Proves pods/portforward stays unneeded for Hubble traffic.
#   help           Show this message.
#
# Prerequisites:
#   - kind      https://kind.sigs.k8s.io/
#   - kubectl
#   - cilium    https://docs.cilium.io/en/stable/gettingstarted/k8s-install-default/
#   - docker + helm + go (install-radar only)
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-cilium-demo).
#
# NOTES, learned the hard way:
#   - The relay Service port is 80 with a named targetPort "grpc" -> 4245. The
#     direct lane must dial the SERVICE port; port-forward must target the
#     CONTAINER port. Any change that conflates them breaks exactly one lane.
#   - Even a plaintext relay install ships hubble-relay-client-certs in
#     kube-system (relay's own client certs for talking to Hubble peers).
#     Radar loads it and still connects plaintext — TLS material existing does
#     not mean the relay serves TLS.
#   - kubectl port-forward bypasses NetworkPolicy: the stream enters through
#     the kubelet/container runtime, not the pod network. That is why the
#     fallback lane works precisely when the direct lane is blocked.
#   - Radar caches a failed direct-reachability probe per address. After
#     netpol-off, the first reconnect may still use the fallback; the
#     background re-probe restores the direct lane on the next connect.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-cilium-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"

# Pinned so the demo behaves consistently across runs. Bump deliberately.
CILIUM_VERSION="${CILIUM_VERSION:-1.18.5}"
ECHO_IMAGE="${ECHO_IMAGE:-nginx:1.27-alpine}"
CLIENT_IMAGE="${CLIENT_IMAGE:-curlimages/curl:8.10.1}"

DEMO_NS="demo"
RELAY_NS="kube-system"
RADAR_NS="radar"
NETPOL_NAME="radar-demo-restrict-relay"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE='\033[34m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RED='\033[31m'; C_DIM='\033[2m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''; C_RESET=''
fi

step()  { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()    { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn()  { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail()  { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }
note()  { printf "${C_DIM}    %s${C_RESET}\n" "$1"; }

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "$1 not found in PATH. Install: $2"
  fi
}

kctl() {
  kubectl --context "${KUBECTL_CTX}" "$@"
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

require_cluster() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run '$0 up' first."
}

relay_tls_enabled() {
  # Read cluster reality, not helm values: the chart flips the relay Service
  # port from 80 to 443 when TLS server mode is on.
  [ "$(kctl -n "${RELAY_NS}" get svc hubble-relay -o jsonpath='{.spec.ports[0].port}' 2>/dev/null)" = "443" ]
}

netpol_active() {
  kctl -n "${RELAY_NS}" get networkpolicy "${NETPOL_NAME}" >/dev/null 2>&1
}

# --- up -----------------------------------------------------------------------

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd cilium "https://docs.cilium.io/en/stable/gettingstarted/k8s-install-default/"

  if cluster_exists; then
    ok "kind cluster ${CLUSTER_NAME} already exists"
  else
    step "Creating kind cluster ${CLUSTER_NAME} (no default CNI — Cilium owns networking)"
    kind create cluster --name "${CLUSTER_NAME}" --config - <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
EOF
  fi

  if kctl -n "${RELAY_NS}" get deploy hubble-relay >/dev/null 2>&1; then
    ok "Cilium + Hubble Relay already installed"
  else
    step "Installing Cilium ${CILIUM_VERSION} with Hubble Relay (plaintext)"
    cilium install --context "${KUBECTL_CTX}" --version "${CILIUM_VERSION}" \
      --set hubble.relay.enabled=true
  fi

  step "Waiting for Cilium to be ready"
  cilium status --context "${KUBECTL_CTX}" --wait >/dev/null
  ok "Cilium ready"

  step "Deploying traffic workloads (${DEMO_NS}: echo server + curl client)"
  kctl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${DEMO_NS}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
  namespace: ${DEMO_NS}
spec:
  replicas: 2
  selector: {matchLabels: {app: echo}}
  template:
    metadata: {labels: {app: echo}}
    spec:
      containers:
        - name: echo
          image: ${ECHO_IMAGE}
          ports: [{containerPort: 80}]
          resources: {requests: {cpu: 10m, memory: 16Mi}}
---
apiVersion: v1
kind: Service
metadata:
  name: echo
  namespace: ${DEMO_NS}
spec:
  selector: {app: echo}
  ports: [{port: 80, targetPort: 80}]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: client
  namespace: ${DEMO_NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: client}}
  template:
    metadata: {labels: {app: client}}
    spec:
      containers:
        - name: client
          image: ${CLIENT_IMAGE}
          command: ["/bin/sh", "-c"]
          args: ["while true; do curl -s http://echo.${DEMO_NS}.svc/ >/dev/null 2>&1; sleep 2; done"]
          resources: {requests: {cpu: 10m, memory: 16Mi}}
EOF
  kctl -n "${DEMO_NS}" rollout status deploy/echo --timeout=120s >/dev/null
  kctl -n "${DEMO_NS}" rollout status deploy/client --timeout=120s >/dev/null
  ok "Workloads running — flows accumulate within seconds"

  echo
  ok "Demo ready. Next:"
  note "kubectl config use-context ${KUBECTL_CTX}"
  note "Run local Radar (or ./scripts/visual-test-start.sh) and open the Traffic view,"
  note "or '$0 install-radar' for the in-cluster default-RBAC lane."
}

# --- modes --------------------------------------------------------------------

cmd_netpol() {
  require_cluster
  step "Restricting hubble-relay ingress to ${RELAY_NS} (blocks in-cluster Radar's direct dial)"
  kctl apply -f - >/dev/null <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ${NETPOL_NAME}
  namespace: ${RELAY_NS}
spec:
  podSelector:
    matchLabels: {k8s-app: hubble-relay}
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels: {kubernetes.io/metadata.name: ${RELAY_NS}}
EOF
  ok "Policy applied"
  note "In-cluster Radar with default RBAC now fails BOTH lanes on reconnect; the"
  note "error must name rbac.portForward=true AND the network path. kubectl"
  note "port-forward keeps working — it enters via the kubelet, not the CNI."
}

cmd_netpol_off() {
  require_cluster
  kctl -n "${RELAY_NS}" delete networkpolicy "${NETPOL_NAME}" --ignore-not-found >/dev/null
  ok "Policy removed. Radar's cached unreachable verdict heals on the next connect."
}

cmd_tls() {
  require_cluster
  step "Switching relay to TLS (cilium upgrade — relay pod restarts)"
  cilium upgrade --context "${KUBECTL_CTX}" --version "${CILIUM_VERSION}" \
    --reuse-values --set hubble.relay.tls.server.enabled=true
  kctl -n "${RELAY_NS}" rollout status deploy/hubble-relay --timeout=180s >/dev/null
  ok "Relay now serves TLS. Server cert SAN is *.hubble-relay.cilium.io — Radar's"
  note "first TLS attempt (ServerName hubble-relay.kube-system.svc.cluster.local)"
  note "fails verification; SAN discovery must find the real name and retry."
}

cmd_plaintext() {
  require_cluster
  step "Switching relay back to plaintext (cilium upgrade — relay pod restarts)"
  cilium upgrade --context "${KUBECTL_CTX}" --version "${CILIUM_VERSION}" \
    --reuse-values --set hubble.relay.tls.server.enabled=false
  kctl -n "${RELAY_NS}" rollout status deploy/hubble-relay --timeout=180s >/dev/null
  ok "Relay back to plaintext"
}

# --- install-radar ------------------------------------------------------------

cmd_install_radar() {
  require_cluster
  require_cmd docker "https://docs.docker.com/get-docker/"
  require_cmd helm "https://helm.sh/docs/intro/install/"
  require_cmd go "https://go.dev/dl/"

  local repo_root arch tmp
  repo_root="$(cd "$(dirname "$0")/.." && pwd)"
  case "$(uname -m)" in
    arm64|aarch64) arch=arm64 ;;
    *) arch=amd64 ;;
  esac
  tmp="$(mktemp -d)"
  # Expand now: the trap fires at script exit, after this local is gone.
  trap "rm -rf '${tmp}'" EXIT

  step "Building radar for linux/${arch} from the current tree"
  (cd "${repo_root}" && GOOS=linux GOARCH="${arch}" CGO_ENABLED=0 go build -o "${tmp}/radar" ./cmd/explorer)

  step "Building and loading image radar-cilium-demo:dev"
  cat > "${tmp}/Dockerfile" <<'EOF'
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY radar /radar
ENTRYPOINT ["/radar"]
EOF
  docker build -q -t radar-cilium-demo:dev "${tmp}" >/dev/null
  kind load docker-image radar-cilium-demo:dev --name "${CLUSTER_NAME}" >/dev/null
  ok "Image loaded"

  step "Installing deploy/helm/radar with DEFAULT RBAC (rbac.portForward stays false)"
  helm --kube-context "${KUBECTL_CTX}" upgrade --install radar "${repo_root}/deploy/helm/radar" \
    --namespace "${RADAR_NS}" --create-namespace \
    --set image.repository=radar-cilium-demo --set image.tag=dev --set image.pullPolicy=Never \
    --wait --timeout 120s >/dev/null
  # On a rerun the tag is unchanged, so helm alone leaves the old pod (and its
  # old binary) running — restart to pick up the freshly loaded image.
  kctl -n "${RADAR_NS}" rollout restart deploy/radar >/dev/null
  kctl -n "${RADAR_NS}" rollout status deploy/radar --timeout=120s >/dev/null
  ok "Radar running in-cluster (current build)"

  echo
  # `kubectl auth can-i` exits 1 when the answer is no, which under pipefail
  # would poison a pipeline — capture the answer text instead.
  local can_pf
  can_pf="$(kctl auth can-i create pods/portforward -n "${RELAY_NS}" \
    --as "system:serviceaccount:${RADAR_NS}:radar" 2>/dev/null || true)"
  if [ "${can_pf}" = "no" ]; then
    ok "SA cannot create pods/portforward — Hubble traffic must work without it"
  else
    warn "SA unexpectedly HAS pods/portforward (answer: ${can_pf:-<none>}); the direct-lane proof is void"
  fi
  note "kubectl --context ${KUBECTL_CTX} -n ${RADAR_NS} port-forward deploy/radar 9280:9280"
  note "then open http://localhost:9280 -> Traffic, or:"
  note "curl -s -X POST localhost:9280/api/traffic/connect   # expect a ClusterIP address"
}

# --- status -------------------------------------------------------------------

cmd_status() {
  require_cluster
  step "Cilium"
  cilium status --context "${KUBECTL_CTX}" 2>/dev/null | sed -n '1,12p' || true
  step "Relay mode"
  if relay_tls_enabled; then ok "TLS (SAN-discovery lane)"; else ok "plaintext (common lane)"; fi
  if netpol_active; then warn "NetworkPolicy ACTIVE — direct lane blocked for in-cluster Radar"; else ok "no NetworkPolicy — direct lane open"; fi
  step "Relay endpoint"
  kctl -n "${RELAY_NS}" get svc hubble-relay -o wide 2>/dev/null || warn "relay service missing"
  step "Workloads"
  kctl -n "${DEMO_NS}" get pods 2>/dev/null || warn "demo namespace missing"
  if kctl -n "${RADAR_NS}" get deploy radar >/dev/null 2>&1; then
    step "In-cluster Radar"
    kctl -n "${RADAR_NS}" get pods
  fi
}

cmd_down() {
  if cluster_exists; then
    step "Deleting kind cluster ${CLUSTER_NAME}"
    kind delete cluster --name "${CLUSTER_NAME}"
  else
    ok "Cluster ${CLUSTER_NAME} does not exist"
  fi
}

usage() {
  sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'
}

case "${1:-help}" in
  up) cmd_up ;;
  down) cmd_down ;;
  reset) cmd_down; cmd_up ;;
  status) cmd_status ;;
  netpol) cmd_netpol ;;
  netpol-off) cmd_netpol_off ;;
  tls) cmd_tls ;;
  plaintext) cmd_plaintext ;;
  install-radar) cmd_install_radar ;;
  help|-h|--help) usage ;;
  *) fail "unknown subcommand: $1 (try '$0 help')" ;;
esac
