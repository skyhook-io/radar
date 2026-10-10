import { getFriendlyError } from '@skyhook-io/k8s-ui/utils/k8s-errors'
import type { ErrorBody } from './httpErrors'
import type { RadarFeatureSupport } from './radarFeatures'

export interface GitOpsActionCapabilities {
  actions: Record<string, { allowed: boolean; reason?: string }>
}

export const GITOPS_ACTION_KINDS = ['applications', 'gitrepositories', 'ocirepositories', 'helmrepositories', 'kustomizations', 'helmreleases', 'alerts']

export const GITOPS_ACTIONS = ['sync', 'refresh', 'terminate', 'suspend', 'resume', 'rollback', 'validate', 'reconcile', 'sync-with-source']

export function gitOpsDisabledReasons(
  support: RadarFeatureSupport,
  data: GitOpsActionCapabilities | undefined,
  error: unknown,
  unsupported: boolean,
): Record<string, string | undefined> {
  if (support === 'unsupported' || unsupported) return {}
  const failure = error as { status?: number; data?: ErrorBody } | undefined
  const failureReason = failure?.status === 403 && failure.data ? new GitOpsActionError(failure.data, 403).message : "Couldn't verify your permissions. Try again."
  return Object.fromEntries(GITOPS_ACTIONS.map(action => [action,
    error ? failureReason
      : !data ? 'Checking your permissions…'
        : data.actions[action]?.allowed ? undefined
          : data.actions[action]?.reason || 'This action is unavailable.',
  ]))
}

export class GitOpsActionError extends Error {
  readonly rawDetail?: string
  constructor(body: ErrorBody, status: number) {
    const raw = body.error || `HTTP ${status}`
    const friendly = getFriendlyError(raw)
    const message = body.code === 'rbac_denied'
      ? `Your role can't ${body.verb} ${body.resource} in API group ${body.group} in namespace ${body.namespace}. Ask your cluster admin for GitOps action access.`
      : friendly ? `${friendly.summary}. ${friendly.suggestion}` : raw
    super(message)
    this.name = 'GitOpsActionError'
    if (message !== raw) this.rawDetail = raw
  }
}
