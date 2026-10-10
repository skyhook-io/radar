import { gitOpsDenialMessage, type GitOpsPermission } from '@skyhook-io/k8s-ui/utils/gitops-permissions'
import type { ErrorBody } from './httpErrors'
import type { RadarFeatureSupport } from './radarFeatures'

export interface GitOpsActionCapability {
  allowed?: boolean
  /** Every operation the caller is denied, so grant guidance is complete. */
  denied?: GitOpsPermission[]
  /** The action can't run on this object whatever the caller's role. */
  unsupported?: boolean
  reason?: string
}

export interface GitOpsActionCapabilities {
  actions: Record<string, GitOpsActionCapability>
}

export const GITOPS_ACTION_KINDS = ['applications', 'gitrepositories', 'ocirepositories', 'helmrepositories', 'kustomizations', 'helmreleases', 'alerts']

export function isGitOpsActionTarget(kind: string, group: string | undefined): boolean {
  return GITOPS_ACTION_KINDS.includes(kind) && (kind === 'applications' ? group === 'argoproj.io' : !!group?.endsWith('.toolkit.fluxcd.io'))
}

function denialFromBody(body: ErrorBody): GitOpsPermission {
  return { verb: body.verb, group: body.group, resource: body.resource, namespace: body.namespace, name: body.name, kind: body.kind }
}

/** The per-action permissions to gate on; none for a Radar without the endpoint. */
export function gitOpsActionPermissions(
  support: RadarFeatureSupport,
  data: GitOpsActionCapabilities | undefined,
  unsupported: boolean,
): Record<string, GitOpsActionCapability> | undefined {
  if (support === 'unsupported' || unsupported) return undefined
  return data?.actions
}

export function gitOpsDisabledReasons(
  support: RadarFeatureSupport,
  data: GitOpsActionCapabilities | undefined,
  unsupported: boolean,
): Record<string, string | undefined> {
  const permissions = gitOpsActionPermissions(support, data, unsupported)
  return Object.fromEntries(Object.entries(permissions ?? {}).map(([action, capability]) => [action,
    capability.allowed === false
      ? gitOpsDenialMessage(capability.denied ?? [])
      : capability.unsupported ? capability.reason : undefined,
  ]))
}

/**
 * Only a structured `rbac_denied` is presented as a role problem. Any other
 * refusal, including an admission policy that answered 403, shows the
 * server's own message.
 */
export class GitOpsActionError extends Error {
  readonly rawDetail?: string
  constructor(body: ErrorBody, status: number) {
    const raw = body.error || `HTTP ${status}`
    const message: string = body.error_code === 'rbac_denied'
      ? gitOpsDenialMessage([denialFromBody(body)])
      : body.error_code === 'admission_denied' ? body.summary : raw
    super(message)
    this.name = 'GitOpsActionError'
    if (message !== raw) this.rawDetail = raw
  }
}
