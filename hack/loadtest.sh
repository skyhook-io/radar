#!/usr/bin/env bash
#
# loadtest.sh — Generate semi-realistic K8s resources at scale for testing Radar.
#
# Usage:
#   hack/loadtest.sh up [small|medium|large]   # Create resources (default: medium)
#   hack/loadtest.sh down                      # Tear down all loadtest resources
#   hack/loadtest.sh status                    # Show current loadtest resource counts
#   hack/loadtest.sh nodes                     # Create Karpenter NodePool for loadtest
#   hack/loadtest.sh nodes-down                # Remove Karpenter NodePool
#
# Scale profiles (same per-namespace density, different namespace count):
#   small:   16 namespaces,    ~720 pods,    ~480 svc+ing  (quick smoke test)
#   medium:  80 namespaces,  ~3,600 pods,  ~2,400 svc+ing  (1/5th of reported)
#   large:  400 namespaces, ~18,000 pods, ~12,000 svc+ing  (full reported scale)
#
# All pods use registry.k8s.io/pause:3.9 (~1MB RAM, near-zero CPU).
# Estimated spot cost: small ~$0.11/hr, medium ~$0.50/hr, large ~$2.50/hr.
# Teardown deletes all namespaces with prefix "radar-lt-".

set -euo pipefail

LABEL="radar-loadtest"
LABEL_SELECTOR="${LABEL}=true"
NS_PREFIX="${NS_PREFIX:-radar-lt}"
PAUSE_IMAGE="registry.k8s.io/pause:3.9"
MAX_PARALLEL=10
NODEPOOL_NAME="loadtest"
NODECLASS_NAME="loadtest"  # dedicated EC2NodeClass with maxPods=110
KARPENTER_NODE_ROLE="${KARPENTER_NODE_ROLE:?set KARPENTER_NODE_ROLE to your cluster's Karpenter node IAM role}"
CLUSTER_TAG="${CLUSTER_TAG:-us-east-1-nonprod}"  # value for karpenter.sh/discovery tag

# ── Naming pools ──────────────────────────────────────────────────────────────

TEAMS=(platform payments checkout inventory analytics auth notifications shipping)

APP_SUFFIXES=(api worker gateway processor cache scheduler consumer indexer webhook dispatcher)

# 9 deployment names per namespace
DEPLOY_NAMES=(api-server worker cache-layer consumer gateway proxy dispatcher listener handler)

# 9 replica counts, sum = 45 pods per namespace
REPLICAS=(5 7 4 9 3 6 5 3 3)

# Per-namespace constants
DEPLOYS_PER_NS=9       # deployments (and matching ClusterIP services + configmaps)
HEADLESS_EXTRAS=12     # additional headless services per namespace
INGRESSES_PER_NS=9     # ingresses per namespace
HPAS_PER_NS=2          # HPAs per namespace
SECRETS_PER_NS=2       # secrets per namespace
# Totals per namespace: 21 services + 9 ingresses = 30 svc+ing, 45 pods

# ── Profile → namespace count ────────────────────────────────────────────────

get_ns_params() {
  case "${1:-medium}" in
    small)  echo "4 4 1" ;;   # 4 teams x 4 apps x 1 = 16 namespaces
    medium) echo "8 10 1" ;;  # 8 teams x 10 apps x 1 = 80 namespaces
    large)  echo "8 10 5" ;;  # 8 teams x 10 apps x 5 = 400 namespaces
    *)
      echo "Error: unknown profile '$1'. Use small, medium, or large." >&2
      exit 1
      ;;
  esac
}

# ── YAML generators ──────────────────────────────────────────────────────────

gen_namespace() {
  local name=$1 team=$2
  cat <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${name}
  labels:
    ${LABEL}: "true"
    team: ${team}
---
EOF
}

gen_deployment() {
  local ns=$1 name=$2 replicas=$3
  cat <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app: ${name}
    ${LABEL}: "true"
spec:
  replicas: ${replicas}
  selector:
    matchLabels:
      app: ${name}
  template:
    metadata:
      labels:
        app: ${name}
        ${LABEL}: "true"
    spec:
      terminationGracePeriodSeconds: 0
      nodeSelector:
        ${LABEL}: "true"
      tolerations:
      - key: ${LABEL}
        operator: Equal
        value: "true"
        effect: NoSchedule
      containers:
      - name: pause
        image: ${PAUSE_IMAGE}
        resources:
          requests:
            cpu: 1m
            memory: 2Mi
          limits:
            memory: 4Mi
---
EOF
}

