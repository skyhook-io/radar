#!/usr/bin/env bash
# Bootstrap a kind cluster running Grafana Beyla (eBPF) plus a minimal Prometheus
# and a small traffic-generating workload set, so the metrics Beyla actually
# publishes can be scraped and inspected. Built to answer "what is the network
# flow metric really called, and which labels does it really carry" empirically.
# Idempotent — re-run to refresh state without recreating the cluster.
#
# Subcommands:
#   up            Create cluster (if missing), deploy workloads + Prometheus + Beyla
#                 with the network and application features on and DEFAULT attributes.
#   down          Delete the kind cluster.
#   reset         down + up.
#   status        Cluster/pod/target inventory, exported metric names, observed label
#                 sets and label values, series counts.
#   query         Run the exact PromQL the Radar Beyla traffic source uses (L4 + L7)
#                 and print the returned series.
#   default       Beyla config A: network + application, default attributes.
#                 dst_port and transport are NOT exported in this mode — that is the point.
#   attrs         Beyla config B: same, plus attributes.select adding dst.port and
#                 transport. Watch the series count explode via ephemeral ports.
#   no-network    Beyla config C: application only. The network flow metric disappears.
#   port-forward  Port-forward Prometheus to localhost:$PROM_LOCAL_PORT (foreground).
#   help          Show this message.
#
# Prerequisites:
#   - kind     https://kind.sigs.k8s.io/
#   - kubectl
#   - helm     https://helm.sh/docs/intro/install/
#   - curl, python3
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-beyla-demo).
#
# NOTES, learned the hard way:
#   - Beyla's network flow metric exports `dst_port` and `transport` ONLY when they are
#     named in attributes.select. They are Default:false in OBI's attribute registry.
#     A stock install exports neither, so any consumer grouping by them gets empty labels.
#   - `direction` (request/response/unknown) IS on by default. Every conversation is
#     reported twice, once per direction, with src and dst swapped. Aggregating without
#     it produces a mirrored edge for every real edge. Radar keeps direction="request"
#     only: UDP is reported as "unknown" on BOTH ends, so it cannot be oriented at all
#     and is excluded rather than drawn with an invented arrow.
#   - Selecting dst.port makes the response-direction series carry the client's EPHEMERAL
#     port, which is a cardinality bomb: ~400 mirror series for 2 forward series here.
#   - kind nodes do not mount bpffs, so Beyla logs a warning and disables pinned-map
#     features. Network and HTTP metrics are unaffected; ignore it.
#   - Beyla is privileged with hostPID and hostNetwork, which its docs require. Keep this
#     inside a throwaway kind cluster.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-beyla-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"

# Versions pinned so the demo behaves consistently across runs. Bump deliberately.
BEYLA_CHART_VERSION="${BEYLA_CHART_VERSION:-1.16.10}"
PROMETHEUS_IMAGE="${PROMETHEUS_IMAGE:-prom/prometheus:v3.5.0}"
PROM_LOCAL_PORT="${PROM_LOCAL_PORT:-39090}"

DEMO_NS="demo"
BEYLA_NS="beyla"
MONITORING_NS="monitoring"

# The Radar traffic source scopes its queries with this matcher. Kept here so the
# `query` subcommand runs byte-identical PromQL to the product code.
JOB_SELECTOR='job=~".*beyla.*|.*alloy.*"'

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

