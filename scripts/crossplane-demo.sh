#!/usr/bin/env bash
# Bootstrap a kind cluster pre-populated with a curated set of Crossplane
# fixtures for visual-testing the Crossplane UI surfaces. Idempotent — can
# be re-run to refresh state without recreating the cluster.
#
# Subcommands:
#   up      Create cluster (if missing), install Crossplane + provider-kubernetes
#           + function-patch-and-transform, apply fixtures.
#   down    Delete the kind cluster.
#   reset   down + up.
#   status  Show what's installed and inventory the Crossplane resources.
#   help    Show this message.
#
# Prerequisites:
#   - kind     https://kind.sigs.k8s.io/
#   - kubectl
#   - helm     https://helm.sh/docs/intro/install/
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-crossplane-demo).
#
# NOTE: Crossplane v2 cluster-scoped XR composition is broken in 2.2.1 — XR
# instances created from cluster-scoped XRDs that compose provider-kubernetes
# Objects do not reconcile and stay Synced=False. We keep them in the fixture
# set on purpose: they exercise the unhealthy/stuck XR rendering path and the
# crossplaneStuck audit check. See README for details.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-crossplane-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"

# Versions pinned so the demo behaves consistently across runs. Bump when
# you want a newer release; otherwise leave alone.
CROSSPLANE_CHART_VERSION="${CROSSPLANE_CHART_VERSION:-1.17.3}"
PROVIDER_KUBERNETES_VERSION="${PROVIDER_KUBERNETES_VERSION:-v0.18.0}"
FUNCTION_PATCH_VERSION="${FUNCTION_PATCH_VERSION:-v0.9.0}"

# Pretty colors for status output. Quietly turn off in non-interactive env.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE='\033[34m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RED='\033[31m'; C_DIM='\033[2m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''; C_RESET=''
fi

