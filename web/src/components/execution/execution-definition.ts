import { formatCPUString, formatMemoryString } from '@skyhook-io/k8s-ui/utils/format'

export interface ExecutionUnitSummary {
  name: string
  type: string
  image?: string
  command?: string
  requests?: string
  limits?: string
}

export interface ExecutionDefinitionSummary {
  shape: string
  units: ExecutionUnitSummary[]
  externalTemplates: string[]
  configMaps: string[]
  secrets: string[]
  imagePullSecrets: string[]
  serviceAccount: string
  retry: string
  deadline?: string
  parallelism?: string
}

export function executionDefinitionSummary(kind: string, resource: any): ExecutionDefinitionSummary | null {
  if (!resource) return null
  switch (kind.toLowerCase()) {
    case 'job':
    case 'jobs':
      return kubernetesJobSummary(resource.spec ?? {})
    case 'cronjob':
    case 'cronjobs':
      return kubernetesJobSummary(resource.spec?.jobTemplate?.spec ?? {})
    case 'scaledjob':
    case 'scaledjobs':
      return kubernetesJobSummary(resource.spec?.jobTargetRef ?? {})
    case 'cronworkflow':
    case 'cronworkflows':
      return argoWorkflowSummary(resource.spec?.workflowSpec ?? {})
    case 'workflowtemplate':
    case 'workflowtemplates':
    case 'clusterworkflowtemplate':
    case 'clusterworkflowtemplates':
      return argoWorkflowSummary(resource.spec ?? {})
    case 'workflow':
    case 'workflows':
      return argoWorkflowSummary(resource.status?.storedWorkflowTemplateSpec ?? resource.spec ?? {})
    default:
      return null
  }
}

export function executionDefinitionFingerprint(summary: ExecutionDefinitionSummary | null): string {
  if (!summary) return ''
  return JSON.stringify(summary)
}

function kubernetesJobSummary(jobSpec: any): ExecutionDefinitionSummary {
  const podSpec = jobSpec.template?.spec ?? {}
  const containers = Array.isArray(podSpec.containers) ? podSpec.containers : []
  const initContainers = Array.isArray(podSpec.initContainers) ? podSpec.initContainers : []
  const units = [
    ...initContainers.map((container: any) => containerSummary(container, 'Init container')),
    ...containers.map((container: any) => containerSummary(container, 'Container')),
  ]
  const dependencies = podDependencies(podSpec, [...initContainers, ...containers])
  const shapeParts = [`${containers.length} ${containers.length === 1 ? 'container' : 'containers'}`]
  if (initContainers.length > 0) shapeParts.push(`${initContainers.length} init`)
  const parallelism = Number(jobSpec.parallelism ?? 1)
  const completions = Number(jobSpec.completions ?? 1)
  const completionMode = jobSpec.completionMode || 'NonIndexed'
  return {
    shape: `Pod · ${shapeParts.join(' · ')}`,
    units,
    externalTemplates: [],
    configMaps: dependencies.configMaps,
    secrets: dependencies.secrets,
    imagePullSecrets: pullSecretNames(podSpec),
    serviceAccount: podSpec.serviceAccountName || 'default',
    retry: `Backoff limit ${jobSpec.backoffLimit ?? 6} · restart ${podSpec.restartPolicy || 'Never'}`,
    ...(jobSpec.activeDeadlineSeconds != null ? { deadline: `${jobSpec.activeDeadlineSeconds}s` } : {}),
    parallelism: `${parallelism} parallel · ${completions} ${completions === 1 ? 'completion' : 'completions'} · ${completionMode}`,
  }
}

