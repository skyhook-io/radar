#!/usr/bin/env bash
# Seed real kagent 1.x reconciliation scenarios into an existing test cluster.
# Read scripts/kagent-demo/README.md before running.
set -euo pipefail

NS=radar-kagent-demo
OTHER_NS=radar-kagent-demo-consumers
LABEL=app.kubernetes.io/part-of
OWNER=radar-kagent-demo
SEL="${LABEL}=${OWNER}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/kagent-demo"
SOURCE_NS="${KAGENT_DEMO_RUNTIME_NAMESPACE:-kagent}"
SOURCE_HARNESS="${KAGENT_DEMO_HARNESS:-kagent-default}"
WAIT_SECONDS="${WAIT_SECONDS:-600}"
CTX=''
MODE=up
step() { printf '==> %s\n' "$1"; }
fail() { printf '    ✗ %s\n' "$1" >&2; exit 1; }
usage() {
  cat <<EOF
Usage: $0 [--context NAME] [--status | --cleanup]

Seeds ${NS} and ${OTHER_NS} on an existing kagent 1.x + Substrate cluster.
Without --context, current-context must start with radar-test-.
--status lists fixtures; --cleanup removes only the owned demo namespaces.
Runtime source: KAGENT_DEMO_RUNTIME_NAMESPACE / KAGENT_DEMO_HARNESS
(default kagent / kagent-default). WAIT_SECONDS defaults to 600 per wait.
EOF
}
while [ $# -gt 0 ]; do
  case "$1" in
    --context) [ $# -ge 2 ] && [ -n "$2" ] || fail '--context needs a name'; CTX="$2"; shift 2 ;;
    --status) MODE=status; shift ;;
    --cleanup) MODE=cleanup; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
for cmd in kubectl jq; do command -v "$cmd" >/dev/null || fail "$cmd not found in PATH"; done
if [ -z "$CTX" ]; then
  CTX="$(kubectl config current-context)"
  case "$CTX" in radar-test-*) ;; *) fail "Refusing context '$CTX'; use a radar-test-* context or explicitly pass --context NAME" ;; esac
fi
k() { kubectl --context "$CTX" --request-timeout=30s "$@"; }
step "Context: $CTX (never changes current-context)"

owned_or_absent() {
  local object
  object="$(k get "$@" --ignore-not-found -o json)" || fail "Cannot check ownership: $*"
  [ -z "$object" ] || jq -e --arg key "$LABEL" --arg owner "$OWNER" '.metadata.labels[$key] == $owner' <<<"$object" >/dev/null ||
    fail "$* exists without $SEL; refusing to touch it"
}
show_status() {
  local ns
  for ns in "$NS" "$OTHER_NS"; do
    k get namespace -l "$SEL" --field-selector "metadata.name=$ns"
    k get agents.api.kagent.dev,modelconfigs.api.kagent.dev,remotemcpservers.api.kagent.dev,harnesses.api.kagent.dev,workerpools.ate.dev,deploy,rs,pod,svc,cm,secret,sa -n "$ns" -l "$SEL"
  done
}
cleanup() {
  local ns resources resource foreign inventory objects
  resources="$(k api-resources --verbs=list,delete --namespaced -o name)"
  for ns in "$NS" "$OTHER_NS"; do
    owned_or_absent namespace "$ns"
    [ -n "$(k get namespace "$ns" --ignore-not-found -o name)" ] || continue
    # Namespace deletion also removes controller children. Refuse foreign objects
    # except Kubernetes' own default SA / root CA and descendants of demo objects.
    inventory='[]'
    while IFS= read -r resource; do
      case "$resource" in events|events.events.k8s.io) continue ;; esac
      objects="$(k get "$resource" -n "$ns" -o json)" || fail "Cannot inventory $resource in $ns; nothing deleted"
      inventory="$(jq -cn --argjson previous "$inventory" --argjson objects "$objects" '$previous + $objects.items')"
    done <<<"$resources"
    foreign="$(jq -r --arg key "$LABEL" --arg owner "$OWNER" '
      . as $objects |
      def owned($o; $seen):
        if $o.metadata.labels[$key] == $owner then true
        elif ($seen | index($o.metadata.uid)) != null then false
        else any($o.metadata.ownerReferences[]?; .uid as $uid |
          any($objects[] | select(.metadata.uid == $uid); owned(.; $seen + [$o.metadata.uid]))) end;
      .[] | select(owned(.; []) | not) |
      select((.kind == "ServiceAccount" and .metadata.name == "default") or
             (.kind == "ConfigMap" and .metadata.name == "kube-root-ca.crt") | not) |
      .kind + "/" + .metadata.name' <<<"$inventory")"
    [ -z "$foreign" ] || fail "Foreign objects in $ns: $foreign; refusing namespace deletion"
  done
  for ns in "$NS" "$OTHER_NS"; do
    k delete namespace -l "$SEL" --field-selector "metadata.name=$ns" --ignore-not-found --timeout="${WAIT_SECONDS}s"
  done
}
if [ "$MODE" = cleanup ]; then cleanup; exit; fi
if [ "$MODE" = status ]; then show_status; exit; fi

