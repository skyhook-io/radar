export type WorkflowExecutionTone =
  "success" | "danger" | "warning" | "info" | "muted";

export interface WorkflowExecutionNode {
  id: string;
  name: string;
  displayName: string;
  displayLabel: string;
  displayType: string;
  type: string;
  phase: string;
  templateName?: string;
  templateScope?: string;
  boundaryId?: string;
  podName?: string;
  templateRef?: WorkflowTemplateReference;
  message?: string;
  startedAt?: string;
  finishedAt?: string;
  parentIds: string[];
  childIds: string[];
  hierarchyChildIds: string[];
}

export interface WorkflowExecutionEdge {
  source: string;
  target: string;
}

export interface WorkflowExecutionCounts {
  podTotal: number;
  podSucceeded: number;
  podFailed: number;
  podRunning: number;
  podPending: number;
  nodeTotal: number;
  nodeSucceeded: number;
  nodeFailed: number;
  nodeRunning: number;
  nodeSkipped: number;
}

export interface WorkflowExecutionPath {
  terminal: WorkflowExecutionNode;
  nodes: WorkflowExecutionNode[];
  tone: WorkflowExecutionTone;
}

export interface WorkflowExecutionActivity {
  id: string;
  at: string;
  label: string;
  detail?: string;
  tone: WorkflowExecutionTone;
  nodeId?: string;
}

export interface WorkflowTemplateReference {
  name: string;
  kind: "WorkflowTemplate" | "ClusterWorkflowTemplate";
  resourceKind: "workflowtemplates" | "clusterworkflowtemplates";
  namespace: string;
  clusterScope: boolean;
  source: "workflow" | "task";
  template?: string;
  taskName?: string;
}

export interface WorkflowExecutionModel {
  nodes: WorkflowExecutionNode[];
  edges: WorkflowExecutionEdge[];
  roots: WorkflowExecutionNode[];
  executionNodes: WorkflowExecutionNode[];
  executionRoots: WorkflowExecutionNode[];
  visibleSteps: WorkflowExecutionNode[];
  focusPaths: WorkflowExecutionPath[];
  activity: WorkflowExecutionActivity[];
  counts: WorkflowExecutionCounts;
  templateRefs: WorkflowTemplateReference[];
  resourcesDuration?: Record<string, number>;
  isLarge: boolean;
}

export interface WorkflowExecutionRow {
  node: WorkflowExecutionNode;
  depth: number;
}

const LEAF_NODE_TYPES = new Set(["Pod", "Suspend", "Skipped"]);
const ACTIVITY_NODE_TYPES = new Set([
  "Pod",
  "HTTP",
  "Plugin",
  "Suspend",
  "Skipped",
]);
const LARGE_WORKFLOW_NODE_COUNT = 80;

