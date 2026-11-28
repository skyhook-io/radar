import { KARGO_GROUP } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import type { KargoProjectSummary } from '../../api/kargo'

export { KARGO_GROUP }

export const KARGO_PATH = '/kargo'
export const KARGO_TITLE = 'Kargo pipelines'

/** The Resources sidebar category Kargo kinds are listed under (api-resources.ts formats kargo.akuity.io so). */
export const KARGO_SIDEBAR_CATEGORY = 'Akuity Kargo'

/** Kargo's name in the sidebar's Integrations, the page it opens; "Akuity Kargo" sits beneath it. */
export const KARGO_SIDEBAR_LABEL = KARGO_TITLE

/** The namespace picker's tooltip on the Kargo screen, which picks a project itself. */
export const KARGO_NAMESPACE_FILTER_TOOLTIP = 'Pick a Kargo project on this page. The namespace filter doesn’t apply here.'

export const KARGO_PROJECT_PARAM = 'project'
export const KARGO_STAGE_PARAM = 'stage'

/** The pipeline page for a project, scrolled to one Stage's card when stage is given. */
export function kargoPipelinePath(project: string, stage?: string): string {
  const params = new URLSearchParams({ [KARGO_PROJECT_PARAM]: project })
  if (stage) params.set(KARGO_STAGE_PARAM, stage)
  return `${KARGO_PATH}?${params.toString()}`
}

export function kargoDiscovered(apiResources: APIResource[] | undefined): boolean {
  return !!apiResources?.some((r) => r.group === KARGO_GROUP && r.kind === 'Stage')
}

/**
 * The project to show: the one in the URL when it is listed; else one whose
 * Stages deploy to a view namespace; else a view namespace that is a project;
 * else one with problems; else one with Stages; else the first. missing is the
 * URL's project when it isn't listed.
 */
export function chooseKargoProject(projects: KargoProjectSummary[], requested: string | null, viewNamespaces: string[]): { project?: string; missing?: string } {
  const missing = requested && !projects.some((p) => p.name === requested) ? requested : undefined
  if (requested && !missing) return { project: requested }
  const chosen =
    projects.find((p) => p.deployNamespaces?.some((ns) => viewNamespaces.includes(ns))) ??
    projects.find((p) => viewNamespaces.includes(p.name)) ??
    projects.find((p) => p.problems > 0) ??
    projects.find((p) => p.stages > 0) ??
    projects[0]
  return { project: chosen?.name, missing }
}
