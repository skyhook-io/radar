package mcp

import "github.com/skyhook-io/radar/pkg/investigation"

// Server instructions are sent to every MCP client at initialize. Clients show
// them differently: some at session start, some only after the model has loaded
// Radar's tools, and some cut long text (Claude Code keeps the first 2,048
// characters in older versions and 4,096 in newer ones). So the routing comes
// first and stays inside 2,048 characters, the whole text stays under 3,800,
// and every tool description still stands on its own.

// routingInstructions says when to use Radar and which tool answers which
// question. It names read tools only; writeRoutingLine adds the write tools on
// servers that register them.
const routingInstructions = `Radar keeps a live model of the connected Kubernetes cluster: resource state, what manages, exposes and selects what, recent changes, logs, events and ranked issues. Prefer its tools over kubectl for reading cluster state. If your client loads tools on demand, load the core read tools together: issues, diagnose, get_changes, get_resource, list_resources, search, get_workload_logs, get_pod_logs, get_events.

WHICH TOOL
- Symptom known, faulty resource not: issues. Ranked live failures, including references to missing ConfigMaps, Secrets, PVCs or Service backends, and unschedulable pods.
- diagnose, in one call: a Pod, Deployment, StatefulSet, DaemonSet or Rollout (spec, status, owners, current and previous logs, warning events, startup blockers); a Service, Ingress or route that traffic doesn't reach (per-route reachability); an Argo CD Application or Flux Kustomization or HelmRelease (sync and health).
- What changed: get_changes (creates, updates, deletes, field diffs).
- More logs: get_workload_logs for all pods of a workload, get_pod_logs for one container or its previous instance.
- A string in objects (config value, env reference, image, status message): search. In logs: the grep argument of get_pod_logs or get_workload_logs.
- One object, or every object of a kind: get_resource, list_resources.
- Events only: get_events.
- RBAC errors, forbidden, 403: get_subject_permissions.
- CPU, memory, OOMKills: top_resources; history through query_prometheus when Prometheus is present.
- Service routing: diagnose on the entry; get_neighborhood for the graph around it; get_topology for a namespace.
- Helm: list_helm_releases, get_helm_release.
`

// A proxy in front of a full server can hide its write tools (Radar Hub does
// for read-only organizations), so the line holds only if they are listed.
const writeRoutingLine = "- Fixes, if your tool list includes write tools: patch_resource or apply_resource for spec changes; manage_workload to restart, scale or roll back; manage_rollout, manage_cronjob, manage_node.\n"

// signalInstructions says how to read what Radar returns.
const signalInstructions = `READING RADAR'S SIGNALS
- An issues row that names a mechanism and an action is the leading hypothesis; if it has an incident_parent, start from that root. Confirm it with a targeted read (a log line, a field) instead of re-deriving it. If it points at another resource, follow that dependency. Conclude when the evidence names the resource, explains the mechanism and accounts for the reported impact.
- Read the changes attached to issues first; call get_changes when they are absent or you need another window or scope. Reuse the logs diagnose returned; fetch more only for a specific gap.
- issue_timing started_after_resource_was_healthy means a healthy window preceded the failure: look for what changed. started_at_resource_creation on a recently created resource can mean the creation itself is the fault. Prefer timing_summary when present.
- issues is live state; get_cluster_audit is configuration posture.
- After a fix, wait on a condition with a time limit, never a fixed sleep: the rollout completes, new pods are Ready, the issue clears in issues. Check the reported symptom directly only when those signals do not cover it.
`

// serverInstructions is the text a server registering the given tool set sends
// at initialize. The closing paragraph is the evidence rule and the
// untrusted-data rule Radar's own investigation agent follows.
func serverInstructions(includeWrites bool) string {
	routing := routingInstructions
	if includeWrites {
		routing += writeRoutingLine
	}
	return routing + "\n" + signalInstructions + "\n" + investigation.ResultDiscipline + " " + investigation.UntrustedClusterData
}