helm_ctx() {
  helm --kube-context "${KUBECTL_CTX}" "$@"
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

require_cluster() {
  cluster_exists || fail "Cluster '${CLUSTER_NAME}' does not exist. Run '$0 up' first."
}

# --- Beyla values ----------------------------------------------------------

# The chart derives pod security from `preset`:
#   preset: application            -> hostPID: true, discovery.instrument all namespaces
#   config.data.network / preset: network -> hostNetwork: true + ClusterFirstWithHostNet
# We want BOTH feature sets, so we use preset=application and enable network in the
# config body. OTEL_EBPF_METRICS_FEATURES is the authoritative feature switch;
# without "network" in it the flow metric is never exported.
beyla_values_common() {
  cat <<'YAML'
preset: application

resources:
  requests:
    cpu: 50m
    memory: 128Mi
  limits:
    memory: 512Mi

tolerations:
  - operator: Exists
YAML
}

beyla_values_default() {
  beyla_values_common
  cat <<'YAML'
env:
  OTEL_EBPF_METRICS_FEATURES: "application,network"
  OTEL_EBPF_LOG_LEVEL: "info"

config:
  create: true
  data:
    attributes:
      kubernetes:
        enable: true
    network:
      enable: true
    discovery:
      instrument:
        - k8s_namespace: "*"
    prometheus_export:
      port: 9090
      path: /metrics
YAML
}

beyla_values_attrs() {
  beyla_values_common
  cat <<'YAML'
env:
  OTEL_EBPF_METRICS_FEATURES: "application,network"
  OTEL_EBPF_LOG_LEVEL: "info"

config:
  create: true
  data:
    attributes:
      kubernetes:
        enable: true
      # dst.port and transport are Default:false in OBI's attribute registry, so a
      # stock install exports neither. Naming them here is the only way to get them.
      select:
        beyla_network_flow_bytes:
          include:
            - k8s.src.owner.name
            - k8s.src.namespace
            - k8s.src.owner.type
            - k8s.dst.owner.name
            - k8s.dst.namespace
            - k8s.dst.owner.type
            - dst.port
            - transport
            - direction
    network:
      enable: true
    discovery:
      instrument:
        - k8s_namespace: "*"
    prometheus_export:
      port: 9090
      path: /metrics
YAML
}

beyla_values_no_network() {
  beyla_values_common
  cat <<'YAML'
env:
  OTEL_EBPF_METRICS_FEATURES: "application"
  OTEL_EBPF_LOG_LEVEL: "info"

config:
  create: true
  data:
    attributes:
      kubernetes:
        enable: true
    discovery:
      instrument:
        - k8s_namespace: "*"
    prometheus_export:
      port: 9090
      path: /metrics
YAML
}

apply_beyla() {
  local mode="$1" values
  values="$(mktemp)"
  case "$mode" in
    default)    beyla_values_default    > "$values" ;;
    attrs)      beyla_values_attrs      > "$values" ;;
    no-network) beyla_values_no_network > "$values" ;;
    *) fail "unknown Beyla mode: $mode" ;;
  esac

  step "Installing Beyla (mode: ${mode})"
  helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1 || true
  helm repo update grafana >/dev/null 2>&1 || true
  # --reset-values so a previous mode's config.data does not deep-merge into this one.
  helm_ctx upgrade --install beyla grafana/beyla \
    --version "${BEYLA_CHART_VERSION}" \
    -n "${BEYLA_NS}" --create-namespace \
    -f "$values" --reset-values --wait --timeout 5m >/dev/null
  rm -f "$values"
  ok "Beyla release at revision $(helm_ctx list -n ${BEYLA_NS} -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["revision"])')"
  note "Beyla image: $(kctl get ds -n ${BEYLA_NS} beyla -o jsonpath='{.spec.template.spec.containers[0].image}')"
}

# --- Manifests -------------------------------------------------------------

