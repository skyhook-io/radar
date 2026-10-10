import { getFriendlyError } from '@skyhook-io/k8s-ui/utils/k8s-errors'
import type { ErrorBody } from './httpErrors'
import type { RadarFeatureSupport } from './radarFeatures'

export interface GitOpsActionCapability {
  allowed?: boolean
  reason?: string
  error_code?: string
  verb?: string
  group?: string
  resource?: string
  namespace?: string
  kind?: string
  name?: string
  source?: boolean
}

export interface GitOpsActionCapabilities {
  actions: Record<string, GitOpsActionCapability>
}

export const GITOPS_ACTION_KINDS = ['applications', 'gitrepositories', 'ocirepositories', 'helmrepositories', 'kustomizations', 'helmreleases', 'alerts']
export const GITOPS_ACTIONS = ['sync', 'refresh', 'terminate', 'suspend', 'resume', 'rollback', 'validate', 'reconcile', 'sync-with-source']

export function isGitOpsActionTarget(kind: string, group: string | undefined): boolean {
  return GITOPS_ACTION_KINDS.includes(kind) && (kind === 'applications' ? group === 'argoproj.io' : !!group?.endsWith('.toolkit.fluxcd.io'))
}

export function gitOpsDenialMessage(denial: GitOpsActionCapability | ErrorBody): string {
  const label = denial.group === 'argoproj.io' && denial.resource === 'applications'
    ? 'Argo CD Applications'
    : `Flux ${denial.kind}${denial.name ? ` ${denial.name}` : ''}`
  if (denial.source) {
    return `Sync with source also reconciles ${denial.kind} ${denial.namespace}/${denial.name} — your role can't ${denial.verb} it.`
  }
  return `Your role can't ${denial.verb} ${label} in ${denial.namespace}.`
}

export function gitOpsDisabledReasons(
  support: RadarFeatureSupport,
  data: GitOpsActionCapabilities | undefined,
  error: unknown,
  unsupported: boolean,
): Record<string, string | undefined> {
  if (support === 'unsupported' || unsupported) return {}
  if (data) return Object.fromEntries(Object.entries(data.actions).map(([action, capability]) => [action,
    capability.allowed === false ? gitOpsDenialMessage(capability) : undefined,
  ]))
  const failure = error as { status?: number; data?: ErrorBody } | undefined
  if (failure?.status === 403 && failure.data?.error_code === 'rbac_denied') {
    return Object.fromEntries(GITOPS_ACTIONS.map(action => [action, gitOpsDenialMessage(failure.data!)]))
  }
  return {}
}

export class GitOpsActionError extends Error {
  readonly rawDetail?: string
  constructor(body: ErrorBody, status: number) {
    const raw = body.error || `HTTP ${status}`
    const friendly = (status === 403 || /forbidden/i.test(raw)) ? getFriendlyError(raw) : undefined
    const message = body.error_code === 'rbac_denied'
      ? gitOpsDenialMessage(body)
      : body.error_code === 'admission_denied' ? `Rejected by an admission policy: ${raw}`
        : friendly ? `${friendly.summary}. ${friendly.suggestion}` : raw
    super(message)
    this.name = 'GitOpsActionError'
    if (message !== raw) this.rawDetail = raw
  }
}
