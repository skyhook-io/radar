import type {
  InvestigationCaseItem,
  InvestigationCaseResolution,
} from "./investigationCase";
import {
  investigationEvidenceSubjectRef,
  investigationSourceArgs,
} from "./investigationEvidence/identity";
import {
  canonicalKind,
  runTargetKey,
  type InvestigationTargetIdentity,
} from "./target";

// Reads that are about exactly the resource their arguments name. A broad
// read (a search, a namespace listing) proves nothing about which resource
// mattered, however its rows are cited.
const SINGLE_RESOURCE_TOOLS = new Set([
  "diagnose",
  "get_resource",
  "get_events",
  "get_neighborhood",
  "get_workload_logs",
  "get_pod_logs",
]);

const NAMESPACED_ONLY_TOOLS = new Set([
  "get_events",
  "get_pod_logs",
  "get_workload_logs",
]);

// The resources a question-only assessment came to rest on, causes first.
// Each is named by what Radar produced, never by what the agent wrote: the
// object the citation bound to, or else the resource a single-resource read
// was asked about.
export function focusFromCase(
  investigationCase: InvestigationCaseResolution | undefined,
  max = 3,
): InvestigationTargetIdentity[] {
  if (!investigationCase) return [];
  const items = investigationCase.items
    .filter((item) => item.role === "cause" || item.role === "symptom")
    .sort((a, b) => Number(b.role === "cause") - Number(a.role === "cause"));
  const focus: (InvestigationTargetIdentity & { exact: boolean })[] = [];
  for (const item of items) {
    const bound = boundResource(item);
    const resource = bound ?? readResource(item);
    if (!resource) continue;
    // A read's arguments often leave out a built-in group the bound object
    // carries (deployment vs apps/Deployment): the same resource either way,
    // named by the exact identity whichever arrives first.
    const same = focus.findIndex(
      (seen) =>
        sameResource(seen, resource) &&
        (seen.group === resource.group ||
          (!seen.exact && !seen.group) ||
          (!bound && !resource.group)),
    );
    if (same >= 0) {
      if (bound && !focus[same].exact) focus[same] = { ...bound, exact: true };
      continue;
    }
    if (focus.length >= max) continue;
    focus.push({ ...resource, exact: !!bound });
  }
  return focus.map(({ kind, group, namespace, name }) => ({
    kind,
    group,
    namespace,
    name,
  }));
}

function sameResource(
  a: InvestigationTargetIdentity,
  b: InvestigationTargetIdentity,
): boolean {
  return (
    a.namespace === b.namespace &&
    a.name === b.name &&
    runTargetKey(a.kind, "", "", "") === runTargetKey(b.kind, "", "", "")
  );
}

// What the citation bound to, exactly as Radar recorded it.
function boundResource(
  item: InvestigationCaseItem,
): InvestigationTargetIdentity | undefined {
  const data = item.observation?.data;
  const ref = data ? investigationEvidenceSubjectRef(data) : undefined;
  if (!ref?.kind || !ref.name) return undefined;
  return {
    kind: ref.kind,
    group: ref.group ?? "",
    namespace: ref.namespace ?? "",
    name: ref.name,
  };
}

function readResource(
  item: InvestigationCaseItem,
): InvestigationTargetIdentity | undefined {
  const { source } = item;
  if (!source.confirmedSuccess || !SINGLE_RESOURCE_TOOLS.has(source.tool)) {
    return undefined;
  }
  const args = investigationSourceArgs(source);
  const name = typeof args?.name === "string" ? args.name : "";
  const kind =
    source.tool === "get_pod_logs"
      ? "Pod"
      : typeof args?.kind === "string" && args.kind
        ? args.kind
        : source.tool === "get_workload_logs"
          ? "Deployment"
          : "";
  const namespace = typeof args?.namespace === "string" ? args.namespace : "";
  if (!name || !kind) return undefined;
  // These reads search every namespace when given none, so their arguments
  // alone don't say which resource they were about.
  if (!namespace && NAMESPACED_ONLY_TOOLS.has(source.tool)) return undefined;
  const group = typeof args?.group === "string" ? args.group : "";
  // Agents spell kinds every way kubectl accepts: deployment, deployments,
  // Deployment. The server canonicalizes the group when a run starts.
  return {
    kind: canonicalKind(kind, group),
    group,
    namespace,
    name,
  };
}