apply_workloads() {
  step "Deploying traffic workloads in namespace '${DEMO_NS}'"
  # client -> web on HTTP :80  (produces both L4 flows and L7 http_server_* metrics)
  # client -> db  on TCP :6379 (non-HTTP second destination, for the multi-port case)
  kctl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: ${DEMO_NS}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: ${DEMO_NS}
spec:
  # Two replicas, because Beyla's L7 metrics are reported per pod. A destination
  # with one pod cannot show whether a consumer combines those series or lets one
  # replica's figure stand in for the whole destination.
  replicas: 2
  selector:
    matchLabels: { app: web }
  template:
    metadata:
      labels: { app: web }
    spec:
      containers:
        - name: nginx
          image: nginx:1.27-alpine
          ports: [{ containerPort: 80 }, { containerPort: 8080 }]
          volumeMounts:
            - { name: conf, mountPath: /etc/nginx/conf.d }
          resources:
            requests: { cpu: 10m, memory: 16Mi }
            limits: { memory: 64Mi }
      volumes:
        - name: conf
          configMap: { name: web-conf }
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: web-conf
  namespace: ${DEMO_NS}
data:
  # /boom returns 500 so the error path has something to report. Without it the
  # graph's "Errors (5xx)" legend entry can never light up on this cluster, and an
  # error rate of zero proves nothing about whether the code reads it correctly.
  default.conf: |
    server {
      listen 80;
      location / { return 200 'ok\n'; }
      location /boom { return 500 'boom\n'; }
    }
    # A second served port, so the destination is genuinely multi-port rather than
    # multi-port only by way of ephemeral client ports. Both ports fail some of the
    # time, so a destination-wide error figure has to combine them rather than
    # report whichever port was busiest.
    server {
      listen 8080;
      location / { return 200 'ok\n'; }
      location /bad { return 500 'bad\n'; }
    }
---
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: ${DEMO_NS}
spec:
  selector: { app: web }
  ports:
    - { name: http, port: 80, targetPort: 80 }
    - { name: http-alt, port: 8080, targetPort: 8080 }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: db
  namespace: ${DEMO_NS}
spec:
  replicas: 1
  selector:
    matchLabels: { app: db }
  template:
    metadata:
      labels: { app: db }
    spec:
      containers:
        - name: redis
          image: redis:7-alpine
          args: ["--save", "", "--appendonly", "no"]
          ports: [{ containerPort: 6379 }]
          resources:
            requests: { cpu: 10m, memory: 16Mi }
            limits: { memory: 64Mi }
---
apiVersion: v1
kind: Service
metadata:
  name: db
  namespace: ${DEMO_NS}
spec:
  selector: { app: db }
  ports: [{ name: redis, port: 6379, targetPort: 6379 }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: client
  namespace: ${DEMO_NS}
spec:
  replicas: 1
  selector:
    matchLabels: { app: client }
  template:
    metadata:
      labels: { app: client }
    spec:
      containers:
        - name: gen
          image: busybox:1.36
          command: ["/bin/sh", "-c"]
          args:
            - |
              while true; do
                for i in 1 2 3 4 5; do
                  wget -q -T 3 -O /dev/null "http://web.${DEMO_NS}.svc.cluster.local/" || true
                done
                # One 500 per five successes, so the 5xx rate is a visible minority.
                wget -q -T 3 -O /dev/null "http://web.${DEMO_NS}.svc.cluster.local/boom" || true
                printf 'PING\r\n' | timeout 2 nc -w 2 db.${DEMO_NS}.svc.cluster.local 6379 >/dev/null 2>&1 || true
                sleep 1
              done
          resources:
            requests: { cpu: 10m, memory: 16Mi }
            limits: { memory: 64Mi }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: worker
  namespace: ${DEMO_NS}
spec:
  replicas: 1
  selector:
    matchLabels: { app: worker }
  template:
    metadata:
      labels: { app: worker }
    spec:
      containers:
        - name: gen
          image: busybox:1.36
          command: ["/bin/sh", "-c"]
          # A second caller reaching the same destination, on the other port and at
          # a much lower volume than client. The HTTP metric carries no caller
          # labels, so a destination's request rate has to be divided between its
          # callers; with a single caller that division is the identity and proves
          # nothing. The volumes are deliberately lopsided so a rate copied to both
          # callers is obvious rather than plausible.
          args:
            - |
              while true; do
                wget -q -T 3 -O /dev/null "http://web.${DEMO_NS}.svc.cluster.local:8080/" || true
                wget -q -T 3 -O /dev/null "http://web.${DEMO_NS}.svc.cluster.local:8080/bad" || true
                sleep 5
              done
          resources:
            requests: { cpu: 10m, memory: 16Mi }
            limits: { memory: 64Mi }
YAML
  ok "web (2 replicas, :80 and :8080, each with a failing route), db (:6379), and two callers applied"
}

apply_prometheus() {
  step "Deploying Prometheus in namespace '${MONITORING_NS}'"
  # Deliberately minimal: one Deployment, pod service discovery, 2h retention. All we
  # need is a scrape of Beyla plus a query endpoint. The scrape job is named "beyla" so
  # Radar's default job matcher (.*beyla.*|.*alloy.*) applies unchanged.
  kctl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: ${MONITORING_NS}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: prometheus
  namespace: ${MONITORING_NS}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: prometheus-${CLUSTER_NAME}
rules:
  - apiGroups: [""]
    resources: ["nodes", "services", "endpoints", "pods"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: prometheus-${CLUSTER_NAME}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: prometheus-${CLUSTER_NAME}
subjects:
  - kind: ServiceAccount
    name: prometheus
    namespace: ${MONITORING_NS}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: prometheus-config
  namespace: ${MONITORING_NS}
data:
  prometheus.yml: |
    global:
      scrape_interval: 15s
      evaluation_interval: 15s
    scrape_configs:
      # Beyla runs with hostNetwork, so its pod IP is the node IP and its
      # prometheus_export port is reachable directly.
      - job_name: beyla
        kubernetes_sd_configs:
          - role: pod
        relabel_configs:
          - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_name]
            action: keep
            regex: beyla
          - source_labels: [__meta_kubernetes_pod_container_port_number]
            action: keep
            regex: "9090"
          - source_labels: [__meta_kubernetes_pod_node_name]
            target_label: node
      - job_name: prometheus
        static_configs:
          - targets: ["localhost:9090"]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: prometheus
  namespace: ${MONITORING_NS}
spec:
  replicas: 1
  selector:
    matchLabels: { app: prometheus }
  template:
    metadata:
      labels: { app: prometheus }
    spec:
      serviceAccountName: prometheus
      containers:
        - name: prometheus
          image: ${PROMETHEUS_IMAGE}
          args:
            - --config.file=/etc/prometheus/prometheus.yml
            - --storage.tsdb.path=/prometheus
            - --storage.tsdb.retention.time=2h
            - --web.enable-lifecycle
          ports: [{ containerPort: 9090 }]
          volumeMounts:
            - { name: config, mountPath: /etc/prometheus }
            - { name: data, mountPath: /prometheus }
          resources:
            requests: { cpu: 50m, memory: 128Mi }
            limits: { memory: 512Mi }
      volumes:
        - name: config
          configMap: { name: prometheus-config }
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: prometheus
  namespace: ${MONITORING_NS}
spec:
  selector: { app: prometheus }
  ports: [{ name: http, port: 9090, targetPort: 9090 }]
YAML
  ok "Prometheus applied (scrape job 'beyla', 15s interval)"
}

# --- Prometheus access -----------------------------------------------------

# Query Prometheus from inside the cluster node, so no port-forward is needed for
# the status/query subcommands.
prom_api() {
  local path="$1"
  kctl -n "${MONITORING_NS}" exec deploy/prometheus -c prometheus -- \
    /bin/sh -c "wget -q -O - 'http://127.0.0.1:9090${path}'" 2>/dev/null
}

prom_query() {
  local q="$1"
  # urlencode via python3 to keep PromQL byte-identical to the product's queries.
  local enc
  enc="$(python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$q")"
  prom_api "/api/v1/query?query=${enc}"
}