export function buildWorkflowExecutionModel(
  workflow: any,
): WorkflowExecutionModel {
  const rawNodes = asRecord(workflow?.status?.nodes);
  const nodeById = new Map<string, WorkflowExecutionNode>();

  for (const [id, raw] of Object.entries(rawNodes)) {
    const node = asRecord(raw);
    const displayName = asString(node.displayName) || asString(node.name) || id;
    const templateRef = templateReferenceFromObject(
      node.templateRef,
      asString(workflow?.metadata?.namespace),
      "task",
      asString(node.templateName),
      displayName,
    );
    nodeById.set(id, {
      id,
      name: asString(node.name) || displayName,
      displayName,
      displayLabel: displayName,
      displayType: workflowNodeTypeLabel(asString(node.type) || "Unknown"),
      type: asString(node.type) || "Unknown",
      phase:
        asString(node.phase) ||
        (asString(node.type) === "Skipped" ? "Skipped" : "Pending"),
      templateName: asString(node.templateName),
      templateScope: asString(node.templateScope),
      boundaryId: asString(node.boundaryID),
      podName: asString(node.podName),
      templateRef: templateRef ?? undefined,
      message: asString(node.message),
      startedAt: asString(node.startedAt),
      finishedAt: asString(node.finishedAt),
      parentIds: [],
      childIds: [],
      hierarchyChildIds: [],
    });
  }

  const edgeKeys = new Set<string>();
  for (const [id, raw] of Object.entries(rawNodes)) {
    const node = nodeById.get(id);
    if (!node) continue;
    const rawNode = asRecord(raw);
    const children = asStringArray(rawNode.children);
    node.hierarchyChildIds = (
      children.length > 0 ? children : asStringArray(rawNode.outboundNodes)
    ).filter((childID) => nodeById.has(childID));
    const childIds = [...children, ...asStringArray(rawNode.outboundNodes)];
    for (const childId of childIds) {
      if (!nodeById.has(childId)) continue;
      addWorkflowEdge(nodeById, edgeKeys, id, childId);
    }
  }

  for (const node of nodeById.values()) {
    if (
      node.parentIds.length > 0 ||
      !node.boundaryId ||
      node.boundaryId === node.id ||
      !nodeById.has(node.boundaryId)
    )
      continue;
    addWorkflowEdge(nodeById, edgeKeys, node.boundaryId, node.id);
    const boundary = nodeById.get(node.boundaryId);
    if (boundary && !boundary.hierarchyChildIds.includes(node.id))
      boundary.hierarchyChildIds = [...boundary.hierarchyChildIds, node.id];
  }

  const nodes = [...nodeById.values()].sort(compareExecutionNodes);
  const roots = nodes.filter((node) => node.parentIds.length === 0);
  const { nodes: executionNodes, roots: executionRoots } =
    buildPresentationGraph(nodes, asString(workflow?.metadata?.name));
  const visibleSteps = executionNodes;
  const counts = countWorkflowExecution(executionNodes);
  const focusNodes = pickFocusNodes(executionNodes);
  const executionNodeById = new Map(
    executionNodes.map((node) => [node.id, node]),
  );

  return {
    nodes,
    edges: [...edgeKeys].map((key) => {
      const [source, target] = key.split("\u0000");
      return { source, target };
    }),
    roots,
    executionNodes,
    executionRoots,
    visibleSteps,
    focusPaths: focusNodes.map((node) => ({
      terminal: node,
      nodes: lineagePath(executionNodeById, node),
      tone: phaseTone(node.phase),
    })),
    activity: workflowActivity(workflow, executionNodes),
    counts,
    templateRefs: collectWorkflowTemplateRefs(workflow),
    resourcesDuration: asNumberRecord(workflow?.status?.resourcesDuration),
    isLarge: executionNodes.length > LARGE_WORKFLOW_NODE_COUNT,
  };
}

export function flattenWorkflowExecution(
  model: WorkflowExecutionModel,
): WorkflowExecutionRow[] {
  const byID = new Map(model.executionNodes.map((node) => [node.id, node]));
  const seen = new Set<string>();
  const rows: WorkflowExecutionRow[] = [];
  const visit = (node: WorkflowExecutionNode, depth: number) => {
    if (seen.has(node.id)) return;
    seen.add(node.id);
    rows.push({ node, depth });
    for (const childID of node.childIds) {
      const child = byID.get(childID);
      if (child) visit(child, depth + 1);
    }
  };
  for (const root of model.executionRoots) visit(root, 0);
  for (const node of model.executionNodes) visit(node, 0);
  return rows;
}

