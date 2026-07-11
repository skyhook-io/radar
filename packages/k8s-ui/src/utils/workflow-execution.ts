export type WorkflowExecutionTone = 'success' | 'danger' | 'warning' | 'info' | 'muted'

export interface WorkflowExecutionNode {
  id: string
  name: string
  displayName: string
  type: string
  phase: string
  templateName?: string
  templateScope?: string
  boundaryId?: string
  podName?: string
  templateRef?: WorkflowTemplateReference
  message?: string
  startedAt?: string
  finishedAt?: string
  parentIds: string[]
  childIds: string[]
}

export interface WorkflowExecutionEdge {
  source: string
  target: string
}

export interface WorkflowExecutionCounts {
  podTotal: number
  podSucceeded: number
  podFailed: number
  podRunning: number
  podPending: number
  nodeTotal: number
  nodeSucceeded: number
  nodeFailed: number
  nodeRunning: number
  nodeSkipped: number
}

export interface WorkflowExecutionPath {
  terminal: WorkflowExecutionNode
  nodes: WorkflowExecutionNode[]
  tone: WorkflowExecutionTone
}

export interface WorkflowExecutionActivity {
  id: string
  at: string
  label: string
  detail?: string
  tone: WorkflowExecutionTone
  nodeId?: string
}

export interface WorkflowTemplateReference {
  name: string
  kind: 'WorkflowTemplate' | 'ClusterWorkflowTemplate'
  resourceKind: 'workflowtemplates' | 'clusterworkflowtemplates'
  namespace: string
  clusterScope: boolean
  source: 'workflow' | 'task'
  template?: string
  taskName?: string
}

export interface WorkflowExecutionModel {
  nodes: WorkflowExecutionNode[]
  edges: WorkflowExecutionEdge[]
  roots: WorkflowExecutionNode[]
  visibleSteps: WorkflowExecutionNode[]
  focusPaths: WorkflowExecutionPath[]
  activity: WorkflowExecutionActivity[]
  counts: WorkflowExecutionCounts
  templateRefs: WorkflowTemplateReference[]
  resourcesDuration?: Record<string, number>
  isLarge: boolean
}

const STEP_NODE_TYPES = new Set(['Pod', 'Steps', 'StepGroup', 'DAG', 'TaskGroup', 'Suspend', 'Skipped'])
const LEAF_NODE_TYPES = new Set(['Pod', 'Suspend', 'Skipped'])
const LARGE_WORKFLOW_NODE_COUNT = 80

export function buildWorkflowExecutionModel(workflow: any): WorkflowExecutionModel {
  const rawNodes = asRecord(workflow?.status?.nodes)
  const nodeById = new Map<string, WorkflowExecutionNode>()

  for (const [id, raw] of Object.entries(rawNodes)) {
    const node = asRecord(raw)
    const displayName = asString(node.displayName) || asString(node.name) || id
    const templateRef = templateReferenceFromObject(node.templateRef, asString(workflow?.metadata?.namespace), 'task', asString(node.templateName), displayName)
    nodeById.set(id, {
      id,
      name: asString(node.name) || displayName,
      displayName,
      type: asString(node.type) || 'Unknown',
      phase: asString(node.phase) || (asString(node.type) === 'Skipped' ? 'Skipped' : 'Pending'),
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
    })
  }

  const edgeKeys = new Set<string>()
  for (const [id, raw] of Object.entries(rawNodes)) {
    const node = nodeById.get(id)
    if (!node) continue
    const rawNode = asRecord(raw)
    const childIds = [...asStringArray(rawNode.children), ...asStringArray(rawNode.outboundNodes)]
    for (const childId of childIds) {
      if (!nodeById.has(childId)) continue
      addWorkflowEdge(nodeById, edgeKeys, id, childId)
    }
  }

  for (const node of nodeById.values()) {
    if (node.parentIds.length > 0 || !node.boundaryId || node.boundaryId === node.id || !nodeById.has(node.boundaryId)) continue
    addWorkflowEdge(nodeById, edgeKeys, node.boundaryId, node.id)
  }

  const nodes = [...nodeById.values()].sort(compareExecutionNodes)
  const roots = nodes.filter((node) => node.parentIds.length === 0)
  const visibleSteps = nodes.filter((node) => STEP_NODE_TYPES.has(node.type))
  const counts = countWorkflowExecution(nodes)
  const focusNodes = pickFocusNodes(nodes)

  return {
    nodes,
    edges: [...edgeKeys].map((key) => {
      const [source, target] = key.split('\u0000')
      return { source, target }
    }),
    roots,
    visibleSteps,
    focusPaths: focusNodes.map((node) => ({
      terminal: node,
      nodes: lineagePath(nodeById, node),
      tone: phaseTone(node.phase),
    })),
    activity: workflowActivity(workflow, visibleSteps),
    counts,
    templateRefs: collectWorkflowTemplateRefs(workflow),
    resourcesDuration: asNumberRecord(workflow?.status?.resourcesDuration),
    isLarge: nodes.length > LARGE_WORKFLOW_NODE_COUNT,
  }
}