print_series() {
  python3 -c '
import json,sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("    (no/invalid response from Prometheus)"); sys.exit(0)
res = d.get("data", {}).get("result", [])
print("    series: %d" % len(res))
for s in sorted(res, key=lambda x: json.dumps(x["metric"], sort_keys=True)):
    m = dict(s["metric"])
    for k in ("__name__", "instance", "job", "node"):
        m.pop(k, None)
    v = s["value"][1]
    try:
        v = round(float(v), 4)
    except Exception:
        pass
    print("      %s => %s" % (json.dumps(m, sort_keys=True), v))
'
}

# Label-value enumeration covers the whole retention window, so dst_port can hold
# thousands of stale ephemeral ports after a run in `attrs` mode. Summarise instead of
# dumping — the count is the interesting number there anyway.
label_values() {
  prom_api "/api/v1/label/$1/values" | python3 -c '
import json,sys
try:
    v = json.load(sys.stdin)["data"]
except Exception:
    print("(unavailable)"); sys.exit(0)
if not v:
    print("[]  <- label not exported")
elif len(v) <= 12:
    print(v)
else:
    print("%d distinct values, first 12: %s ..." % (len(v), v[:12]))
'
}

# --- Subcommands -----------------------------------------------------------

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation (or 'brew install kind')"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd helm "https://helm.sh/docs/intro/install/ (or 'brew install helm')"
  require_cmd python3 "https://www.python.org/downloads/"

  if cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists — reusing"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --wait 90s
    ok "Cluster created"
  fi

  kctl cluster-info >/dev/null || fail "kind context '${KUBECTL_CTX}' not reachable"

  if ! kctl get --raw /sys/kernel/btf/vmlinux >/dev/null 2>&1; then
    : # not fetchable via the API; checked on the node instead below
  fi

  apply_workloads
  apply_prometheus
  apply_beyla default

  step "Waiting for pods"
  kctl -n "${DEMO_NS}" rollout status deploy/web --timeout=180s >/dev/null
  kctl -n "${DEMO_NS}" rollout status deploy/db --timeout=180s >/dev/null
  kctl -n "${DEMO_NS}" rollout status deploy/client --timeout=180s >/dev/null
  kctl -n "${MONITORING_NS}" rollout status deploy/prometheus --timeout=180s >/dev/null
  kctl -n "${BEYLA_NS}" rollout status ds/beyla --timeout=180s >/dev/null
  ok "All workloads ready"

  warn "These are RATE metrics over a 5-minute window. Give it ~6 minutes of steady"
  warn "traffic before trusting 'status' or 'query' output."
  note "Then: $0 status    and    $0 query"
}