function buildPresentationGraph(
  rawNodes: WorkflowExecutionNode[],
  workflowName: string,
): { nodes: WorkflowExecutionNode[]; roots: WorkflowExecutionNode[] } {
  const rawByID = new Map(rawNodes.map((node) => [node.id, node]));
  const elided = new Set<string>();
  for (const node of rawNodes) {
    if (node.type !== "StepGroup") continue;
    if (
      node.childIds.length === 1 ||
      (node.childIds.length === 0 && !isWorkflowProblemPhase(node.phase))
    )
      elided.add(node.id);
  }

  const presentationByID = new Map<string, WorkflowExecutionNode>();
  for (const rawNode of rawNodes) {
    if (elided.has(rawNode.id)) continue;
    presentationByID.set(rawNode.id, {
      ...rawNode,
      parentIds: [],
      childIds: [],
      displayLabel: presentationLabel(rawNode, workflowName),
      displayType: presentationType(rawNode),
    });
  }

  const nearestVisibleDescendants = (
    id: string,
    path = new Set<string>(),
  ): string[] => {
    if (path.has(id)) return [];
    if (!elided.has(id)) return presentationByID.has(id) ? [id] : [];
    const node = rawByID.get(id);
    if (!node) return [];
    const nextPath = new Set(path).add(id);
    return dedupeStrings(
      node.hierarchyChildIds.flatMap((childID) =>
        nearestVisibleDescendants(childID, nextPath),
      ),
    );
  };

  for (const node of presentationByID.values()) {
    for (const childID of dedupeStrings(
      rawByID.get(node.id)?.hierarchyChildIds ?? [],
    )) {
      for (const visibleChildID of nearestVisibleDescendants(childID))
        addPresentationEdge(presentationByID, node.id, visibleChildID);
    }
  }

  for (const hiddenID of elided) {
    const hidden = rawByID.get(hiddenID);
    if (!hidden || hidden.hierarchyChildIds.length !== 1) continue;
    const stepNumber = stepGroupNumber(hidden);
    if (!stepNumber) continue;
    for (const childID of nearestVisibleDescendants(
      hidden.hierarchyChildIds[0],
    )) {
      const child = presentationByID.get(childID);
      if (child)
        child.displayType = `Step ${stepNumber} · ${child.displayType}`;
    }
  }

  for (const node of presentationByID.values())
    normalizeGroupedChildren(node, presentationByID);

  const nodes = [...presentationByID.values()].sort(compareExecutionNodes);
  return { nodes, roots: nodes.filter((node) => node.parentIds.length === 0) };
}

function presentationLabel(
  node: WorkflowExecutionNode,
  workflowName: string,
): string {
  if (isExitHandlerNode(node, workflowName)) return "Exit handler";
  if (
    node.parentIds.length === 0 &&
    node.displayName === workflowName &&
    node.templateName
  )
    return node.templateName;
  if (node.type !== "StepGroup") return node.displayName;
  const stepNumber = stepGroupNumber(node);
  return stepNumber
    ? `Step ${stepNumber}`
    : node.childIds.length > 1
      ? "Parallel steps"
      : "Workflow step";
}

function presentationType(node: WorkflowExecutionNode): string {
  if (node.type === "StepGroup") {
    if (node.childIds.length > 1)
      return `Parallel · ${node.childIds.length} steps`;
    return "Step group";
  }
  if (node.type === "TaskGroup")
    return `Fan-out · ${node.childIds.length} runs`;
  return workflowNodeTypeLabel(node.type);
}

function normalizeGroupedChildren(
  parent: WorkflowExecutionNode,
  nodes: Map<string, WorkflowExecutionNode>,
) {
  if (parent.type === "Retry") {
    for (const childID of parent.childIds) {
      const child = nodes.get(childID);
      if (!child) continue;
      const iteration = parseIterationName(child.displayName);
      if (
        !iteration ||
        iteration.base !== parent.displayName ||
        iteration.value !== undefined
      )
        continue;
      child.displayLabel = `Attempt ${iteration.index + 1}`;
    }
    return;
  }
  if (parent.type !== "TaskGroup" && parent.type !== "StepGroup") return;
  const group = iterationGroupInfo(parent, nodes);
  if (!group) return;
  parent.displayLabel = group.base;
  const stepNumber =
    parent.type === "StepGroup" ? stepGroupNumber(parent) : null;
  parent.displayType = `${stepNumber ? `Step ${stepNumber} · ` : ""}Fan-out · ${parent.childIds.length} runs`;
  for (const childID of parent.childIds) {
    const child = nodes.get(childID);
    if (!child) continue;
    const iteration = parseIterationName(child.displayName);
    if (!iteration) continue;
    child.displayLabel = iteration.value || `Run ${iteration.index + 1}`;
    child.displayType = `Run ${iteration.index + 1} · ${workflowNodeTypeLabel(child.type)}`;
  }
}

function iterationGroupInfo(
  parent: WorkflowExecutionNode,
  nodes?: Map<string, WorkflowExecutionNode>,
): { base: string } | null {
  if (parent.childIds.length < 2) return null;
  if (!nodes) return null;
  const iterations = parent.childIds
    .map((id) => nodes.get(id))
    .map((node) => (node ? parseIterationName(node.displayName) : null));
  if (iterations.some((iteration) => !iteration)) return null;
  const base = iterations[0]!.base;
  return iterations.every((iteration) => iteration!.base === base)
    ? { base }
    : null;
}