function argoWorkflowSummary(spec: any): ExecutionDefinitionSummary {
  const templates = Array.isArray(spec.templates) ? spec.templates : []
  const entrypoint = templates.find((template: any) => template?.name === spec.entrypoint)
  const units = templates.flatMap((template: any) => argoTemplateUnits(template))
  const dependencies = podDependencies(spec, unitsSourceContainers(templates))
  for (const template of templates) {
    const templateDependencies = podDependencies(template, [])
    dependencies.configMaps.push(...templateDependencies.configMaps)
    dependencies.secrets.push(...templateDependencies.secrets)
  }
  const imagePullSecrets = unique([
    ...pullSecretNames(spec),
    ...templates.flatMap((template: any) => pullSecretNames(template)),
  ])
  const retries = templates
    .filter((template: any) => template?.retryStrategy != null)
    .map((template: any) => formatArgoRetry(template.name || 'template', template.retryStrategy))
  if (spec.retryStrategy != null) retries.unshift(formatArgoRetry('Workflow', spec.retryStrategy))
  if (spec.templateDefaults?.retryStrategy != null) retries.unshift(formatArgoRetry('Defaults', spec.templateDefaults.retryStrategy))
  return {
    shape: entrypoint
      ? argoTemplateShape(entrypoint)
      : spec.workflowTemplateRef?.name
        ? `${spec.workflowTemplateRef.clusterScope ? 'ClusterWorkflowTemplate' : 'WorkflowTemplate'} · ${spec.workflowTemplateRef.name}`
        : templates.length > 0
          ? `${templates.length} defined ${templates.length === 1 ? 'template' : 'templates'}`
          : 'Inline workflow',
    units,
    externalTemplates: argoExternalTemplateRefs(spec, templates),
    configMaps: unique(dependencies.configMaps),
    secrets: unique(dependencies.secrets),
    imagePullSecrets,
    serviceAccount: spec.serviceAccountName || 'default',
    retry: retries.length > 0 ? retries.join(' · ') : 'Not configured',
    ...(spec.activeDeadlineSeconds != null ? { deadline: `${spec.activeDeadlineSeconds}s` } : {}),
    ...(spec.parallelism != null ? { parallelism: `${spec.parallelism} maximum` } : argoShapePolicy(entrypoint)),
  }
}

function pullSecretNames(podSpec: any): string[] {
  return unique((podSpec?.imagePullSecrets ?? []).flatMap((secret: any) => secret?.name ? [String(secret.name)] : []))
}

function argoTemplateShape(template: any): string {
  const name = template.name || 'entrypoint'
  if (template.dag) {
    const tasks = Array.isArray(template.dag.tasks) ? template.dag.tasks.length : 0
    return `DAG · ${name} · ${tasks} ${tasks === 1 ? 'task' : 'tasks'}`
  }
  if (template.steps) {
    const groups = Array.isArray(template.steps) ? template.steps.length : 0
    const steps = Array.isArray(template.steps) ? template.steps.reduce((sum: number, group: any) => sum + (Array.isArray(group) ? group.length : 0), 0) : 0
    return `Steps · ${name} · ${steps} ${steps === 1 ? 'step' : 'steps'} in ${groups} ${groups === 1 ? 'group' : 'groups'}`
  }
  if (template.containerSet) {
    const containers = Array.isArray(template.containerSet.containers) ? template.containerSet.containers.length : 0
    return `Container set · ${name} · ${containers} ${containers === 1 ? 'container' : 'containers'}`
  }
  if (template.script) return `Script · ${name}`
  if (template.container) return `Container · ${name}`
  if (template.resource) return `Resource · ${name} · ${template.resource.action || 'operation'}`
  if (template.suspend != null) return `Suspend · ${name}`
  if (template.http) return `HTTP · ${name}`
  return `Template · ${name}`
}

function argoTemplateUnits(template: any): ExecutionUnitSummary[] {
  if (template.container) return [containerSummary({ ...template.container, resources: template.container.resources ?? template.resources }, 'Container', template.name)]
  if (template.script) return [containerSummary({ ...template.script, resources: template.script.resources ?? template.resources }, 'Script', template.name)]
  if (template.containerSet?.containers) {
    return template.containerSet.containers.map((container: any) => containerSummary(container, 'Container', `${template.name}/${container.name}`))
  }
  return []
}