gen_service() {
  local ns=$1 name=$2 port=$3
  cat <<EOF
apiVersion: v1
kind: Service
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app: ${name}
    ${LABEL}: "true"
spec:
  selector:
    app: ${name}
  ports:
  - port: ${port}
    targetPort: ${port}
    protocol: TCP
---
EOF
}

gen_headless_service() {
  local ns=$1 name=$2 port=$3
  cat <<EOF
apiVersion: v1
kind: Service
metadata:
  name: ${name}-hl
  namespace: ${ns}
  labels:
    app: ${name}
    ${LABEL}: "true"
spec:
  clusterIP: None
  selector:
    app: ${name}
  ports:
  - port: ${port}
    targetPort: ${port}
    protocol: TCP
---
EOF
}

gen_ingress() {
  local ns=$1 svc_name=$2 host=$3
  cat <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ${svc_name}-ing
  namespace: ${ns}
  labels:
    ${LABEL}: "true"
spec:
  rules:
  - host: ${host}
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: ${svc_name}
            port:
              number: 8080
---
EOF
}

gen_configmap() {
  local ns=$1 name=$2
  cat <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${name}-config
  namespace: ${ns}
  labels:
    app: ${name}
    ${LABEL}: "true"
data:
  LOG_LEVEL: "info"
  ENV: "loadtest"
  SERVICE_NAME: "${name}"
---
EOF
}

gen_secret() {
  local ns=$1 name=$2
  cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${name}-secret
  namespace: ${ns}
  labels:
    app: ${name}
    ${LABEL}: "true"
type: Opaque
data:
  api-key: bG9hZHRlc3Qta2V5
  db-password: bG9hZHRlc3QtcGFzcw==
---
EOF
}

gen_hpa() {
  local ns=$1 deploy_name=$2
  cat <<EOF
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: ${deploy_name}-hpa
  namespace: ${ns}
  labels:
    ${LABEL}: "true"
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: ${deploy_name}
  minReplicas: 1
  maxReplicas: 20
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 80
---
EOF
}

# ── Generate YAML for one namespace ──────────────────────────────────────────

gen_ns_yaml() {
  local ns_name=$1 team=$2

  gen_namespace "$ns_name" "$team"

  # Deployments + ClusterIP services + ConfigMaps
  for ((d=0; d<DEPLOYS_PER_NS; d++)); do
    local dname=${DEPLOY_NAMES[d]}
    local reps=${REPLICAS[d]}
    local port=$((8080 + d))

    gen_deployment "$ns_name" "$dname" "$reps"
    gen_service "$ns_name" "$dname" "$port"
    gen_configmap "$ns_name" "$dname"
  done

  # HPAs (first N deployments)
  for ((h=0; h<HPAS_PER_NS; h++)); do
    gen_hpa "$ns_name" "${DEPLOY_NAMES[h]}"
  done

  # Extra headless services (cycle through deployment names)
  for ((e=0; e<HEADLESS_EXTRAS; e++)); do
    local base_name=${DEPLOY_NAMES[e % DEPLOYS_PER_NS]}
    local suffix=$((e / DEPLOYS_PER_NS + 1))
    if ((suffix == 1)); then
      gen_headless_service "$ns_name" "$base_name" $((9090 + e))
    else
      gen_headless_service "$ns_name" "${base_name}-${suffix}" $((9090 + e))
    fi
  done

  # Ingresses
  for ((i=0; i<INGRESSES_PER_NS; i++)); do
    local ing_svc=${DEPLOY_NAMES[i]}
    gen_ingress "$ns_name" "$ing_svc" "${ing_svc}.${ns_name}.example.com"
  done

  # Secrets
  gen_secret "$ns_name" "${team}-app"
  gen_secret "$ns_name" "${team}-db"
}

# ── Karpenter NodePool ────────────────────────────────────────────────────────

