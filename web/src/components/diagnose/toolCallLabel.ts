import { prettyTool } from "./parts";

// One readable sentence for a tool call in flight, from the call's arguments:
// "Reading logs for pod shop/api-7d4 / api" tells the reader what the agent
// is looking at; "Get Pod Logs" tells them which tool ran.
export function describeToolCall(tool: string, args?: string): string {
  const a = parseArgs(args);
  const kind = kindLabel(a.kind);
  const target = a.name
    ? `${a.namespace ? `${a.namespace}/` : ""}${a.name}`
    : undefined;
  const scoped = (what: string) =>
    `${what}${a.namespace ? ` in ${a.namespace}` : ""}`;
  switch (tool) {
    case "diagnose":
      return target
        ? `Diagnosing ${kind ?? "resource"} ${target}`
        : "Diagnosing";
    case "get_resource":
      return target
        ? `Reading ${kind ?? "resource"} ${target}`
        : "Reading a resource";
    case "list_resources":
      return scoped(`Listing ${a.kind ? pluralLabel(a.kind) : "resources"}`);
    case "get_events":
      return target ? `Reading events for ${target}` : scoped("Reading events");
    case "get_pod_logs":
      return `Reading logs for pod ${target ?? ""}${a.container ? ` / ${a.container}` : ""}${a.previous ? " (previous instance)" : ""}`.trim();
    case "get_workload_logs":
      return `Reading logs for ${kind ?? "workload"} ${target ?? ""}`.trim();
    case "get_changes":
      return target
        ? `Reading recent changes for ${target}`
        : scoped("Reading recent changes");
    case "issues":
      return scoped(`Listing ${a.kind ? `${kindLabel(a.kind)} ` : ""}issues`);
    case "search":
      return a.query ? `Searching for “${a.query}”` : "Searching";
    case "query_prometheus":
      return "Querying Prometheus";
    case "discover_metrics":
      return "Discovering metrics";
    case "get_prometheus_rules":
      return "Reading alert rules";
    case "get_helm_release":
      return `Reading Helm release ${target ?? ""}`.trim();
    case "list_helm_releases":
      return scoped("Listing Helm releases");
    case "get_neighborhood":
      return target
        ? `Mapping what ${kind ?? "resource"} ${target} connects to`
        : "Mapping connections";
    case "get_topology":
      return scoped("Mapping the topology");
    case "get_subject_permissions":
      return target
        ? `Checking what ${kind ?? "subject"} ${target} may do`
        : "Checking permissions";
    case "list_namespaces":
      return "Listing namespaces";
    case "list_packages":
      return a.chart
        ? `Looking up packages matching “${a.chart}”`
        : "Looking up packages";
    case "top_resources":
      return "Ranking resources";
    case "get_dashboard":
      return "Reading the cluster dashboard";
    case "get_cluster_audit":
      return "Running the cluster audit";
    case "get_cluster_upgrade_readiness":
      return "Checking upgrade readiness";
    case "apply_resource":
      return "Applying the change";
    case "patch_resource":
      return target
        ? `Patching ${kind ?? "resource"} ${target}`
        : "Patching a resource";
    case "manage_workload":
    case "manage_rollout":
    case "manage_node":
    case "manage_gitops":
    case "manage_cronjob":
      return target
        ? `Changing ${kind ?? "resource"} ${target}`
        : "Changing a resource";
    default:
      return prettyTool(tool);
  }
}

interface ToolArgs {
  kind?: string;
  namespace?: string;
  name?: string;
  container?: string;
  previous?: boolean;
  query?: string;
  chart?: string;
}

function parseArgs(args: string | undefined): ToolArgs {
  if (!args) return {};
  try {
    const raw = JSON.parse(args) as Record<string, unknown>;
    const str = (key: string) =>
      typeof raw[key] === "string" && (raw[key] as string).trim()
        ? (raw[key] as string).trim()
        : undefined;
    return {
      kind: str("kind"),
      namespace: str("namespace"),
      name: str("name"),
      container: str("container"),
      previous: raw.previous === true,
      query: str("query"),
      chart: str("chart"),
    };
  } catch {
    return {};
  }
}

// Arguments carry kinds as "deployment", "Deployment" or "configmaps".
// Capitalise and otherwise leave the word alone: stripping an "s" turns
// Ingress into Ingres.
function kindLabel(kind: string | undefined): string | undefined {
  if (!kind) return undefined;
  return kind.charAt(0).toUpperCase() + kind.slice(1);
}

function pluralLabel(kind: string): string {
  const base = kindLabel(kind) ?? kind;
  return base.endsWith("s") ? base : `${base}s`;
}