step()    { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()      { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn()    { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail()    { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }
note()    { printf "${C_DIM}    %s${C_RESET}\n" "$1"; }

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "$1 not found in PATH. Install: $2"
  fi
}

kctl() {
  kubectl --context "${KUBECTL_CTX}" "$@"
}

# --- Cluster lifecycle -----------------------------------------------------

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation (or 'brew install kind')"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd helm "https://helm.sh/docs/intro/install/ (or 'brew install helm')"

  if cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists — reusing"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --wait 60s
    ok "Cluster created"
  fi

  kctl cluster-info >/dev/null || fail "kind context not reachable"

  install_crossplane
  install_provider_kubernetes
  grant_provider_cluster_admin
  install_function_patch_and_transform
  apply_provider_config
  apply_standalone_managed_resources
  apply_xrd_and_composition
  apply_xr_instances
  wait_briefly_for_reconcile
  print_summary
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  if cluster_exists; then
    step "Deleting cluster '${CLUSTER_NAME}'"
    kind delete cluster --name "${CLUSTER_NAME}"
    ok "Deleted"
  else
    warn "Cluster '${CLUSTER_NAME}' does not exist; nothing to do"
  fi
}

cmd_reset() {
  cmd_down
  cmd_up
}

# --- Crossplane ------------------------------------------------------------

install_crossplane() {
  step "Installing Crossplane (chart ${CROSSPLANE_CHART_VERSION}) into 'crossplane-system' namespace"

  helm repo add crossplane-stable https://charts.crossplane.io/stable >/dev/null 2>&1 || true
  helm repo update crossplane-stable >/dev/null

  # --wait gates on the crossplane + rbac-manager Deployments going Ready.
  helm upgrade --install crossplane crossplane-stable/crossplane \
    --kube-context "${KUBECTL_CTX}" \
    --namespace crossplane-system --create-namespace \
    --version "${CROSSPLANE_CHART_VERSION}" \
    --wait --timeout 5m >/dev/null

  ok "Crossplane healthy"
}

# --- Providers + Functions -------------------------------------------------

install_provider_kubernetes() {
  step "Installing provider-kubernetes (${PROVIDER_KUBERNETES_VERSION})"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubernetes
spec:
  package: xpkg.crossplane.io/crossplane-contrib/provider-kubernetes:${PROVIDER_KUBERNETES_VERSION}
EOF

  wait_for_provider_healthy provider-kubernetes
  ok "provider-kubernetes healthy"
}

# wait_for_provider_healthy polls the Healthy condition on a Provider object.
# Provider install pulls an OCI artifact then unpacks/installs a CRD bundle —
# typically 30–90s on a fresh cluster. Fail loudly on timeout so the caller
# knows the demo state isn't usable yet.
wait_for_provider_healthy() {
  local name="$1" deadline=$((SECONDS + 240))
  while [ $SECONDS -lt $deadline ]; do
    local healthy
    healthy=$(kctl get provider.pkg "$name" \
      -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}' 2>/dev/null || echo "")
    if [ "$healthy" = "True" ]; then
      return 0
    fi
    sleep 3
  done
  fail "Provider '$name' did not reach Healthy=True within 4m"
}

# grant_provider_cluster_admin patches the provider's auto-generated SA with
# cluster-admin so it can manage arbitrary resources. provider-kubernetes
# generates its SA name with a revision-hash suffix (e.g.
# "provider-kubernetes-d7f6a..."), so we discover it dynamically rather than
# hard-coding. Idempotent — overwrites the binding each run.
grant_provider_cluster_admin() {
  step "Granting provider-kubernetes SA cluster-admin"

  local sa
  # Wait briefly for the SA to be created (Provider install creates it
  # asynchronously after the package unpacks).
  local deadline=$((SECONDS + 120))
  while [ $SECONDS -lt $deadline ]; do
    sa=$(kctl -n crossplane-system get sa \
      -o jsonpath='{range .items[?(@.metadata.name)]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
      | grep '^provider-kubernetes-' | head -n1 || true)
    if [ -n "${sa:-}" ]; then
      break
    fi
    sleep 2
  done

  if [ -z "${sa:-}" ]; then
    fail "Could not find provider-kubernetes SA in crossplane-system"
  fi

  note "provider SA: ${sa}"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: provider-kubernetes-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: ${sa}
    namespace: crossplane-system
EOF

  ok "ClusterRoleBinding applied"
}

install_function_patch_and_transform() {
  step "Installing function-patch-and-transform (${FUNCTION_PATCH_VERSION})"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: pkg.crossplane.io/v1
kind: Function
metadata:
  name: function-patch-and-transform
spec:
  package: xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:${FUNCTION_PATCH_VERSION}
EOF

  wait_for_function_healthy function-patch-and-transform
  ok "function-patch-and-transform healthy"
}

wait_for_function_healthy() {
  local name="$1" deadline=$((SECONDS + 240))
  while [ $SECONDS -lt $deadline ]; do
    local healthy
    healthy=$(kctl get function.pkg "$name" \
      -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}' 2>/dev/null || echo "")
    if [ "$healthy" = "True" ]; then
      return 0
    fi
    sleep 3
  done
  fail "Function '$name' did not reach Healthy=True within 4m"
}

# --- ProviderConfig --------------------------------------------------------

apply_provider_config() {
  step "Applying ProviderConfig (InjectedIdentity)"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: InjectedIdentity
EOF

  ok "ProviderConfig 'default' applied"
}

# --- Standalone Managed Resources -----------------------------------------

# Two MR Object instances wrapping a ConfigMap and a Namespace. These
# exercise the standalone MR renderer path — managed resources created
# directly by the user without an XR/Composition in the picture.
apply_standalone_managed_resources() {
  step "Applying standalone Managed Resources"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: Object
metadata:
  name: standalone-configmap
spec:
  providerConfigRef:
    name: default
  forProvider:
    manifest:
      apiVersion: v1
      kind: ConfigMap
      metadata:
        name: standalone-demo
        namespace: default
      data:
        managed-by: crossplane
        purpose: radar-demo-standalone-mr
---
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: Object
metadata:
  name: standalone-namespace
spec:
  providerConfigRef:
    name: default
  forProvider:
    manifest:
      apiVersion: v1
      kind: Namespace
      metadata:
        name: crossplane-demo-managed
        labels:
          managed-by: crossplane
EOF

  ok "Standalone MRs applied (Object/standalone-configmap, Object/standalone-namespace)"
}

# --- XRD + Composition ----------------------------------------------------

# XRD: AppBundle (cluster-scoped) — apiextensions.crossplane.io/v2
# Composition wiring AppBundle → function-patch-and-transform with two
# composed resources (a ConfigMap and a Service via provider-kubernetes
# Objects). The patches use FromCompositeFieldPath with string.type: Format
# transforms so the composed names + labels derive from the XR's spec.
apply_xrd_and_composition() {
  step "Applying XRD (AppBundle) + Composition"

  cat <<'EOF' | kctl apply -f - >/dev/null
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: appbundles.demo.example.io
spec:
  scope: Cluster
  group: demo.example.io
  names:
    kind: AppBundle
    plural: appbundles
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                appName:
                  type: string
                  description: Name used for the composed ConfigMap and Service.
                namespace:
                  type: string
                  default: default
                  description: Namespace into which the composed resources are written.
              required:
                - appName
            status:
              type: object
              properties:
                composedConfigMap:
                  type: string
                composedService:
                  type: string
EOF

  cat <<'EOF' | kctl apply -f - >/dev/null
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: appbundles.demo.example.io
spec:
  compositeTypeRef:
    apiVersion: demo.example.io/v1alpha1
    kind: AppBundle
  mode: Pipeline
  pipeline:
    - step: patch-and-transform
      functionRef:
        name: function-patch-and-transform
      input:
        apiVersion: pt.fn.crossplane.io/v1beta1
        kind: Resources
        resources:
          - name: configmap
            base:
              apiVersion: kubernetes.crossplane.io/v1alpha1
              kind: Object
              spec:
                providerConfigRef:
                  name: default
                forProvider:
                  manifest:
                    apiVersion: v1
                    kind: ConfigMap
                    metadata:
                      name: placeholder
                      namespace: placeholder
                    data:
                      managed-by: crossplane-composition
            patches:
              - type: FromCompositeFieldPath
                fromFieldPath: spec.appName
                toFieldPath: spec.forProvider.manifest.metadata.name
                transforms:
                  - type: string
                    string:
                      type: Format
                      fmt: "%s-config"
              - type: FromCompositeFieldPath
                fromFieldPath: spec.namespace
                toFieldPath: spec.forProvider.manifest.metadata.namespace
              - type: FromCompositeFieldPath
                fromFieldPath: spec.appName
                toFieldPath: metadata.name
                transforms:
                  - type: string
                    string:
                      type: Format
                      fmt: "%s-configmap"
          - name: service
            base:
              apiVersion: kubernetes.crossplane.io/v1alpha1
              kind: Object
              spec:
                providerConfigRef:
                  name: default
                forProvider:
                  manifest:
                    apiVersion: v1
                    kind: Service
                    metadata:
                      name: placeholder
                      namespace: placeholder
                    spec:
                      selector:
                        app: placeholder
                      ports:
                        - port: 80
                          targetPort: 8080
            patches:
              - type: FromCompositeFieldPath
                fromFieldPath: spec.appName
                toFieldPath: spec.forProvider.manifest.metadata.name
                transforms:
                  - type: string
                    string:
                      type: Format
                      fmt: "%s-svc"
              - type: FromCompositeFieldPath
                fromFieldPath: spec.namespace
                toFieldPath: spec.forProvider.manifest.metadata.namespace
              - type: FromCompositeFieldPath
                fromFieldPath: spec.appName
                toFieldPath: spec.forProvider.manifest.spec.selector.app
              - type: FromCompositeFieldPath
                fromFieldPath: spec.appName
                toFieldPath: metadata.name
                transforms:
                  - type: string
                    string:
                      type: Format
                      fmt: "%s-service"
EOF

  ok "XRD + Composition applied"
}

# --- XR Instances ----------------------------------------------------------

apply_xr_instances() {
  step "Applying AppBundle XR instances (hello-world, greeter)"

  # Wait for the XRD to be Established before creating XR instances so the
  # apiserver has registered the CRD endpoint. Without this, the first
  # `kubectl apply` of the XR can race the XRD reconciler and fail with
  # "no matches for kind AppBundle".
  local deadline=$((SECONDS + 60))
  while [ $SECONDS -lt $deadline ]; do
    if kctl get xrd appbundles.demo.example.io \
         -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null \
         | grep -q "True"; then
      break
    fi
    sleep 2
  done

  cat <<'EOF' | kctl apply -f - >/dev/null
apiVersion: demo.example.io/v1alpha1
kind: AppBundle
metadata:
  name: hello-world
spec:
  appName: hello-world
  namespace: default
---
apiVersion: demo.example.io/v1alpha1
kind: AppBundle
metadata:
  name: greeter
spec:
  appName: greeter
  namespace: default
EOF

  ok "XR instances applied (will stay Synced=False — see README, this is intentional)"
}

# wait_briefly_for_reconcile gives the controllers a few seconds to roll
# through status updates so the summary table has populated fields rather
# than empty cells. Not gated on Synced=True because the XRs are expected
# to stay Synced=False (Crossplane v2 cluster-scoped XR limitation).
wait_briefly_for_reconcile() {
  step "Waiting briefly for reconcile loops to populate status (~10s)"
  sleep 10
  ok "done"
}

# --- Status ----------------------------------------------------------------

cmd_status() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"

  if ! cluster_exists; then
    warn "Cluster '${CLUSTER_NAME}' does not exist. Run: $0 up"
    return
  fi

  step "Cluster: ${CLUSTER_NAME} (context ${KUBECTL_CTX})"

  printf "\n${C_BLUE}Crossplane pods${C_RESET}\n"
  kctl -n crossplane-system get pods --no-headers 2>/dev/null \
    | awk '{ printf "    %s %s\n", $1, $3 }' || warn "no pods (Crossplane not installed?)"

  printf "\n${C_BLUE}Providers${C_RESET}\n"
  kctl get providers.pkg.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s installed=%s healthy=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Functions${C_RESET}\n"
  kctl get functions.pkg.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s installed=%s healthy=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}ProviderConfigs (kubernetes.crossplane.io)${C_RESET}\n"
  kctl get providerconfigs.kubernetes.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s\n", $1 }' || note "(none)"

  printf "\n${C_BLUE}XRDs${C_RESET}\n"
  kctl get xrds --no-headers 2>/dev/null \
    | awk '{ printf "    %s established=%s offered=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Compositions${C_RESET}\n"
  kctl get compositions --no-headers 2>/dev/null \
    | awk '{ printf "    %s\n", $1 }' || note "(none)"

  printf "\n${C_BLUE}CompositionRevisions${C_RESET}\n"
  kctl get compositionrevisions --no-headers 2>/dev/null \
    | awk '{ printf "    %s revision=%s\n", $1, $2 }' || note "(none)"

  printf "\n${C_BLUE}AppBundle XRs${C_RESET}\n"
  kctl get appbundles.demo.example.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s synced=%s ready=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Managed Resources (Object/kubernetes.crossplane.io)${C_RESET}\n"
  kctl get objects.kubernetes.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s synced=%s ready=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n"
}