function parseIterationName(
  displayName: string,
): { base: string; index: number; value?: string } | null {
  const match = displayName.match(/^(.+)\((\d+)(?::(.+))?\)$/);
  if (!match) return null;
  return { base: match[1], index: Number(match[2]), value: match[3] };
}

function stepGroupNumber(node: WorkflowExecutionNode): number | null {
  const match = node.name.match(/\[(\d+)\]$/);
  return match ? Number(match[1]) + 1 : null;
}

function isExitHandlerNode(
  node: WorkflowExecutionNode,
  workflowName: string,
): boolean {
  return Boolean(
    workflowName &&
    (node.name === `${workflowName}.onExit` ||
      node.displayName === `${workflowName}.onExit`),
  );
}

function workflowNodeTypeLabel(type: string): string {
  switch (type) {
    case "Steps":
      return "Steps template";
    case "StepGroup":
      return "Step group";
    case "TaskGroup":
      return "Fan-out";
    case "Retry":
      return "Retry strategy";
    case "Skipped":
      return "Skipped step";
    default:
      return type;
  }
}

function addPresentationEdge(
  nodes: Map<string, WorkflowExecutionNode>,
  source: string,
  target: string,
) {
  if (source === target) return;
  const parent = nodes.get(source);
  const child = nodes.get(target);
  if (!parent || !child || parent.childIds.includes(target)) return;
  parent.childIds = [...parent.childIds, target];
  child.parentIds = [...child.parentIds, source];
}

function dedupeStrings(values: string[]): string[] {
  return [...new Set(values)];
}

function collectWorkflowTemplateRefs(
  workflow: any,
): WorkflowTemplateReference[] {
  const namespace = asString(workflow?.metadata?.namespace);
  const refs: WorkflowTemplateReference[] = [];
  const workflowRef = templateReferenceFromObject(
    workflow?.spec?.workflowTemplateRef,
    namespace,
    "workflow",
  );
  if (workflowRef) refs.push(workflowRef);

  const effectiveSpec = effectiveWorkflowSpec(workflow);
  for (const template of asArray(effectiveSpec?.templates)) {
    const templateMap = asRecord(template);
    const templateName = asString(templateMap.name);
    for (const task of taskLikeObjects(templateMap)) {
      const taskMap = asRecord(task);
      const ref = templateReferenceFromObject(
        taskMap.templateRef,
        namespace,
        "task",
        templateName,
        asString(taskMap.name),
      );
      if (ref) refs.push(ref);
    }
  }

  for (const raw of Object.values(asRecord(workflow?.status?.nodes))) {
    const node = asRecord(raw);
    const ref = templateReferenceFromObject(
      node.templateRef,
      namespace,
      "task",
      asString(node.templateName),
      asString(node.displayName) || asString(node.name),
    );
    if (ref) refs.push(ref);
  }

  return dedupeTemplateRefs(refs);
}

function effectiveWorkflowSpec(workflow: any): Record<string, any> {
  const stored = asRecord(workflow?.status?.storedWorkflowTemplateSpec);
  return Object.keys(stored).length > 0 ? stored : asRecord(workflow?.spec);
}

function phaseTone(phase: string): WorkflowExecutionTone {
  switch (phase) {
    case "Succeeded":
      return "success";
    case "Failed":
    case "Error":
      return "danger";
    case "Running":
    case "Pending":
      return "warning";
    case "Skipped":
    case "Omitted":
      return "muted";
    default:
      return "info";
  }
}

function isWorkflowProblemPhase(phase: string): boolean {
  return phase === "Failed" || phase === "Error";
}

function addWorkflowEdge(
  nodeById: Map<string, WorkflowExecutionNode>,
  edgeKeys: Set<string>,
  source: string,
  target: string,
) {
  const key = `${source}\u0000${target}`;
  if (edgeKeys.has(key)) return;
  const parent = nodeById.get(source);
  const child = nodeById.get(target);
  if (!parent || !child) return;
  edgeKeys.add(key);
  parent.childIds = [...parent.childIds, target];
  child.parentIds = [...child.parentIds, source];
}