function argoExternalTemplateRefs(spec: any, templates: any[]): string[] {
  const refs: string[] = []
  if (templates.length === 0 && spec.workflowTemplateRef?.name) refs.push(`${spec.workflowTemplateRef.clusterScope ? 'ClusterWorkflowTemplate' : 'WorkflowTemplate'}/${spec.workflowTemplateRef.name}`)
  for (const template of templates) {
    const tasks = Array.isArray(template?.dag?.tasks) ? template.dag.tasks : []
    const steps = Array.isArray(template?.steps) ? template.steps.flatMap((group: any) => Array.isArray(group) ? group : []) : []
    for (const item of [...tasks, ...steps]) {
      if (item?.templateRef?.name) refs.push(`${item.templateRef.clusterScope ? 'ClusterWorkflowTemplate' : 'WorkflowTemplate'}/${item.templateRef.name}`)
    }
  }
  return unique(refs)
}

function unitsSourceContainers(templates: any[]): any[] {
  return templates.flatMap((template) => {
    if (template?.container) return [template.container]
    if (template?.script) return [template.script]
    return Array.isArray(template?.containerSet?.containers) ? template.containerSet.containers : []
  })
}

function containerSummary(container: any, type: string, fallbackName?: string): ExecutionUnitSummary {
  const command = [...(Array.isArray(container.command) ? container.command : []), ...(Array.isArray(container.args) ? container.args : [])]
  const requests = formatResources(container.resources?.requests)
  const limits = formatResources(container.resources?.limits)
  return {
    name: container.name || fallbackName || type,
    type,
    ...(container.image ? { image: String(container.image) } : {}),
    ...(command.length > 0 ? { command: command.map(String).join(' ') } : {}),
    ...(requests && requests !== '-' ? { requests } : {}),
    ...(limits && limits !== '-' ? { limits } : {}),
  }
}

function podDependencies(podSpec: any, containers: any[]): { configMaps: string[]; secrets: string[] } {
  const configMaps: string[] = []
  const secrets: string[] = []
  for (const pullSecret of podSpec?.imagePullSecrets ?? []) {
    if (pullSecret?.name) secrets.push(pullSecret.name)
  }
  for (const container of containers) {
    for (const source of container?.envFrom ?? []) {
      if (source?.configMapRef?.name) configMaps.push(source.configMapRef.name)
      if (source?.secretRef?.name) secrets.push(source.secretRef.name)
    }
    for (const variable of container?.env ?? []) {
      if (variable?.valueFrom?.configMapKeyRef?.name) configMaps.push(variable.valueFrom.configMapKeyRef.name)
      if (variable?.valueFrom?.secretKeyRef?.name) secrets.push(variable.valueFrom.secretKeyRef.name)
    }
  }
  for (const volume of podSpec?.volumes ?? []) {
    if (volume?.configMap?.name) configMaps.push(volume.configMap.name)
    if (volume?.secret?.secretName) secrets.push(volume.secret.secretName)
    for (const source of volume?.projected?.sources ?? []) {
      if (source?.configMap?.name) configMaps.push(source.configMap.name)
      if (source?.secret?.name) secrets.push(source.secret.name)
    }
  }
  return { configMaps: unique(configMaps), secrets: unique(secrets) }
}

function formatArgoRetry(name: string, retry: any): string {
  if (retry && Object.keys(retry).length === 0) return `${name}: until completion`
  const limit = retry?.limit != null ? `${retry.limit} retries` : 'controller limit'
  const policy = retry?.retryPolicy || 'OnFailure'
  const backoff = retry?.backoff?.duration ? ` · ${retry.backoff.duration} backoff` : ''
  return `${name}: ${limit} · ${policy}${backoff}`
}

function argoShapePolicy(entrypoint: any): { parallelism?: string } {
  if (entrypoint?.dag?.failFast != null) return { parallelism: `DAG fail-fast ${entrypoint.dag.failFast ? 'on' : 'off'}` }
  return {}
}

function unique(values: string[]): string[] {
  return [...new Set(values)].sort((a, b) => a.localeCompare(b))
}

function formatResources(resources: any): string | undefined {
  const parts = []
  if (resources?.cpu) parts.push(`CPU ${formatCPUString(String(resources.cpu)).replace(/^1 cores$/, '1 core')}`)
  if (resources?.memory) parts.push(`memory ${formatMemoryString(String(resources.memory))}`)
  return parts.length > 0 ? parts.join(' · ') : undefined
}