cmd_down() {
  if cluster_exists; then
    step "Deleting kind cluster '${CLUSTER_NAME}'"
    kind delete cluster --name "${CLUSTER_NAME}"
    ok "Deleted"
  else
    warn "Cluster '${CLUSTER_NAME}' does not exist — nothing to delete"
  fi
}

cmd_status() {
  require_cluster

  step "Cluster"
  note "context:    ${KUBECTL_CTX}"
  note "k8s:        $(kctl version -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"])' 2>/dev/null || echo unknown)"
  note "node kernel: $(docker exec "${CLUSTER_NAME}-control-plane" uname -r 2>/dev/null || echo unknown)"

  step "Pods"
  kctl get pods -A 2>/dev/null | grep -vE "kube-system|local-path" || true

  step "Beyla"
  note "chart:  $(helm_ctx list -n ${BEYLA_NS} -o json 2>/dev/null | python3 -c 'import json,sys; r=json.load(sys.stdin); print(r[0]["chart"]+" (app "+r[0]["app_version"]+")") if r else print("not installed")' 2>/dev/null || echo unknown)"
  note "image:  $(kctl get ds -n ${BEYLA_NS} beyla -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo n/a)"
  note "digest: $(kctl get pods -n ${BEYLA_NS} -o jsonpath='{.items[0].status.containerStatuses[0].imageID}' 2>/dev/null || echo n/a)"
  note "hostNetwork=$(kctl get ds -n ${BEYLA_NS} beyla -o jsonpath='{.spec.template.spec.hostNetwork}' 2>/dev/null) hostPID=$(kctl get ds -n ${BEYLA_NS} beyla -o jsonpath='{.spec.template.spec.hostPID}' 2>/dev/null)"
  note "features: $(kctl get ds -n ${BEYLA_NS} beyla -o jsonpath='{range .spec.template.spec.containers[0].env[?(@.name=="OTEL_EBPF_METRICS_FEATURES")]}{.value}{end}' 2>/dev/null)"
  echo
  note "startup mode lines:"
  kctl logs -n "${BEYLA_NS}" ds/beyla 2>/dev/null | grep -E "starting Beyla in|Flows agent successfully" | sed 's/^/      /' || true

  step "Prometheus targets"
  prom_api "/api/v1/targets" | python3 -c '
import json,sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("    (unavailable)"); sys.exit(0)
for t in d["data"]["activeTargets"]:
    print("    %-40s %-6s %s" % (t["labels"].get("instance"), t["health"], t.get("lastError","")))
' || true

  step "Metric names Beyla exposes (runtime metrics filtered out)"
  prom_api "/api/v1/label/__name__/values" | python3 -c '
import json,sys
skip = ("go_","process_","prometheus_","promhttp_","net_conntrack_","scrape_")
try:
    d = json.load(sys.stdin)["data"]
except Exception:
    print("    (unavailable)"); sys.exit(0)
for n in d:
    if not n.startswith(skip):
        print("      " + n)
print()
print("    obi_-prefixed:   %s" % ([n for n in d if n.startswith("obi")] or "NONE"))
print("    beyla_-prefixed: %s" % ([n for n in d if n.startswith("beyla")] or "NONE"))
' || true

  step "Label names on beyla_network_flow_bytes_total"
  prom_query 'beyla_network_flow_bytes_total' | python3 -c '
import json,sys
try:
    res = json.load(sys.stdin)["data"]["result"]
except Exception:
    print("    (unavailable)"); sys.exit(0)
if not res:
    print("    NO SERIES — is the network feature enabled? (see: $0 default)")
    sys.exit(0)