cmd_nodes() {
  echo "==> Creating EC2NodeClass '${NODECLASS_NAME}' (maxPods=110 via userData)"

  # EC2NodeClass with AL2023 + maxPods override + prefix delegation for Cilium ENI mode
  # ipPrefixCount tells Karpenter to calculate pod capacity using /28 prefixes (16 IPs each)
  # kubelet.maxPods sets the kubelet's max-pods (EKS default is ENI-based ~17 for t3.medium)
  kubectl apply --server-side -f - <<EOF
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata:
  name: ${NODECLASS_NAME}
spec:
  amiSelectorTerms:
  - alias: al2023@latest
  role: ${KARPENTER_NODE_ROLE}
  subnetSelectorTerms:
  - tags:
      karpenter.sh/discovery: ${CLUSTER_TAG}
  securityGroupSelectorTerms:
  - tags:
      karpenter.sh/discovery: ${CLUSTER_TAG}
  blockDeviceMappings:
  - deviceName: /dev/xvda
    ebs:
      encrypted: true
      volumeSize: 20Gi
      volumeType: gp3
  kubelet:
    maxPods: 110
  ipPrefixCount: 5
EOF

  echo "==> Creating NodePool '${NODEPOOL_NAME}' (spot-only t3.medium, tainted)"

  kubectl apply --server-side -f - <<EOF
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: ${NODEPOOL_NAME}
spec:
  weight: 100
  template:
    metadata:
      labels:
        ${LABEL}: "true"
    spec:
      taints:
      - key: ${LABEL}
        value: "true"
        effect: NoSchedule
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: ${NODECLASS_NAME}
      requirements:
      - key: karpenter.sh/capacity-type
        operator: In
        values:
        - spot
      - key: node.kubernetes.io/instance-type
        operator: In
        values:
        - t3.medium
      - key: topology.kubernetes.io/zone
        operator: In
        values:
        - us-east-1a
        - us-east-1b
        - us-east-1c
      - key: kubernetes.io/arch
        operator: In
        values:
        - amd64
      expireAfter: 720h
  limits:
    cpu: "400"
    memory: 800Gi
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
    consolidateAfter: 30s
    budgets:
    - nodes: "20%"
EOF

  echo ""
  echo "    EC2NodeClass + NodePool created."
  echo "    Pods with toleration '${LABEL}=true:NoSchedule'"
  echo "    will be scheduled on spot t3.medium nodes (~\$0.015/hr each)."
  echo "    kubelet maxPods=110 (Cilium prefix delegation: ~110 IPs/node)."
  echo ""
  echo "    Limits: 400 CPU / 800 GiB (supports up to ~200 nodes)"
  echo "    Estimated cost: ~\$0.015/hr per node (110 pods each)"
}

cmd_nodes_down() {
  echo "==> Removing Karpenter NodePool '${NODEPOOL_NAME}'..."
  if kubectl get nodepool "${NODEPOOL_NAME}" > /dev/null 2>&1; then
    kubectl delete nodepool "${NODEPOOL_NAME}"
    echo "    NodePool deleted. Karpenter will drain and terminate loadtest nodes."
    echo "    This may take a few minutes."
  else
    echo "    NodePool '${NODEPOOL_NAME}' not found."
  fi
  echo "==> Removing EC2NodeClass '${NODECLASS_NAME}'..."
  if kubectl get ec2nodeclass "${NODECLASS_NAME}" > /dev/null 2>&1; then
    kubectl delete ec2nodeclass "${NODECLASS_NAME}"
    echo "    EC2NodeClass deleted."
  else
    echo "    EC2NodeClass '${NODECLASS_NAME}' not found."
  fi
}

# ── Commands ──────────────────────────────────────────────────────────────────