step 'Checking kagent and Substrate prerequisites'
discovery="$(k get --raw /apis/api.kagent.dev/v1alpha3)" || fail 'api.kagent.dev/v1alpha3 is not served. Install kagent 1.x with Agent Substrate first; this script never installs or upgrades it.'
for resource in agents modelconfigs remotemcpservers harnesses; do
  jq -e --arg resource "$resource" 'any(.resources[]; .name == $resource)' <<<"$discovery" >/dev/null || fail "Missing api.kagent.dev/v1alpha3/$resource; install kagent 1.x first"
done
k get --raw /apis/ate.dev >/dev/null || fail 'Agent Substrate ate.dev is not served; install Agent Substrate first'
harness="$(k get harnesses.api.kagent.dev "$SOURCE_HARNESS" -n "$SOURCE_NS" -o json)" || fail "Missing Harness $SOURCE_NS/$SOURCE_HARNESS"
jq -e '.spec.kagent != null' <<<"$harness" >/dev/null || fail 'Source Harness must use the kagent runtime'
pool_name="$(jq -r '.spec.substrate.workerPoolRef.name' <<<"$harness")"
pool="$(k get workerpools.ate.dev "$pool_name" -n "$SOURCE_NS" -o json)" || fail "Missing WorkerPool $SOURCE_NS/$pool_name"

apply() {
  local object ns name kind
  object="$(cat)"
  ns="$(jq -r '.metadata.namespace // ""' <<<"$object")"
  name="$(jq -r '.metadata.name' <<<"$object")"
  kind="$(jq -r 'if .apiVersion | contains("/") then .kind + "." + (.apiVersion | split("/")[0]) else .kind end' <<<"$object")"
  if [ -n "$ns" ]; then owned_or_absent "$kind" "$name" -n "$ns"; else owned_or_absent "$kind" "$name"; fi
  jq --arg key "$LABEL" --arg owner "$OWNER" '.metadata.labels[$key] = $owner' <<<"$object" | k apply -f -
}
for ns in "$NS" "$OTHER_NS"; do
  jq -n --arg ns "$ns" '{apiVersion:"v1",kind:"Namespace",metadata:{name:$ns}}' | apply
  jq -n --arg ns "$ns" '{apiVersion:"v1",kind:"ServiceAccount",metadata:{name:"demo-worker",namespace:$ns}}' | apply
  jq --arg ns "$ns" --arg key "$LABEL" --arg owner "$OWNER" '
    {apiVersion,kind,metadata:{name:"demo-worker",namespace:$ns,labels:{"kagent.dev/worker-pool-name":"demo-worker","kagent.dev/worker-pool-namespace":$ns}},spec} |
    .spec.replicas=1 | .spec.template.serviceAccountName="demo-worker"' <<<"$pool" | apply
  jq --arg ns "$ns" '{apiVersion,kind,metadata:{name:"demo-harness",namespace:$ns},spec} |
    .spec.substrate.workerPoolRef.name="demo-worker"' <<<"$harness" | apply
done
k create configmap model-stub -n "$NS" --from-file="stub.py=$DIR/stub.py" --dry-run=client -o json | apply
jq -n --arg ns "$NS" --arg key "$LABEL" --arg owner "$OWNER" '{
  apiVersion:"apps/v1",kind:"Deployment",metadata:{name:"model-stub",namespace:$ns},spec:{replicas:1,
  selector:{matchLabels:{app:"radar-kagent-model-stub"}},template:{metadata:{labels:{app:"radar-kagent-model-stub",($key):$owner}},spec:{
  automountServiceAccountToken:false,containers:[{name:"stub",image:"python:3.12-slim",command:["python3","-u","/app/stub.py"],
  ports:[{containerPort:8080}],readinessProbe:{httpGet:{path:"/v1/models",port:8080}},
  volumeMounts:[{name:"app",mountPath:"/app",readOnly:true}],resources:{requests:{cpu:"10m",memory:"32Mi"},limits:{memory:"128Mi"}}}],
  volumes:[{name:"app",configMap:{name:"model-stub"}}]}}}}' | apply
