#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="$SCRIPT_DIR/workload-metrics-demo"
export CLUSTER_NAME=radar-workload-metrics-demo
export RADAR_METRICS_DEMO_HEADER=true
export KUBECONFIG="${RADAR_METRICS_DEMO_STATE:-${TMPDIR:-/tmp}/radar-workload-metrics-demo}/kubeconfig"
CTX="kind-$CLUSTER_NAME"
umask 077

kctl() { kubectl --kubeconfig "$KUBECONFIG" --context "$CTX" "$@"; }
hctl() { helm --kubeconfig "$KUBECONFIG" --kube-context "$CTX" "$@"; }

case "${1:-help}" in
  up)
    mkdir -p "$(dirname "$KUBECONFIG")"
    if ! kind get clusters | grep -qx "$CLUSTER_NAME"; then
      kind create cluster --name "$CLUSTER_NAME" --image kindest/node:v1.36.1 --kubeconfig "$KUBECONFIG" --wait 120s
    else
      kind export kubeconfig --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG"
    fi
    bash "$SCRIPT_DIR/beyla-demo.sh" up
    hctl upgrade beyla beyla --repo https://grafana.github.io/helm-charts --version 1.16.10 -n beyla --reuse-values -f "$FIXTURES/beyla-values.yaml" --wait --timeout 5m
    kctl -n demo scale deployment/client deployment/worker --replicas=0
    kctl -n demo set resources deployment/web --limits=cpu=200m,memory=64Mi
    kctl -n demo rollout status deployment/web --timeout=180s
    kctl apply -f "$FIXTURES/collector.yaml"
    kctl -n monitoring create configmap prometheus-config --from-file=prometheus.yml="$FIXTURES/prometheus.yaml" --dry-run=client -o yaml | kctl apply -f -
    kctl -n monitoring rollout restart deployment/prometheus
    hctl upgrade --install radar-istio-base base --repo https://istio-release.storage.googleapis.com/charts --version 1.30.3 --namespace radar-metrics-istio-system --create-namespace -f "$FIXTURES/istio-base-values.yaml" --wait --timeout 5m
    hctl upgrade --install radar-istio istiod --repo https://istio-release.storage.googleapis.com/charts --version 1.30.3 --namespace radar-metrics-istio-system -f "$FIXTURES/istio-values.yaml" --wait --timeout 5m
    kctl apply -f "$FIXTURES/fixtures.yaml"
    kctl -n monitoring rollout status deployment/prometheus --timeout=180s
    kctl -n monitoring rollout status deployment/kube-state-metrics --timeout=180s
    for name in web worker; do kctl -n radar-metrics-fixture rollout status "deployment/$name" --timeout=180s; done
    kctl -n radar-metrics-fixture rollout status statefulset/redis --timeout=180s
    kctl -n radar-metrics-fixture rollout status daemonset/node-worker --timeout=180s
    printf 'Ready. Isolated kubeconfig: %s\nRun: bash %s traffic\nThen: bash %s check\n' "$KUBECONFIG" "$0" "$0"
    ;;
  traffic) kctl create -f "$FIXTURES/traffic.yaml" ;;
  status) kctl get pods -n radar-metrics-fixture; kctl get pods -n monitoring; kctl get jobs -n radar-metrics-fixture ;;
  check) node "$FIXTURES/check.mjs" "$KUBECONFIG" "$CTX" ;;
  history) node "$FIXTURES/check.mjs" "$KUBECONFIG" "$CTX" history ;;
  down) kind delete cluster --name "$CLUSTER_NAME" --kubeconfig "$KUBECONFIG" ;;
  *) printf 'Usage: bash %s {up|traffic|status|check|history|down}\nRead %s/README.md first.\n' "$0" "$FIXTURES" ;;
esac