cmd_up() {
  local profile=${1:-medium}
  read -r teams_count app_count repeat <<< "$(get_ns_params "$profile")"

  local base_ns=$((teams_count * app_count))
  local total_ns=$((base_ns * repeat))
  local total_pods=$((45 * total_ns))
  local total_svc=$((21 * total_ns))
  local total_ing=$((9 * total_ns))
  local mem_gib=$(( total_pods * 2 / 1024 ))
  local nodes_needed=$(( (total_pods + 109) / 110 ))  # ceil division

  echo "==> Profile: ${profile}"
  echo "    Namespaces:   ${total_ns} (${teams_count} teams x ${app_count} apps x ${repeat} repeat)"
  echo "    Per namespace: 9 deploys, 45 pods, 21 svc, 9 ing, 9 cm, 2 secrets, 2 hpa"
  echo ""
  echo "    Est. pods:      ~${total_pods}"
  echo "    Est. services:  ~${total_svc}"
  echo "    Est. ingresses: ~${total_ing}"
  echo "    Est. nodes:     ~${nodes_needed} (t3.medium spot @ ~\$0.015/hr each)"
  echo "    Est. cost:      ~\$$(echo "scale=2; ${nodes_needed} * 0.015" | bc)/hr"
  echo "    Est. memory:    ~${mem_gib} GiB (${total_pods} pods x 2Mi)"
  echo ""

  # Check NodePool exists
  if ! kubectl get nodepool "${NODEPOOL_NAME}" > /dev/null 2>&1; then
    echo "    WARNING: NodePool '${NODEPOOL_NAME}' not found."
    echo "    Pods have a taint toleration and may stay Pending without it."
    echo "    Run 'hack/loadtest.sh nodes' first to create the NodePool."
    echo ""
    read -rp "    Continue anyway? [y/N] " confirm
    if [[ "${confirm}" != "y" && "${confirm}" != "Y" ]]; then
      echo "    Aborted."
      exit 1
    fi
  fi

  local tmpdir
  tmpdir=$(mktemp -d)
  trap "rm -rf ${tmpdir}" EXIT

  local ns_idx=0
  local running=0

  for ((r=1; r<=repeat; r++)); do
    for ((t=0; t<teams_count; t++)); do
      local team=${TEAMS[t]}
      for ((a=0; a<app_count; a++)); do
        local app_suffix=${APP_SUFFIXES[a]}
        local ns_name
        if ((repeat == 1)); then
          ns_name="${NS_PREFIX}-${team}-${app_suffix}"
        else
          ns_name="${NS_PREFIX}-${team}-${app_suffix}-${r}"
        fi

        local yaml_file="${tmpdir}/${ns_name}.yaml"
        gen_ns_yaml "$ns_name" "$team" > "$yaml_file"

        # Apply in background
        (
          if kubectl apply --server-side --force-conflicts -f "$yaml_file" > /dev/null 2>&1; then
            echo "    [$(( ns_idx + 1 ))/${total_ns}] ${ns_name}"
          else
            echo "    [$(( ns_idx + 1 ))/${total_ns}] ${ns_name} (FAILED)"
          fi
        ) &
        running=$((running + 1))

        if ((running >= MAX_PARALLEL)); then
          wait -n 2>/dev/null || true
          running=$((running - 1))
        fi

        ns_idx=$((ns_idx + 1))
      done
    done
  done

  wait
  echo ""
  echo "==> Done. Created ${total_ns} namespaces."
  echo "    Karpenter will provision ~${nodes_needed} spot nodes as pods become Pending."
  echo "    Run 'hack/loadtest.sh status' to verify counts."
  echo "    Run 'hack/loadtest.sh down' to tear down."
}

cmd_down() {
  echo "==> Deleting all namespaces with prefix ${NS_PREFIX}-..."
  local namespaces
  namespaces=$(kubectl get ns -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
    | grep "^${NS_PREFIX}-" || true)

  if [[ -z "$namespaces" ]]; then
    echo "    No loadtest namespaces found."
    return
  fi

  local count
  count=$(echo "$namespaces" | wc -l | tr -d ' ')
  echo "    Found ${count} namespaces to delete."

  local running=0
  while IFS= read -r ns; do
    kubectl delete ns "$ns" --wait=false > /dev/null 2>&1 &
    running=$((running + 1))
    if ((running >= MAX_PARALLEL)); then
      wait -n 2>/dev/null || true
      running=$((running - 1))
    fi
  done <<< "$namespaces"
  wait

  echo "    Namespace deletion initiated (finalizers may take a moment)."
  echo "    Once pods are gone, Karpenter will consolidate/terminate empty nodes."
  echo "    Run 'hack/loadtest.sh status' to check progress."
}