jq -n --arg ns "$NS" '{apiVersion:"v1",kind:"Service",metadata:{name:"model-stub",namespace:$ns},spec:{selector:{app:"radar-kagent-model-stub"},ports:[{port:8080,targetPort:8080}]}}' | apply
k rollout status deployment/model-stub -n "$NS" --timeout="${WAIT_SECONDS}s"
for secret in stub-key wrong-key; do
  key=OPENAI_API_KEY; [ "$secret" != wrong-key ] || key=OTHER_KEY
  k create secret generic "$secret" -n "$NS" --from-literal="$key=fake-demo-value" --dry-run=client -o json | apply
done
model() {
  local name="$1" secret="$2" sharing="$3"
  jq -n --arg ns "$NS" --arg name "$name" --arg secret "$secret" --arg sharing "$sharing" '{
    apiVersion:"api.kagent.dev/v1alpha3",kind:"ModelConfig",metadata:{name:$name,namespace:$ns},spec:{
    provider:"OpenAI",model:"stub-model",apiKeySecret:$secret,apiKeySecretKey:"OPENAI_API_KEY",stream:true,
    allowedNamespaces:{from:$sharing},openAI:{baseUrl:("http://model-stub."+$ns+".svc.cluster.local:8080/v1")}}}' | apply
}
model stub-model stub-key Same
model shared-model stub-key All
model restricted-model stub-key Same
model missing-secret absent-key Same
model missing-key wrong-key Same
model staged-model staged-key Same
for server in unreachable tools; do
  url="http://model-stub.${NS}.svc.cluster.local:8080/mcp"
  [ "$server" != unreachable ] || url="http://model-stub.${NS}.svc.cluster.local:9999/mcp"
  jq -n --arg ns "$NS" --arg name "$server" --arg url "$url" '{apiVersion:"api.kagent.dev/v1alpha3",kind:"RemoteMCPServer",
    metadata:{name:$name,namespace:$ns},spec:{description:"Radar demo MCP server",url:$url,protocol:"STREAMABLE_HTTP",timeout:"5s"}}' | apply
done
agent() {
  local name="$1" ns="$2" model="$3" model_ns="$4" server="${5:-}" tool="${6:-}"
  jq -n --arg name "$name" --arg ns "$ns" --arg model "$model" --arg model_ns "$model_ns" --arg server "$server" --arg tool "$tool" '{
    apiVersion:"api.kagent.dev/v1alpha3",kind:"Agent",metadata:{name:$name,namespace:$ns},spec:{harnessRef:{name:"demo-harness"},
    template:{inline:{description:"Radar workspace fixture",systemPrompt:"Reply briefly. This is a test.",modelConfig:{name:$model,namespace:$model_ns}}}}} |
    if $server != "" then .spec.template.inline.tools=[{mcp:{server:{kind:"RemoteMCPServer",name:$server},tools:[$tool]}}] else . end' | apply
}
agent missing-model "$NS" absent-model "$NS"
agent missing-secret "$NS" missing-secret "$NS"
agent missing-key "$NS" missing-key "$NS"
agent unreachable-mcp "$NS" stub-model "$NS" unreachable demo_echo
agent unoffered-tool "$NS" stub-model "$NS" tools demo_not_offered
agent blocked-edit "$NS" stub-model "$NS"
agent cross-namespace "$OTHER_NS" shared-model "$NS"
agent sharing-refused "$OTHER_NS" restricted-model "$NS"

step 'Waiting for real prepared revisions before breaking the working edit'
for coordinate in "$NS/blocked-edit" "$NS/unreachable-mcp" "$NS/unoffered-tool" "$OTHER_NS/cross-namespace"; do
  ns="${coordinate%/*}"; name="${coordinate#*/}"
  k wait agents.api.kagent.dev/"$name" -n "$ns" --for=condition=Ready --timeout="${WAIT_SECONDS}s" || fail "Agent $coordinate did not prepare; inspect --status (no status is synthesized)"
done
revision="$(k get agents.api.kagent.dev blocked-edit -n "$NS" -o jsonpath='{.status.latestSuccessfulRevision}')"
[ -n "$revision" ] || fail 'Working Agent has no successful revision'
agent blocked-edit "$NS" absent-model "$NS"
k wait agents.api.kagent.dev/blocked-edit -n "$NS" --for=condition=ResolvedRefs=False --timeout="${WAIT_SECONDS}s"
[ "$(k get agents.api.kagent.dev blocked-edit -n "$NS" -o jsonpath='{.status.latestSuccessfulRevision}')" = "$revision" ] || fail 'Blocked edit lost its previous successful revision'
step "Fixtures applied; blocked-edit previously prepared revision $revision. Open /kagent with both demo namespaces selected."
show_status