function countWorkflowExecution(
  nodes: WorkflowExecutionNode[],
): WorkflowExecutionCounts {
  const counts: WorkflowExecutionCounts = {
    podTotal: 0,
    podSucceeded: 0,
    podFailed: 0,
    podRunning: 0,
    podPending: 0,
    nodeTotal: 0,
    nodeSucceeded: 0,
    nodeFailed: 0,
    nodeRunning: 0,
    nodeSkipped: 0,
  };
  for (const node of nodes) {
    if (node.type === "Pod") {
      counts.podTotal++;
      if (node.phase === "Succeeded") counts.podSucceeded++;
      else if (isWorkflowProblemPhase(node.phase)) counts.podFailed++;
      else if (node.phase === "Running") counts.podRunning++;
      else if (node.phase === "Pending") counts.podPending++;
    }
    counts.nodeTotal++;
    if (node.phase === "Succeeded") counts.nodeSucceeded++;
    else if (isWorkflowProblemPhase(node.phase)) counts.nodeFailed++;
    else if (node.phase === "Running") counts.nodeRunning++;
    else if (node.phase === "Skipped" || node.phase === "Omitted")
      counts.nodeSkipped++;
  }
  return counts;
}

function pickFocusNodes(
  nodes: WorkflowExecutionNode[],
): WorkflowExecutionNode[] {
  const failed = nodes.filter(
    (node) =>
      isWorkflowProblemPhase(node.phase) &&
      (LEAF_NODE_TYPES.has(node.type) ||
        node.type === "Container" ||
        node.message),
  );
  if (failed.length > 0) return failed.slice(0, 12);
  const active = nodes.filter(
    (node) => node.phase === "Running" || node.phase === "Pending",
  );
  return active.slice(0, 12);
}

function lineagePath(
  nodeById: Map<string, WorkflowExecutionNode>,
  terminal: WorkflowExecutionNode,
): WorkflowExecutionNode[] {
  const path: WorkflowExecutionNode[] = [];
  const seen = new Set<string>();
  let current: WorkflowExecutionNode | undefined = terminal;
  while (current && !seen.has(current.id)) {
    seen.add(current.id);
    path.unshift(current);
    const parentId: string | undefined = [...current.parentIds].sort((a, b) =>
      compareExecutionNodes(nodeById.get(a), nodeById.get(b)),
    )[0];
    current = parentId ? nodeById.get(parentId) : undefined;
  }
  return path;
}

function workflowActivity(
  workflow: any,
  nodes: WorkflowExecutionNode[],
): WorkflowExecutionActivity[] {
  const items: WorkflowExecutionActivity[] = [];
  const scheduledAt = asString(
    workflow?.metadata?.annotations?.["workflows.argoproj.io/scheduled-time"],
  );
  if (scheduledAt) {
    items.push({
      id: "workflow-scheduled",
      at: scheduledAt,
      label: "Scheduled",
      tone: "info",
    });
  }
  const startedAt = asString(workflow?.status?.startedAt);
  if (startedAt) {
    items.push({
      id: "workflow-started",
      at: startedAt,
      label: "Workflow started",
      tone: "info",
    });
  }
  for (const node of activityNodes(nodes)) {
    if (node.startedAt) {
      items.push({
        id: `${node.id}-started`,
        at: node.startedAt,
        label: `${node.displayLabel} started`,
        detail: node.displayType,
        tone: phaseTone(node.phase),
        nodeId: node.id,
      });
    }
    if (node.finishedAt) {
      items.push({
        id: `${node.id}-finished`,
        at: node.finishedAt,
        label: `${node.displayLabel} ${activityVerb(node.phase)}`,
        detail: node.message || node.displayType,
        tone: phaseTone(node.phase),
        nodeId: node.id,
      });
    }
  }
  const finishedAt = asString(workflow?.status?.finishedAt);
  if (finishedAt) {
    items.push({
      id: "workflow-finished",
      at: finishedAt,
      label: `Workflow ${activityVerb(asString(workflow?.status?.phase) || "finished")}`,
      tone: phaseTone(asString(workflow?.status?.phase)),
    });
  }
  return items.sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}