keys = set()
for s in res: keys |= set(s["metric"])
print("    series: %d" % len(res))
for k in sorted(keys): print("      " + k)
' || true

  step "Label values"
  for L in direction transport dst_port k8s_src_owner_type k8s_dst_owner_type server_port; do
    printf "    %-22s %s\n" "$L" "$(label_values "$L")"
  done

  step "Series counts"
  printf "    %-42s %s\n" "beyla_network_flow_bytes_total" "$(prom_query 'count(beyla_network_flow_bytes_total)' | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else "0 (metric absent)")' 2>/dev/null)"
  printf "    %-42s %s\n" "http_server_request_duration_seconds_count" "$(prom_query 'count(http_server_request_duration_seconds_count)' | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else "0 (metric absent)")' 2>/dev/null)"
}

cmd_query() {
  require_cluster

  step "Radar L4 query (network flows, request direction only)"
  note "sum by (8 labels) (rate(beyla_network_flow_bytes_total{${JOB_SELECTOR}, direction=\"request\"}[5m]))"
  prom_query "sum by (k8s_src_owner_name, k8s_src_namespace, k8s_src_owner_type, k8s_dst_owner_name, k8s_dst_namespace, k8s_dst_owner_type, dst_port, transport) (rate(beyla_network_flow_bytes_total{${JOB_SELECTOR}, direction=\"request\"}[5m]))" | print_series

  step "Without the direction filter (what the mirrored response edges look like)"
  note "the same query with no direction filter — every edge above gains a twin pointing the other way"
  prom_query "sum by (direction, k8s_src_owner_name, k8s_dst_owner_name, k8s_src_owner_type, k8s_dst_owner_type, dst_port, transport) (rate(beyla_network_flow_bytes_total{${JOB_SELECTOR}, k8s_src_namespace=\"${DEMO_NS}\", k8s_dst_namespace=\"${DEMO_NS}\"}[5m]))" | print_series

  step "Traffic Radar excludes: direction=unknown (UDP, both ends)"
  note "this is the probe behind the \"UDP is not shown\" warning"
  prom_query "sum by (direction, k8s_src_owner_name, k8s_dst_owner_name, dst_port, transport) (rate(beyla_network_flow_bytes_total{${JOB_SELECTOR}, direction=\"unknown\"}[5m]))" | print_series

  step "Radar L7 query (HTTP server duration count, joined on server_port)"
  note "sum by (7 labels incl. server_port) (rate(http_server_request_duration_seconds_count{${JOB_SELECTOR}}[5m]))"
  prom_query "sum by (k8s_namespace_name, k8s_owner_name, k8s_pod_name, server_port, http_request_method, http_route, http_response_status_code) (rate(http_server_request_duration_seconds_count{${JOB_SELECTOR}}[5m]))" | print_series

  step "Mirror-series count for one conversation"
  printf "    %-34s %s\n" "client -> web" "$(prom_query 'count(beyla_network_flow_bytes_total{k8s_src_owner_name="client",k8s_dst_owner_name="web"})' | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else 0)' 2>/dev/null)"
  printf "    %-34s %s\n" "web -> client (response direction)" "$(prom_query 'count(beyla_network_flow_bytes_total{k8s_src_owner_name="web",k8s_dst_owner_name="client"})' | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else 0)' 2>/dev/null)"
}

cmd_default()    { require_cluster; apply_beyla default;    warn "Wait ~6 min for the 5m rate window to refill before querying."; }
cmd_attrs()      { require_cluster; apply_beyla attrs;      warn "Wait ~6 min. Expect a large series count: response-direction series carry ephemeral ports."; }
cmd_no_network() { require_cluster; apply_beyla no-network; warn "beyla_network_flow_bytes_total will disappear once the 5m staleness window passes."; }

cmd_port_forward() {
  require_cluster
  step "Prometheus on http://localhost:${PROM_LOCAL_PORT} (Ctrl-C to stop)"
  kctl -n "${MONITORING_NS}" port-forward svc/prometheus "${PROM_LOCAL_PORT}:9090"
}

cmd_help() {
  sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
}

case "${1:-help}" in
  up)           cmd_up ;;
  down)         cmd_down ;;
  reset)        cmd_down; cmd_up ;;
  status)       cmd_status ;;
  query)        cmd_query ;;
  default)      cmd_default ;;
  attrs)        cmd_attrs ;;
  no-network)   cmd_no_network ;;
  port-forward) cmd_port_forward ;;
  help|-h|--help) cmd_help ;;
  *) fail "Unknown subcommand '$1'. Run '$0 help'." ;;
esac