# --- Final summary ---------------------------------------------------------

print_summary() {
  printf "\n"
  step "Demo cluster ready"
  cat <<EOF

  Context:    ${KUBECTL_CTX}

  Fixtures baked in:
    Crossplane core    — crossplane + rbac-manager in crossplane-system
    Providers          — provider-kubernetes (${PROVIDER_KUBERNETES_VERSION}), Healthy
    Functions          — function-patch-and-transform (${FUNCTION_PATCH_VERSION}), Healthy
    ProviderConfig     — kubernetes.crossplane.io/default (InjectedIdentity)
    Standalone MRs     — Object/standalone-configmap, Object/standalone-namespace
    XRD                — appbundles.demo.example.io (cluster-scoped, v2)
    Composition        — appbundles.demo.example.io (Pipeline mode)
    XR instances       — AppBundle/hello-world, AppBundle/greeter
                         (intentionally Synced=False — Crossplane v2 cluster-scoped
                          XR + provider-kubernetes Object composition is broken in
                          2.2.x; useful for testing the unhealthy XR + audit paths)

  Run Radar against this cluster:
    kubectl config use-context ${KUBECTL_CTX}
    ./scripts/visual-test-start.sh

  Other commands:
    $0 status   # inventory the cluster
    $0 reset    # nuke + recreate
    $0 down     # delete cluster

EOF
}

# --- Entry point -----------------------------------------------------------

cmd_help() {
  sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# Support both subcommand form (`up`, `down`, ...) and a flag-style alias
# (`--reset`) requested by the spec. The flag is a thin wrapper around
# `reset` so the rest of the script stays subcommand-shaped like gitops-demo.sh.
case "${1:-help}" in
  up)              cmd_up      ;;
  down)            cmd_down    ;;
  reset|--reset)   cmd_reset   ;;
  status)          cmd_status  ;;
  help|-h|--help)  cmd_help    ;;
  *)
    printf "${C_RED}Unknown subcommand: %s${C_RESET}\n\n" "$1"
    cmd_help
    exit 1
    ;;
esac