function activityNodes(
  nodes: WorkflowExecutionNode[],
): WorkflowExecutionNode[] {
  const byID = new Map(nodes.map((node) => [node.id, node]));
  const failedExecutableDescendant = (node: WorkflowExecutionNode): boolean => {
    const queue = [...node.childIds];
    const seen = new Set<string>();
    while (queue.length > 0) {
      const id = queue.shift()!;
      if (seen.has(id)) continue;
      seen.add(id);
      const child = byID.get(id);
      if (!child) continue;
      if (
        ACTIVITY_NODE_TYPES.has(child.type) &&
        isWorkflowProblemPhase(child.phase)
      )
        return true;
      queue.push(...child.childIds);
    }
    return false;
  };
  const executableMessages = new Set(
    nodes
      .filter(
        (node) =>
          ACTIVITY_NODE_TYPES.has(node.type) &&
          isWorkflowProblemPhase(node.phase),
      )
      .map((node) => node.message)
      .filter(Boolean),
  );
  return nodes.filter((node) => {
    if (ACTIVITY_NODE_TYPES.has(node.type)) return true;
    if (node.type === "Container")
      return (
        isWorkflowProblemPhase(node.phase) &&
        Boolean(node.message) &&
        !executableMessages.has(node.message)
      );
    return (
      isWorkflowProblemPhase(node.phase) &&
      Boolean(node.message) &&
      !failedExecutableDescendant(node)
    );
  });
}

function activityVerb(phase: string): string {
  switch (phase) {
    case "Succeeded":
      return "succeeded";
    case "Failed":
      return "failed";
    case "Error":
      return "errored";
    case "Skipped":
    case "Omitted":
      return "skipped";
    default:
      return "finished";
  }
}

function templateReferenceFromObject(
  raw: any,
  namespace: string,
  source: "workflow" | "task",
  template?: string,
  taskName?: string,
): WorkflowTemplateReference | null {
  const ref = asRecord(raw);
  const name = asString(ref.name);
  if (!name) return null;
  const clusterScope = ref.clusterScope === true;
  return {
    name,
    kind: clusterScope ? "ClusterWorkflowTemplate" : "WorkflowTemplate",
    resourceKind: clusterScope
      ? "clusterworkflowtemplates"
      : "workflowtemplates",
    namespace: clusterScope ? "" : namespace,
    clusterScope,
    source,
    template: asString(ref.template) || template,
    taskName,
  };
}

function taskLikeObjects(template: Record<string, any>): any[] {
  const tasks: any[] = [];
  for (const task of asArray(template?.dag?.tasks)) tasks.push(task);
  for (const group of asArray(template?.steps)) {
    for (const step of asArray(group)) tasks.push(step);
  }
  return tasks;
}

function dedupeTemplateRefs(
  refs: WorkflowTemplateReference[],
): WorkflowTemplateReference[] {
  const seen = new Set<string>();
  const out: WorkflowTemplateReference[] = [];
  for (const ref of refs) {
    const key = [
      ref.resourceKind,
      ref.namespace,
      ref.name,
      ref.source,
      ref.template || "",
      ref.taskName || "",
    ].join("\u0000");
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(ref);
  }
  return out;
}

function compareExecutionNodes(
  a?: WorkflowExecutionNode,
  b?: WorkflowExecutionNode,
): number {
  if (!a && !b) return 0;
  if (!a) return 1;
  if (!b) return -1;
  const aTime = a.startedAt
    ? Date.parse(a.startedAt)
    : Number.POSITIVE_INFINITY;
  const bTime = b.startedAt
    ? Date.parse(b.startedAt)
    : Number.POSITIVE_INFINITY;
  if (aTime !== bTime) return aTime - bTime;
  return a.displayName.localeCompare(b.displayName) || a.id.localeCompare(b.id);
}

function asRecord(value: any): Record<string, any> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value
    : {};
}

function asArray(value: any): any[] {
  return Array.isArray(value) ? value : [];
}

function asString(value: any): string {
  return typeof value === "string" ? value : "";
}

function asStringArray(value: any): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

function asNumberRecord(value: any): Record<string, number> | undefined {
  const record = asRecord(value);
  const out: Record<string, number> = {};
  for (const [key, raw] of Object.entries(record)) {
    if (typeof raw === "number") out[key] = raw;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}
