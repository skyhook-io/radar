import { isApiGroup } from '../components/resources/resource-utils-cnpg'

/** Lists the Kargo Stages allowed to update an Argo CD Application, as "<project>:<stage>[,...]". */
export const KARGO_AUTHORIZED_STAGE_ANNOTATION = 'kargo.akuity.io/authorized-stage'

export interface KargoStageRef {
  project: string
  stage: string
}

interface MaybeApplication {
  apiVersion?: unknown
  kind?: unknown
  metadata?: { annotations?: Record<string, string | undefined> | null } | null
}

/**
 * The Stages an Argo CD Application's authorized-stage annotation names, or
 * null when the object isn't an Application or the annotation is absent or
 * malformed. Parsed as Kargo does: a malformed or glob entry rejects the
 * whole value, since Kargo then lets no Stage update the Application.
 */
export function kargoAuthorizedStages(app: MaybeApplication | null | undefined): KargoStageRef[] | null {
  if (!app || app.kind !== 'Application' || !isApiGroup(app.apiVersion, 'argoproj.io')) return null
  const value = app.metadata?.annotations?.[KARGO_AUTHORIZED_STAGE_ANNOTATION]
  if (typeof value !== 'string') return null
  const out: KargoStageRef[] = []
  for (const raw of value.split(',')) {
    const entry = raw.trim()
    if (!entry) continue
    const at = entry.indexOf(':')
    const project = at < 0 ? '' : entry.slice(0, at).trim()
    const stage = at < 0 ? '' : entry.slice(at + 1).trim()
    if (!project || !stage || entry.includes('*')) return null
    if (!out.some((s) => s.project === project && s.stage === stage)) out.push({ project, stage })
  }
  return out.length > 0 ? out : null
}