cmd_status() {
  echo "==> Loadtest resource counts (namespaces matching ${NS_PREFIX}-*)"
  echo ""

  # Single cluster-wide query per resource type (fast even at scale)
  local ns_count
  ns_count=$(kubectl get ns -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
    | grep -c "^${NS_PREFIX}-" || echo 0)
  echo "    Namespaces:   ${ns_count}"

  if ((ns_count == 0)); then
    echo "    (no loadtest resources found)"
    echo ""
    # Still show node info
    cmd_status_nodes
    return
  fi

  # Use label selector for cluster-wide counts (single API call each)
  local deploy_count pod_count svc_count ing_count cm_count secret_count hpa_count
  deploy_count=$(kubectl get deploy -A -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  pod_count=$(kubectl get pods -A -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  svc_count=$(kubectl get svc -A -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  ing_count=$(kubectl get ing -A -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  cm_count=$(kubectl get cm -A -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  secret_count=$(kubectl get secret -A -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  hpa_count=$(kubectl get hpa -A -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')

  echo "    Deployments:  ${deploy_count}"
  echo "    Pods:         ${pod_count}"
  echo "    Services:     ${svc_count}"
  echo "    Ingresses:    ${ing_count}"
  echo "    ConfigMaps:   ${cm_count}"
  echo "    Secrets:      ${secret_count}"
  echo "    HPAs:         ${hpa_count}"
  echo ""
  echo "    Svc+Ing:      $((svc_count + ing_count))"
  echo ""

  # Pod phase breakdown
  local pending running_pods
  pending=$(kubectl get pods -A -l "${LABEL_SELECTOR}" --field-selector=status.phase=Pending --no-headers 2>/dev/null | wc -l | tr -d ' ')
  running_pods=$(kubectl get pods -A -l "${LABEL_SELECTOR}" --field-selector=status.phase=Running --no-headers 2>/dev/null | wc -l | tr -d ' ')
  echo "    Pod status:   ${running_pods} Running, ${pending} Pending"

  echo ""
  cmd_status_nodes
}

cmd_status_nodes() {
  # Show loadtest node info
  local lt_nodes
  lt_nodes=$(kubectl get nodes -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  echo "    Loadtest nodes: ${lt_nodes}"

  if ((lt_nodes > 0)); then
    local ready not_ready
    ready=$(kubectl get nodes -l "${LABEL_SELECTOR}" --no-headers 2>/dev/null | grep -c " Ready " || echo 0)
    not_ready=$((lt_nodes - ready))
    echo "    Node status:    ${ready} Ready, ${not_ready} NotReady/Provisioning"
    local est_cost
    est_cost=$(echo "scale=2; ${lt_nodes} * 0.015" | bc 2>/dev/null || echo "?")
    echo "    Est. spot cost: ~\$${est_cost}/hr"
  fi

  # Show NodePool status
  if kubectl get nodepool "${NODEPOOL_NAME}" > /dev/null 2>&1; then
    echo "    NodePool '${NODEPOOL_NAME}': exists"
  else
    echo "    NodePool '${NODEPOOL_NAME}': not found (run 'hack/loadtest.sh nodes' to create)"
  fi
}

# ── Main ──────────────────────────────────────────────────────────────────────

case "${1:-}" in
  up)
    cmd_up "${2:-medium}"
    ;;
  down)
    cmd_down
    ;;
  status)
    cmd_status
    ;;
  nodes)
    cmd_nodes
    ;;
  nodes-down)
    cmd_nodes_down
    ;;
  *)
    echo "Usage: hack/loadtest.sh {up|down|status|nodes|nodes-down}"
    echo ""
    echo "Commands:"
    echo "  nodes       Create Karpenter NodePool (spot t3.medium, ~\$0.015/hr each)"
    echo "  up [SIZE]   Create loadtest resources (small, medium, large)"
    echo "  status      Show resource counts and node status"
    echo "  down        Delete all loadtest namespaces"
    echo "  nodes-down  Remove Karpenter NodePool (drains loadtest nodes)"
    echo ""
    echo "Profiles (same density, different namespace count):"
    echo "  small    16 ns,    ~720 pods,   ~480 svc+ing  ~\$0.11/hr"
    echo "  medium   80 ns,  ~3600 pods,  ~2400 svc+ing  ~\$0.50/hr"
    echo "  large   400 ns, ~18000 pods, ~12000 svc+ing  ~\$2.50/hr"
    echo ""
    echo "Workflow:"
    echo "  1. hack/loadtest.sh nodes        # create NodePool"
    echo "  2. hack/loadtest.sh up medium    # create resources"
    echo "  3. hack/loadtest.sh status       # monitor"
    echo "  4. hack/loadtest.sh down         # delete resources"
    echo "  5. hack/loadtest.sh nodes-down   # remove NodePool"
    exit 1
    ;;
esac
