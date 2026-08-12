#!/usr/bin/env bash

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-ig-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"
IG_VERSION="${IG_VERSION:-0.54.1}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="${SCRIPT_DIR}/ig-demo/fixtures.yaml"

fail() { printf 'error: %s\n' "$1" >&2; exit 1; }
step() { printf '==> %s\n' "$1"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

up() {
  require_cmd kind
  require_cmd kubectl
  require_cmd helm

  if ! cluster_exists; then
    step "Creating kind cluster ${CLUSTER_NAME}"
    kind create cluster --name "${CLUSTER_NAME}" --wait 60s
  fi

  step "Installing Inspektor Gadget ${IG_VERSION}"
  helm repo add gadget https://inspektor-gadget.github.io/charts >/dev/null 2>&1 || true
  helm repo update gadget >/dev/null
  helm upgrade --install gadget gadget/gadget \
    --kube-context "${KUBECTL_CTX}" \
    --namespace gadget --create-namespace \
    --version "${IG_VERSION}" \
    --wait --timeout 5m >/dev/null

  step "Applying Live Debug fixtures"
  kubectl --context "${KUBECTL_CTX}" apply -f "${FIXTURES}" >/dev/null
  kubectl --context "${KUBECTL_CTX}" -n ig-demo rollout status deployment/server deployment/probe --timeout=180s
  status
}

down() {
  require_cmd kind
  if cluster_exists; then
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
}

status() {
  require_cmd kubectl
  step "Inspektor Gadget"
  kubectl --context "${KUBECTL_CTX}" -n gadget get daemonset,pods -l k8s-app=gadget -o wide
  step "Fixtures"
  kubectl --context "${KUBECTL_CTX}" -n ig-demo get pods,service -o wide
  printf '\nUse: kubectl config use-context %s\n' "${KUBECTL_CTX}"
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  reset) down; up ;;
  status) status ;;
  *) fail "usage: $0 {up|down|reset|status}" ;;
esac