export function collectWorkflowTemplateRefs(workflow: any): WorkflowTemplateReference[] {
  const namespace = asString(workflow?.metadata?.namespace)
  const refs: WorkflowTemplateReference[] = []
  const workflowRef = templateReferenceFromObject(workflow?.spec?.workflowTemplateRef, namespace, 'workflow')
  if (workflowRef) refs.push(workflowRef)

  const effectiveSpec = effectiveWorkflowSpec(workflow)
  for (const template of asArray(effectiveSpec?.templates)) {
    const templateMap = asRecord(template)
    const templateName = asString(templateMap.name)
    for (const task of taskLikeObjects(templateMap)) {
      const taskMap = asRecord(task)
      const ref = templateReferenceFromObject(taskMap.templateRef, namespace, 'task', templateName, asString(taskMap.name))
      if (ref) refs.push(ref)
    }
  }

  for (const raw of Object.values(asRecord(workflow?.status?.nodes))) {
    const node = asRecord(raw)
    const ref = templateReferenceFromObject(node.templateRef, namespace, 'task', asString(node.templateName), asString(node.displayName) || asString(node.name))
    if (ref) refs.push(ref)
  }

  return dedupeTemplateRefs(refs)
}

function effectiveWorkflowSpec(workflow: any): Record<string, any> {
  const stored = asRecord(workflow?.status?.storedWorkflowTemplateSpec)
  return Object.keys(stored).length > 0 ? stored : asRecord(workflow?.spec)
}

export function phaseTone(phase: string): WorkflowExecutionTone {
  switch (phase) {
    case 'Succeeded':
      return 'success'
    case 'Failed':
    case 'Error':
      return 'danger'
    case 'Running':
    case 'Pending':
      return 'warning'
    case 'Skipped':
    case 'Omitted':
      return 'muted'
    default:
      return 'info'
  }
}

export function isWorkflowProblemPhase(phase: string): boolean {
  return phase === 'Failed' || phase === 'Error'
}

function addWorkflowEdge(nodeById: Map<string, WorkflowExecutionNode>, edgeKeys: Set<string>, source: string, target: string) {
  const key = `${source}\u0000${target}`
  if (edgeKeys.has(key)) return
  const parent = nodeById.get(source)
  const child = nodeById.get(target)
  if (!parent || !child) return
  edgeKeys.add(key)
  parent.childIds = [...parent.childIds, target]
  child.parentIds = [...child.parentIds, source]
}

function countWorkflowExecution(nodes: WorkflowExecutionNode[]): WorkflowExecutionCounts {
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
  }
  for (const node of nodes) {
    if (node.type === 'Pod') {
      counts.podTotal++
      if (node.phase === 'Succeeded') counts.podSucceeded++
      else if (isWorkflowProblemPhase(node.phase)) counts.podFailed++
      else if (node.phase === 'Running') counts.podRunning++
      else if (node.phase === 'Pending') counts.podPending++
    }
    if (STEP_NODE_TYPES.has(node.type)) {
      counts.nodeTotal++
      if (node.phase === 'Succeeded') counts.nodeSucceeded++
      else if (isWorkflowProblemPhase(node.phase)) counts.nodeFailed++
      else if (node.phase === 'Running') counts.nodeRunning++
      else if (node.phase === 'Skipped' || node.phase === 'Omitted') counts.nodeSkipped++
    }
  }
  return counts
}

function pickFocusNodes(nodes: WorkflowExecutionNode[]): WorkflowExecutionNode[] {
  const failed = nodes.filter((node) => isWorkflowProblemPhase(node.phase) && (LEAF_NODE_TYPES.has(node.type) || node.message))
  if (failed.length > 0) return failed.slice(0, 12)
  const active = nodes.filter((node) => (node.phase === 'Running' || node.phase === 'Pending') && STEP_NODE_TYPES.has(node.type))
  return active.slice(0, 12)
}

function lineagePath(nodeById: Map<string, WorkflowExecutionNode>, terminal: WorkflowExecutionNode): WorkflowExecutionNode[] {
  const path: WorkflowExecutionNode[] = []
  const seen = new Set<string>()
  let current: WorkflowExecutionNode | undefined = terminal
  while (current && !seen.has(current.id)) {
    seen.add(current.id)
    path.unshift(current)
    const parentId: string | undefined = [...current.parentIds].sort((a, b) => compareExecutionNodes(nodeById.get(a), nodeById.get(b)))[0]
    current = parentId ? nodeById.get(parentId) : undefined
  }
  return path
}

