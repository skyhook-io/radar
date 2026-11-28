import { useCallback, useMemo } from 'react'
import { useLocation } from 'react-router-dom'
import { KargoFreightRenderer as BaseKargoFreightRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/KargoFreightRenderer'
import { KargoPromotionRenderer as BaseKargoPromotionRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/KargoPromotionRenderer'
import { KargoStageRenderer as BaseKargoStageRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/KargoStageRenderer'
import { KargoWarehouseRenderer as BaseKargoWarehouseRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/KargoWarehouseRenderer'
import {
  isApiGroup,
  type KargoFreight,
  type ResourceRef,
} from '@skyhook-io/k8s-ui'
import type { ResourceHost } from '../../integrations/resourceHost'
import type { SelectedResource } from '../../types'
import { useKargoProject, type KargoProjectResponse } from '../../api/kargo'
import { useRadarFeature } from '../../api/client'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { KARGO_GROUP, KARGO_PATH, kargoPipelinePath } from './routes'

type KargoObject = {
  apiVersion?: string
  metadata?: { name?: string; namespace?: string }
  spec?: { freight?: string }
  status?: { freight?: { name?: string } }
}
type RendererProps = { data: KargoObject; onNavigate?: (ref: ResourceRef) => void }

const KIND_BY_PLURAL: Record<string, string> = {
  stages: 'Stage',
  freights: 'Freight',
  promotions: 'Promotion',
  warehouses: 'Warehouse',
}

/**
 * The project answer for the drawer's object, read through the Kargo
 * endpoint so every join is authorized per kind. Undefined while it loads, on
 * a Radar without the endpoint, or for an object of another group.
 */
function useProjectFor(data: KargoObject): KargoProjectResponse | undefined {
  const { support } = useRadarFeature('kargoWorkspace')
  const namespace = data?.metadata?.namespace
  const ours = isApiGroup(data?.apiVersion, KARGO_GROUP)
  const { data: project } = useKargoProject(namespace, { enabled: ours && support === 'supported' })
  return project && project.name === namespace ? project : undefined
}

/**
 * Opens the object's project on the pipeline page. Undefined on the pipeline
 * page itself, where the drawer already sits over it, on a Radar without the
 * page, and for an object of another group.
 */
function useOpenPipeline(data: KargoObject): ((stage?: string) => void) | undefined {
  const { support } = useRadarFeature('kargoWorkspace')
  const { pathname } = useLocation()
  const navigate = useWorkspaceNavigate()
  const project = data?.metadata?.namespace
  const open = useCallback((stage?: string) => { if (project) navigate(kargoPipelinePath(project, stage)) }, [navigate, project])
  const onPage = pathname === KARGO_PATH || pathname.startsWith(`${KARGO_PATH}/`)
  if (!project || onPage || support !== 'supported' || !isApiGroup(data?.apiVersion, KARGO_GROUP)) return undefined
  return open
}

/** Freight the caller can read, by name; undefined when Freight wasn't read. */
function freightByName(project: KargoProjectResponse | undefined): Record<string, KargoFreight> | undefined {
  if (!project?.freightRead) return undefined
  return Object.fromEntries(project.freight.map((f) => [f.metadata?.name ?? '', f]))
}

/**
 * The Warehouse's Freight in the answer (its newest) and how many it has in
 * all, so the drawer shows the real newest and counts the rest instead of
 * reading a partial list as the whole. Undefined when Freight wasn't read.
 */
export function warehouseFreights(project: KargoProjectResponse | undefined, warehouse: string): { freights: KargoFreight[]; total?: number } | undefined {
  if (!project?.freightRead) return undefined
  const freights = project.freight.filter((f) => f.origin?.name === warehouse && (f.origin?.kind ?? 'Warehouse') === 'Warehouse')
  const counts = project.freightByWarehouse
  return { freights, total: counts && Object.hasOwn(counts, warehouse) ? counts[warehouse] : undefined }
}

export function KargoStageRenderer({ data, onNavigate }: RendererProps) {
  const project = useProjectFor(data)
  const freights = useMemo(() => freightByName(project), [project])
  const onOpenPipeline = useOpenPipeline(data)
  return <BaseKargoStageRenderer data={data} onNavigate={onNavigate} freights={freights} onOpenPipeline={onOpenPipeline} />
}

export function KargoFreightRenderer({ data, onNavigate }: RendererProps) {
  const project = useProjectFor(data)
  const name: string = data?.metadata?.name ?? ''
  // Absent when Stages weren't read, so the section stays hidden rather than claiming none ran.
  const runs = project?.verificationRuns ? project.verificationRuns[name] ?? [] : undefined
  const onOpenPipeline = useOpenPipeline(data)
  return <BaseKargoFreightRenderer data={data} onNavigate={onNavigate} verificationRuns={runs} onOpenPipeline={onOpenPipeline} />
}

export function KargoPromotionRenderer({ data, onNavigate }: RendererProps) {
  const project = useProjectFor(data)
  const name: string | undefined = data?.status?.freight?.name ?? data?.spec?.freight
  const freight = project?.freightRead && name ? project.freight.find((f) => f.metadata?.name === name) : undefined
  const onOpenPipeline = useOpenPipeline(data)
  return <BaseKargoPromotionRenderer data={data} onNavigate={onNavigate} freight={freight} onOpenPipeline={onOpenPipeline} />
}

export function KargoWarehouseRenderer({ data, onNavigate }: RendererProps) {
  const project = useProjectFor(data)
  const name: string = data?.metadata?.name ?? ''
  const produced = useMemo(() => warehouseFreights(project, name), [project, name])
  return <BaseKargoWarehouseRenderer data={data} onNavigate={onNavigate} freights={produced?.freights} freightTotal={produced?.total} />
}

function KargoDrawerTrailBack({ resource }: { resource: SelectedResource }) {
  return <DrawerTrailBack resource={resource} pathPrefix={KARGO_PATH} kindLabel={(plural) => KIND_BY_PLURAL[plural]} />
}

/**
 * Kargo's drawers, wherever they open: the renderers read their own object,
 * and these wrappers add the Freight aliases, "since", verification runs and
 * a Warehouse's Freight from the Kargo endpoint.
 */
export const kargoResourceHost: ResourceHost = {
  id: 'kargo',
  feature: 'kargoWorkspace',
  renderers: { KargoStageRenderer, KargoFreightRenderer, KargoPromotionRenderer, KargoWarehouseRenderer },
  drawerNavigation: {
    ownsPath: (pathname) => pathname === KARGO_PATH || pathname.startsWith(`${KARGO_PATH}/`),
    Component: KargoDrawerTrailBack,
  },
}
