package investigation

// Radar's general guidance: when to use Radar, which tool answers which
// question, how to read Radar's signals, and the evidence and untrusted-data
// rules. It lives here, beside the investigation prompts, so every product that
// puts it in front of an agent uses the same bytes. Radar's public MCP
// endpoints (/mcp, /mcp-readonly, stdio) send it as server instructions, in
// the initialize result or, on protocol 2026-07-28, which has no initialize,
// the server/discover result; the private investigation transport sends none.
//
// Clients show server instructions differently: some at session start, some
// only after the model has loaded Radar's tools, and some cut long text (Claude
// Code keeps the first 2,048 characters in older versions and 4,096 in newer
// ones; Codex shows a deferred server only by the first line, cut at 250
// characters). So the first line stands alone within 250 characters, the
// routing comes first and stays inside 2,048 characters, the whole text stays
// under 3,800, and every tool description still stands on its own. The routing
// makes sure an agent knows each tool exists and when to reach for it; the
// tool's own description, which the agent reads before calling it, carries the
// detail.

// GuidanceRouting says when to use Radar and which tool answers which
// question. It names read tools only; GuidanceWriteRouting adds the write tools
// where they are registered.
const GuidanceRouting = `Radar keeps a live model of this Kubernetes cluster: prefer its tools to kubectl for reads; load these together: issues, diagnose, get_changes, get_resource, list_resources, search, get_workload_logs, get_pod_logs, get_events.

WHICH TOOL
- Symptom known, faulty resource not: issues. Ranked live failures, with references to missing objects and unschedulable pods.
- One suspect resource: diagnose, for a Pod, Deployment, StatefulSet, DaemonSet or Rollout; a Service, Ingress or route that traffic doesn't reach; an Argo CD Application or Flux Kustomization or HelmRelease (sync and health).
- What changed: get_changes.
- More logs: get_workload_logs, or get_pod_logs for one container or its previous instance.
- A string in objects (config value, image, status message): search. In logs: the grep argument of get_pod_logs or get_workload_logs.
- One object or a whole kind: get_resource, list_resources.
- RBAC, forbidden, 403: get_subject_permissions.
- CPU, memory, OOMKills: top_resources; history: query_prometheus, when present.
- Request, error, retry or queue metrics: discover_metrics, then query_prometheus.
- The graph around one object (owners, selectors, config references, service routing): get_neighborhood; a namespace: get_topology.
- Helm: list_helm_releases, get_helm_release.
`

// GuidanceWriteRouting routes fixes to the write tools. A proxy in front of a
// full server can hide its write tools (Radar Hub does for read-only
// organizations) while still listing other vendors' write tools, so the line
// conditions each Kubernetes write tool on that tool being listed rather than
// on write tools in general.
const GuidanceWriteRouting = "- Fixes, each only if listed: patch_resource, apply_resource; manage_workload to restart, scale or roll back; manage_rollout, manage_cronjob, manage_node. Fix the diagnosed cause, ideally by restoring what changed and broke; stopgaps only if it can't be fixed now.\n"

// GuidanceSignals says how to read what Radar returns.
const GuidanceSignals = `READING RADAR'S SIGNALS
- An issues row naming a mechanism and an action is the leading hypothesis; start from its incident_parent if any. Confirm it with a targeted read; don't re-derive it. If it points at another resource, follow it. Conclude only when the evidence names the resource, explains the mechanism and accounts for the symptom; keep other findings separate.
- Use changes attached to issues; get_changes when absent or for another window or scope. Reuse logs from diagnose; fetch more only for a specific gap.
- issue_timing started_after_resource_was_healthy: a healthy window preceded the failure; look for what changed. started_at_resource_creation means the resource, or its owner workload, started failing during creation or first reconciliation. Compare resource_created_at with the incident window. Timing alone doesn't prove cause. Prefer timing_summary when present.
- issues is live state; get_cluster_audit is configuration posture.
- After a fix, wait on a condition with a time limit: rollout done, new pods Ready, issue gone from issues. Sleep only for time that must pass (a rate window, a scrape interval). Check the symptom only if they don't cover it.
`

// GeneralGuidanceReadOnly is the general guidance for a surface without write
// tools; GeneralGuidanceFull adds the write-tool routing. Both end with the
// evidence rule and the untrusted-data rule Radar's own investigation agent
// follows.
const (
	GeneralGuidanceReadOnly = GuidanceRouting + "\n" + GuidanceSignals + "\n" + ResultDiscipline + " " + UntrustedClusterData
	GeneralGuidanceFull     = GuidanceRouting + GuidanceWriteRouting + "\n" + GuidanceSignals + "\n" + ResultDiscipline + " " + UntrustedClusterData
)

// GeneralGuidance returns the general guidance for a surface that does or does
// not register Radar's write tools.
func GeneralGuidance(includeWrites bool) string {
	if includeWrites {
		return GeneralGuidanceFull
	}
	return GeneralGuidanceReadOnly
}