function workflowActivity(workflow: any, nodes: WorkflowExecutionNode[]): WorkflowExecutionActivity[] {
  const items: WorkflowExecutionActivity[] = []
  const scheduledAt = asString(workflow?.metadata?.annotations?.['workflows.argoproj.io/scheduled-time'])
  if (scheduledAt) {
    items.push({ id: 'workflow-scheduled', at: scheduledAt, label: 'Scheduled', tone: 'info' })
  }
  const startedAt = asString(workflow?.status?.startedAt)
  if (startedAt) {
    items.push({ id: 'workflow-started', at: startedAt, label: 'Workflow started', tone: 'info' })
  }
  for (const node of nodes) {
    if (node.startedAt) {
      items.push({
        id: `${node.id}-started`,
        at: node.startedAt,
        label: `${node.displayName} started`,
        detail: node.type,
        tone: phaseTone(node.phase),
        nodeId: node.id,
      })
    }
    if (node.finishedAt) {
      items.push({
        id: `${node.id}-finished`,
        at: node.finishedAt,
        label: `${node.displayName} ${activityVerb(node.phase)}`,
        detail: node.message || node.type,
        tone: phaseTone(node.phase),
        nodeId: node.id,
      })
    }
  }
  const finishedAt = asString(workflow?.status?.finishedAt)
  if (finishedAt) {
    items.push({ id: 'workflow-finished', at: finishedAt, label: `Workflow ${activityVerb(asString(workflow?.status?.phase) || 'finished')}`, tone: phaseTone(asString(workflow?.status?.phase)) })
  }
  return items.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
}

function activityVerb(phase: string): string {
  switch (phase) {
    case 'Succeeded':
      return 'succeeded'
    case 'Failed':
      return 'failed'
    case 'Error':
      return 'errored'
    case 'Skipped':
    case 'Omitted':
      return 'skipped'
    default:
      return 'finished'
  }
}

function templateReferenceFromObject(raw: any, namespace: string, source: 'workflow' | 'task', template?: string, taskName?: string): WorkflowTemplateReference | null {
  const ref = asRecord(raw)
  const name = asString(ref.name)
  if (!name) return null
  const clusterScope = ref.clusterScope === true
  return {
    name,
    kind: clusterScope ? 'ClusterWorkflowTemplate' : 'WorkflowTemplate',
    resourceKind: clusterScope ? 'clusterworkflowtemplates' : 'workflowtemplates',
    namespace: clusterScope ? '' : namespace,
    clusterScope,
    source,
    template: asString(ref.template) || template,
    taskName,
  }
}

function taskLikeObjects(template: Record<string, any>): any[] {
  const tasks: any[] = []
  for (const task of asArray(template?.dag?.tasks)) tasks.push(task)
  for (const group of asArray(template?.steps)) {
    for (const step of asArray(group)) tasks.push(step)
  }
  return tasks
}

function dedupeTemplateRefs(refs: WorkflowTemplateReference[]): WorkflowTemplateReference[] {
  const seen = new Set<string>()
  const out: WorkflowTemplateReference[] = []
  for (const ref of refs) {
    const key = [ref.resourceKind, ref.namespace, ref.name, ref.source, ref.template || '', ref.taskName || ''].join('\u0000')
    if (seen.has(key)) continue
    seen.add(key)
    out.push(ref)
  }
  return out
}

function compareExecutionNodes(a?: WorkflowExecutionNode, b?: WorkflowExecutionNode): number {
  if (!a && !b) return 0
  if (!a) return 1
  if (!b) return -1
  const aTime = a.startedAt ? Date.parse(a.startedAt) : Number.POSITIVE_INFINITY
  const bTime = b.startedAt ? Date.parse(b.startedAt) : Number.POSITIVE_INFINITY
  if (aTime !== bTime) return aTime - bTime
  return a.displayName.localeCompare(b.displayName) || a.id.localeCompare(b.id)
}

function asRecord(value: any): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {}
}

function asArray(value: any): any[] {
  return Array.isArray(value) ? value : []
}

function asString(value: any): string {
  return typeof value === 'string' ? value : ''
}

function asStringArray(value: any): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}

function asNumberRecord(value: any): Record<string, number> | undefined {
  const record = asRecord(value)
  const out: Record<string, number> = {}
  for (const [key, raw] of Object.entries(record)) {
    if (typeof raw === 'number') out[key] = raw
  }
  return Object.keys(out).length > 0 ? out : undefined
}
