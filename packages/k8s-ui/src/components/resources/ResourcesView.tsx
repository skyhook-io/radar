import React, { useState, useMemo, useEffect, useCallback, useDeferredValue, useRef, useContext, useId } from 'react'
import { TableVirtuoso, type TableVirtuosoHandle } from 'react-virtuoso'
import { useDebouncedValue } from '../../hooks/useDebouncedValue'
import { PaneLoader } from '../ui/PaneLoader'
import { RestrictedState } from '../ui/RestrictedState'
import { Input } from '../ui/Input'
import type { TopPodMetrics, TopNodeMetrics, ContainerResourceMetrics } from '../../types'
import {
  Search,
  RefreshCw,
  AlertTriangle,
  Globe,
  Shield,
  ChevronDown,
  ChevronUp,
  ArrowUpDown,
  ListFilter,
  X,
  Columns3,
  RotateCcw,
  Trash2,
  Tag,
  Copy,
  Check,
  Plus,
  GitCompare,
  Regex,
  ListChecks,
  Minus,
  Scale,
} from 'lucide-react'
import { clsx } from 'clsx'
import { ResourceBar } from '../ui/ResourceBar'
import type { SelectedResource, APIResource } from '../../types'
import { isForbiddenError } from '../../types/fetch-error'
import type { NavigateToResource } from '../../utils/navigation'
import { categorizeResources, CORE_RESOURCES, findAPIResourceForRoute, isCoreBatchJob } from '../../utils/api-resources'
import { copyText } from '../../utils/clipboard'
import {
  getPodStatus,
  getPodRestarts,
  getPodProblems,
  getWorkloadDisplayStatus,
  workloadStatusLabel,
  getWorkloadProblems,
  workloadMatchesProblemCategory,
  getContainerSquareStates,
  getWorkloadImages,
  getWorkloadConditions,
  getReplicaSetOwner,
  isReplicaSetActive,
  getServiceStatus,
  getServicePorts,
  getServiceExternalIP,
  getServiceSelector,
  getServiceEndpointsStatus,
  getIngressHosts,
  getIngressClass,
  hasIngressTLS,
  getIngressAddress,
  getIngressRules,
  getConfigMapKeys,
  getConfigMapSize,
  getSecretType,
  getSecretKeyCount,
  getJobStatus,
  getJobCompletions,
  getJobDuration,
  getCronJobStatus,
  getCronJobSchedule,
  getCronJobLastRun,
  getHPAStatus,
  getHPAReplicas,
  getHPATarget,
  getHPAMetrics,
  getNodeStatus,
  getNodeRoles,
  getNodeConditions,
  getNodeTaints,
  getNodeVersion,
  getPVCStatus,
  getPVCCapacity,
  getPVCAccessModes,
  getAnalysisRunStatus,
  summarizeAnalysisMetrics,
  getRolloutStrategy,
  getRolloutReady,
  getRolloutStep,
  getWorkflowStatus,
  getWorkflowDuration,
  getWorkflowProgress,
  getWorkflowTemplate,
  getCronWorkflowStatus,
  getCronWorkflowSchedule,
  getCronWorkflowLastRun,
  getCronWorkflowTemplate,
  getPVStatus,
  getPVAccessModes,
  getPVClaim,
  getStorageClassProvisioner,
  getStorageClassReclaimPolicy,
  getStorageClassBindingMode,
  getStorageClassExpansion,
  getGatewayStatus,
  getGatewayClass,
  getGatewayListeners,
  getGatewayAttachedRoutes,
  getGatewayAddresses,
  getGatewayClassStatus,
  getGatewayClassController,
  getGatewayClassDescription,
  getRouteStatus,
  getRouteParents,
  getRouteHostnames,
  getRouteBackends,
  getRouteRulesCount,
  getSealedSecretStatus,
  getSealedSecretKeyCount,
  getWorkflowTemplateCount,
  getWorkflowTemplateEntrypoint,
  getNetworkPolicyTypes,
  getNetworkPolicyRuleCount,
  getNetworkPolicySelector,
  getPDBStatus,
  getPDBBudget,
  getPDBHealthy,
  getPDBAllowed,
  getServiceAccountAutomount,
  getServiceAccountSecretCount,
  getRoleRuleCount,
  formatAge,
  formatDuration,
  truncate,
  getCellFilterValue,
  parseColumnFilters,
  serializeColumnFilters,
  parseColumnFilterExcludes,
  podMatchesProblemCategory,
  SEVERITY_DOT_COLOR,
  healthColors,
} from './resource-utils'
import { getGenericResourceStatus } from './generic-status'
import { getIstioGatewayServerCount, getIstioGatewaySelectorString } from './resource-utils-istio'
import {
  type PrinterColumnDef, type PrinterTable, PRINTER_COLUMN_PREFIX, formatPrinterCell, printerCellSortValue,
  printerCellTone, printerColumnKey, printerColumnWidth, printerTableKey, printerTableMatchesKind, readPrinterCell,
} from './printer-columns'
import { SEVERITY_BADGE, EVENT_TYPE_COLORS } from '../../utils/badge-colors'
import { pluralize } from '../../utils/pluralize'
import { getPodGpuCount, getNodeGpuCount } from '../../utils/extended-resources'
import { parseQuantityToNumber } from '../../utils/format'
import { type CustomColumnDef, type CustomColumnSource, customColumnKey, readCustomColumnValue, sanitizeCustomColumnDefs } from '../../utils/custom-columns'
import { isRolloutActivityVisible } from '../../utils/workload-rollout'
import { FreshnessControl, type FreshnessConnection } from '../ui/FreshnessControl'
import { Tooltip } from '../ui/Tooltip'
import { AuditBadgeTooltip, type AuditBadgeMessage } from '../audit/AuditBadgeTooltip'
import { SEVERITY_TEXT_CLASS } from '../checks/severity'
// CRD-specific cell components (extracted)
import { GitRepositoryCell, OCIRepositoryCell, HelmRepositoryCell, KustomizationCell, FluxHelmReleaseCell, FluxAlertCell } from './renderers/flux-cells'
import { ArgoApplicationCell, ArgoApplicationSetCell, ArgoAppProjectCell } from './renderers/argo-cells'
import { VulnerabilityReportCell, ConfigAuditReportCell, ExposedSecretReportCell, RbacAssessmentReportCell, ClusterComplianceReportCell, SbomReportCell } from './renderers/trivy-cells'
import { CertificateCell, CertificateRequestCell, ClusterIssuerCell, IssuerCell, OrderCell, ChallengeCell } from './renderers/certmanager-cells'
import { NodePoolCell, NodeClaimCell, EC2NodeClassCell } from './renderers/karpenter-cells'
import { ScaledObjectCell, ScaledJobCell, TriggerAuthenticationCell, ClusterTriggerAuthenticationCell } from './renderers/keda-cells'
import { ResourceClaimCell, ResourceClaimTemplateCell, DeviceClassCell, ResourceSliceCell } from './renderers/dra-cells'
import { NvidiaClusterPolicyCell, NvidiaDriverCell } from './renderers/nvidia-cells'
import { ClusterQueueCell, LocalQueueCell, KueueWorkloadCell, ResourceFlavorCell, AdmissionCheckCell, ProvisioningRequestCell } from './renderers/kueue-cells'
import { RayClusterCell, RayJobCell, RayServiceCell, RayCronJobCell } from './renderers/ray-cells'
import { LeaderWorkerSetCell, JobSetCell } from './renderers/jobset-lws-cells'
import { getJobSetReadyJobs, getJobSetRestarts } from './resource-utils-jobset-lws'
import { InferenceServiceCell, ServingRuntimeCell, InferenceGraphCell, TrainedModelCell, LLMInferenceServiceCell } from './renderers/kserve-cells'
import { InferencePoolCell, InferenceObjectiveCell } from './renderers/inference-gateway-cells'
import { VolcanoJobCell, VolcanoQueueCell, VolcanoPodGroupCell, JobFlowCell, JobTemplateCell } from './renderers/volcano-cells'
import { KaiQueueCell, KaiPodGroupCell } from './renderers/kai-cells'
import { KaitoWorkspaceCell, RAGEngineCell } from './renderers/kaito-cells'
import { NIMServiceCell, NIMCacheCell, NIMPipelineCell } from './renderers/nim-cells'
import { AMDDeviceConfigCell } from './renderers/amd-gpu-cells'
import { PyTorchJobCell, TFJobCell, MPIJobCell, TrainJobCell } from './renderers/kubeflow-training-cells'
import { ServiceMonitorCell, PrometheusRuleCell, PodMonitorCell } from './renderers/prometheus-cells'
import { PolicyReportCell, ClusterPolicyReportCell, KyvernoPolicyCell, ClusterPolicyCell,
  KyvernoUpdateRequestCell,
  KyvernoEphemeralReportCell,
} from './renderers/kyverno-cells'
import { KyvernoModernPolicyCell, KyvernoPolicyExceptionCell, KyvernoCleanupPolicyCell } from './renderers/kyverno-modern-cells'
import { KYVERNO_MODERN_PLURALS, isModernKyvernoPolicy } from './resource-utils-kyverno-modern'
import { isAnyKyvernoPolicyException } from './resource-utils-kyverno-exceptions'
import { ExternalSecretCell, ClusterExternalSecretCell, SecretStoreCell, ClusterSecretStoreCell } from './renderers/eso-cells'
import { BackupCell, RestoreCell, ScheduleCell, BackupStorageLocationCell, VolumeSnapshotLocationCell, BackupRepositoryCell } from './renderers/velero-cells'
import { isVeleroResource } from './resource-utils-velero'
import { CNPGClusterCell, CNPGBackupCell, CNPGScheduledBackupCell, CNPGPoolerCell, CNPGObjectStoreCell, CNPGDeclarativeCell, CNPGImageCatalogCell } from './renderers/cnpg-cells'
import { isApiGroup, CNPG_GROUP } from './resource-utils-cnpg'
import { ManagedResourceCell, CompositeResourceCell, CrossplaneProviderCell, CrossplaneProviderConfigCell, CompositionCell, XRDCell } from './renderers/crossplane-cells'
import { isManagedResource, isComposite } from './resource-utils-crossplane'
import { VirtualServiceCell, DestinationRuleCell, IstioGatewayCell, ServiceEntryCell, PeerAuthenticationCell, AuthorizationPolicyCell } from './renderers/istio-cells'
import { KnativeServiceCell, ConfigurationCell as KnativeConfigurationCell, RevisionCell as KnativeRevisionCell, RouteCell as KnativeRouteCell, BrokerCell, TriggerCell, EventTypeCell, PingSourceCell, ApiServerSourceCell, ContainerSourceCell, SinkBindingCell, ChannelCell, InMemoryChannelCell, SubscriptionCell, SequenceCell, ParallelCell, DomainMappingCell, ServerlessServiceCell, KnativeIngressCell, KnativeCertificateCell } from './renderers/knative-cells'
import { IngressRouteCell, MiddlewareCell, TraefikServiceCell, ServersTransportCell, TLSOptionCell } from './renderers/traefik-cells'
import { HTTPProxyCell } from './renderers/contour-cells'
import { CAPIClusterCell, CAPIMachineCell, CAPIMachineDeploymentCell, CAPIMachineSetCell, CAPIMachinePoolCell, CAPIKubeadmControlPlaneCell, CAPIClusterClassCell, CAPIMachineHealthCheckCell } from './renderers/capi-cells'
import { AWSManagedControlPlaneCell, AWSManagedMachinePoolCell, AWSMachineCell, AWSMachineTemplateCell, AWSManagedClusterCell } from './renderers/aws-capi-cells'
import { GCPManagedControlPlaneCell, GCPManagedMachinePoolCell, GCPMachineCell, GCPMachineTemplateCell, GCPManagedClusterCell } from './renderers/gcp-capi-cells'
import { AzureManagedControlPlaneCell, AzureManagedMachinePoolCell, AzureMachineCell, AzureMachineTemplateCell, AzureManagedClusterCell } from './renderers/azure-capi-cells'
import { CalicoInfraCell, CalicoPolicyCell } from './renderers/calico-cells'
import { isCalicoPolicyResource, isCoreNetworkPolicyKind } from './resource-utils-calico'
import { useRegisterShortcut, useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'
import { ResourcesSidebar } from './ResourcesSidebar'
import type { SelectedKindInfo } from './ResourcesSidebar'
import { CompareTray, togglePick, pickIndex, refToParam, SIDE_TONES, type CompareTrayPick, type NamespacedRef } from '../compare'
import { ConfirmDialog } from '../ui/ConfirmDialog'

// Pod problem filter options (special multi-select, not a single column value)
const POD_PROBLEMS = ['CrashLoopBackOff', 'ImagePullBackOff', 'OOMKilled', 'Unschedulable', 'Not Ready', 'High Restarts', 'Init Failed', 'Exit Code Error', 'Failed', 'Other'] as const
const WORKLOAD_PROBLEMS = ['Unavailable', 'Rollout Stuck', 'Rollout In Progress'] as const
const WORKLOAD_KINDS = new Set(['deployments', 'statefulsets', 'daemonsets'])
const BULK_RESTART_WORKLOAD_KINDS = new Set(['deployments', 'statefulsets', 'daemonsets', 'rollouts'])
const BULK_SCALE_WORKLOAD_KINDS = new Set(['deployments', 'statefulsets'])

// Columns to skip for auto-detected filters (high cardinality, text-like, or non-filterable)
export const SKIP_FILTER_COLUMNS = new Set([
  'name', 'age', 'keys', 'size', 'images', 'domains', 'hosts', 'rules',
  'ports', 'message', 'url', 'ref', 'revision', 'path', 'selector', 'ready', 'restarts',
  'completions', 'duration', 'schedule', 'lastRun', 'target', 'replicas', 'metrics',
  'capacity', 'accessModes', 'volume', 'step', 'progress', 'template', 'expires',
  'issuer', 'domain', 'presented', 'listeners', 'routes', 'addresses', 'hostnames',
  'parents', 'backends', 'controller', 'description', 'externalIP', 'address',
  'conditions', 'taints', 'desired', 'upToDate', 'available', 'owner',
  'tls', 'endpoints', 'object', 'count', 'lastSeen', 'source', 'inventory',
  'lastUpdated', 'chart', 'events', 'repo',
  'generators', 'applications', 'destinations', 'sources', 'budget', 'healthy', 'allowed',
  'secrets', 'subjects', 'role', 'entrypoint', 'templates',
])

// Namespace/node cardinality scales with cluster size, not kind enum size;
// node keeps a generous cap as a sanity bound, namespace is uncapped.
export function isColumnFilterableByDistinctCount(colKey: string, distinctCount: number): boolean {
  if (SKIP_FILTER_COLUMNS.has(colKey)) return false
  const maxDistinct = colKey === 'namespace'
    ? Number.POSITIVE_INFINITY
    : colKey === 'node' ? 200 : 30
  return distinctCount >= 2 && distinctCount <= maxDistinct
}

// Column definitions per resource kind
export interface Column {
  key: string
  label: string
  width?: string
  hideOnMobile?: boolean
  tooltip?: string // Explanation of what this column means
  defaultVisible?: boolean // false = hidden by default, shown via column picker
  defaultWidth?: number // default width in px (used for resizable columns)
  minWidth?: number // minimum width in px
}

type BulkResourceItem = { kind: string; group?: string; namespace: string; name: string }

/**
 * Extra column injected by the parent — for example, a leading "Cluster"
 * column when the table is rendered inside a multi-cluster host.
 * Self-contained: carries its own render/sort/filter functions so the
 * host code doesn't need to extend KNOWN_COLUMNS or the per-kind cell
 * renderers.
 *
 * When extraLeadingColumns is undefined or empty (the standard
 * single-cluster path), the rest of ResourcesView's behavior is
 * byte-identical to today.
 *
 * Keys must NOT collide with existing KNOWN_COLUMNS keys (name,
 * namespace, age, status, ready, etc.) — collisions silently bypass
 * the extra and fall through to the built-in cell.
 */
export interface ExtraColumn extends Column {
  /** Cell content for this column. Receives the resource row. */
  render: (resource: any) => React.ReactNode
  /** Sort key extractor. Falls back to localeCompare on render output. */
  getSortValue?: (resource: any) => string | number
  /** Filter value extractor used by the column-filter dropdown's
   *  unique-values pull. Falls back to row not being filterable. */
  getFilterValue?: (resource: any) => string
}

// Materializes a custom-column def into a self-contained ExtraColumn so it
// rides the existing render/sort/filter override rails (extraColumnsByKey).
// The pure parts (key encoding, value read, load-boundary sanitizer) live in
// utils/custom-columns so they're unit-tested; only the JSX render stays here.
function buildCustomColumn(d: CustomColumnDef): ExtraColumn {
  return {
    key: customColumnKey(d),
    label: d.path,
    width: 'w-40',
    defaultVisible: true,
    tooltip: d.source === 'label' ? `Label: ${d.path}` : `Annotation: ${d.path}`,
    render: (resource: any) => {
      const v = readCustomColumnValue(resource, d)
      if (!v) return <span className="text-sm text-theme-text-secondary">-</span>
      return (
        <Tooltip content={v}>
          <span className="text-sm text-theme-text-secondary truncate block">{v}</span>
        </Tooltip>
      )
    },
    getSortValue: (resource: any) => readCustomColumnValue(resource, d),
    getFilterValue: (resource: any) => readCustomColumnValue(resource, d),
  }
}

// Tailwind width class → pixel minimum mapping for CSS Grid column sizing
// A width class missing from this map falls back to 80px, and nothing says so —
// the column just renders narrow enough to truncate its header. Keep the whole
// Tailwind step range present rather than only the sizes in use today.
const TAILWIND_WIDTH_TO_PX: Record<string, number> = {
  'w-12': 48, 'w-14': 56, 'w-16': 64, 'w-20': 80, 'w-24': 96,
  'w-28': 112, 'w-32': 128, 'w-36': 144, 'w-40': 160, 'w-44': 176,
  'w-48': 192, 'w-52': 208, 'w-56': 224, 'w-60': 240, 'w-64': 256,
  'w-72': 288, 'w-80': 320, 'w-96': 384,
}

const COMPARE_COLUMN_WIDTH = 36
const COMPARE_COLUMN_STYLE: React.CSSProperties = {
  width: COMPARE_COLUMN_WIDTH,
  minWidth: COMPARE_COLUMN_WIDTH,
  maxWidth: COMPARE_COLUMN_WIDTH,
}

// Built-in sortable keys. Hoisted so getColumnMinWidth can reserve room for the
// sort icon without the header render and the width math drifting apart.
const BUILTIN_SORTABLE_COLUMN_KEYS = new Set([
  'name', 'namespace', 'age', 'status', 'ready', 'restarts', 'type', 'version',
  'desired', 'available', 'upToDate', 'lastSeen', 'count', 'reason', 'object',
  'cpu', 'memory', 'containers',
])

// The header row is px-4 with a `truncate` label, under table-layout:fixed with
// explicit <col> widths — so a column narrower than its own heading clips to
// "ADDRESS T…" and stays clipped at every viewport size, because only the Name
// column flexes. Deriving a floor from the label keeps a declared w-* from
// being narrower than the word it has to show.
const HEADER_PADDING_PX = 32 // px-4 on both sides
const HEADER_SORT_AFFORDANCE_PX = 20 // gap-1 + the w-3.5 chevron / ArrowUpDown
const HEADER_FILTER_AFFORDANCE_PX = 20 // gap-1 + the p-0.5 filter button
// Deliberately an upper bound on the rendered header font (DM Sans 500, 12px,
// uppercase, tracking-wide): measured per-character widths across the curated
// labels ranged 7.2–8.1px, so 8.2 never under-reserves. Over-reserving costs
// space the flexible Name column has to spare; under-reserving clips again.
// Printer and host-extra columns take their label from vendor CRD text, which
// can be long or non-Latin. Cap the floor so one such label can't size a column
// off the screen; past this a truncated heading (with its tooltip) is better.
const HEADER_FLOOR_MAX_PX = 320

// Must match the rendered header: text-xs (12px) / font-medium (500) / uppercase
// / tracking-wide, in the app font. measureText does not apply letter-spacing,
// so it is added per gap.
const HEADER_FONT = "500 12px 'DM Sans Variable', 'DM Sans', system-ui, sans-serif"
const HEADER_LETTER_SPACING_PX = 0.3 // tracking-wide = 0.025em at 12px
// Fallback only — used where there is no canvas (SSR, jsdom). Deliberately an
// upper bound: measured per-character widths across the curated labels ranged
// 7.2–8.1px, so this never under-reserves for Latin text.
const HEADER_CHAR_PX_FALLBACK = 8.2
const HEADER_SPACE_PX_FALLBACK = 4

let headerMeasureCtx: CanvasRenderingContext2D | null | undefined
const headerLabelWidthCache = new Map<string, number>()

// DM Sans is loaded with font-display:swap, so the first tables can render — and
// be measured — while the browser is still painting the fallback face. Those
// measurements would otherwise be cached against the wrong glyph metrics for the
// rest of the session: a wider real face clips a label whose tooltip never
// activates, a narrower one leaves the table permanently too wide. Drop the
// cache when the real font arrives and let subscribers recompute.
let headerFontEpoch = 0
const headerFontListeners = new Set<() => void>()
let headerFontWatchStarted = false

function watchHeaderFont(): void {
  if (headerFontWatchStarted) return
  headerFontWatchStarted = true
  const fonts = typeof document === 'undefined' ? undefined : (document as Document & { fonts?: FontFaceSet }).fonts
  if (!fonts?.ready) return
  void fonts.ready.then(() => {
    headerLabelWidthCache.clear()
    headerMeasureCtx = undefined // re-created so ctx.font resolves against the loaded face
    headerFontEpoch++
    for (const notify of headerFontListeners) notify()
  })
}

/**
 * Re-renders once the web font has loaded, so column widths measured against the
 * fallback face are recomputed against the real one.
 */
export function useHeaderFontEpoch(): number {
  const [epoch, setEpoch] = useState(headerFontEpoch)
  useEffect(() => {
    const notify = () => setEpoch(headerFontEpoch)
    headerFontListeners.add(notify)
    notify() // the font may have loaded between render and effect
    return () => { headerFontListeners.delete(notify) }
  }, [])
  return epoch
}

/**
 * Width of a header label as the browser will actually draw it.
 *
 * Measured rather than estimated because the labels are not a closed set: an
 * uncurated CRD's printer columns take their names from the vendor's own
 * additionalPrinterColumns, so they can be any length, any script, and a
 * per-character constant calibrated on curated ASCII silently stops holding.
 */
function headerLabelWidth(label: string): number {
  watchHeaderFont()
  const cached = headerLabelWidthCache.get(label)
  if (cached !== undefined) return cached
  const upper = label.toUpperCase() // the header renders uppercase
  let width: number
  if (headerMeasureCtx === undefined) {
    headerMeasureCtx = typeof document === 'undefined'
      ? null
      : document.createElement('canvas').getContext('2d')
    if (headerMeasureCtx) headerMeasureCtx.font = HEADER_FONT
  }
  if (headerMeasureCtx) {
    width = headerMeasureCtx.measureText(upper).width
      + HEADER_LETTER_SPACING_PX * Math.max(0, upper.length - 1)
  } else {
    width = 0
    for (const ch of upper) width += ch === ' ' ? HEADER_SPACE_PX_FALLBACK : HEADER_CHAR_PX_FALLBACK
  }
  headerLabelWidthCache.set(label, width)
  return width
}

/**
 * Header label that carries its own full text in a tooltip when it truncates.
 *
 * The width floor in getColumnMinWidth keeps a heading from being clipped by its
 * own column, but three cases still truncate: a label past the floor cap, a
 * column the user dragged narrower, and the sort/filter controls sharing the
 * row — whose width depends on their state (an active filter renders padding,
 * an icon and a count). Predicting those widths was wrong twice, so this reads
 * the actual overflow instead of computing it.
 */
function HeaderLabel({ label, className }: { label: string; className?: string }) {
  const ref = useRef<HTMLSpanElement>(null)
  const [truncated, setTruncated] = useState(false)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const check = () => setTruncated(el.scrollWidth > el.clientWidth + 0.5)
    check()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(check)
    ro.observe(el)
    return () => { ro.disconnect() }
  }, [label])
  const span = <span ref={ref} className={clsx('truncate', className)}>{label}</span>
  return truncated ? <Tooltip content={label}>{span}</Tooltip> : span
}

/**
 * Whether a column's header renders a sort control, which takes width beside the
 * label. Shared by the header render and the width math so the two can't
 * disagree: printer and custom columns are sortable through their own
 * getSortValue even though their keys aren't in the built-in list.
 */
export function isColumnSortable(col: Column, extras?: Map<string, ExtraColumn>): boolean {
  return BUILTIN_SORTABLE_COLUMN_KEYS.has(col.key) || !!extras?.get(col.key)?.getSortValue
}

export function getColumnMinWidth(col: Column, extras?: Map<string, ExtraColumn>): number {
  const declared = declaredColumnWidth(col)
  // A column-filter button also sits in this row, but whether one renders
  // depends on the distinct values in the data (isColumnFilterableByDistinctCount),
  // which isn't knowable here — those columns stay ~20px tighter than ideal.
  // Both controls are reserved statically. The filter button only renders once
  // the data has a filterable spread of values (isColumnFilterableByDistinctCount),
  // but sizing the column from that would make it change width as rows load or
  // are filtered — the jumping-column problem fixed layout exists to avoid. So
  // reserve for any column that could ever carry one, and keep the width stable.
  const affordance = (isColumnSortable(col, extras) ? HEADER_SORT_AFFORDANCE_PX : 0)
    + (SKIP_FILTER_COLUMNS.has(col.key) ? 0 : HEADER_FILTER_AFFORDANCE_PX)
  const headerFloor = HEADER_PADDING_PX + headerLabelWidth(col.label) + affordance
  return Math.max(declared, Math.min(Math.ceil(headerFloor), HEADER_FLOOR_MAX_PX))
}

function declaredColumnWidth(col: Column): number {
  if (col.minWidth) return col.minWidth
  if (!col.width) return 200 // Name column (no width class) gets wider minimum
  const match = col.width.match(/(?:min-)?w-(\d+)/)
  if (match) return TAILWIND_WIDTH_TO_PX[`w-${match[1]}`] || 80
  return 80
}

// Default columns for unknown resource types (CRDs)
/**
 * Why a list of this kind being empty is not the answer it looks like.
 *
 * Kyverno's two working records are garbage-collected within seconds of
 * finishing, so "none" is the resting state of a healthy cluster — and for
 * UpdateRequest it is ALSO what you see when a generate rule never fired at
 * all. Those are opposite conclusions and a bare "No X found" renders them
 * identically.
 */
export function emptyKindNote(kind: string, group?: string, unfilteredTotal = 0): string | undefined {
  // A search or column filter emptying the table is the reader's own doing.
  // Explaining that none is the resting state then answers a question they did
  // not ask, while their own term is still in the box.
  if (unfilteredTotal > 0) return undefined
  const g = group ?? ''
  if (kind.toLowerCase() === 'updaterequest' && g === 'kyverno.io') {
    return 'These are deleted seconds after the work completes, so none is the normal resting state. It also looks like this if a generate or mutate-existing rule never ran — check the policy itself to tell the two apart.'
  }
  if ((kind.toLowerCase() === 'ephemeralreport' || kind.toLowerCase() === 'clusterephemeralreport') && g === 'reports.kyverno.io') {
    return 'These exist only between a background scan and the PolicyReport it becomes, so none means the scan has finished rather than that nothing was checked.'
  }
  return undefined
}

const DEFAULT_COLUMNS: Column[] = [
  { key: 'name', label: 'Name' },
  { key: 'namespace', label: 'Namespace', width: 'w-48' },
  { key: 'status', label: 'Status', width: 'w-28' },
  { key: 'age', label: 'Age', width: 'w-24' },
]

const KNOWN_COLUMNS: Record<string, Column[]> = {
  pods: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'containers', label: 'Containers', width: 'w-32', tooltip: 'Container readiness; hover each square for name and state' },
    { key: 'status', label: 'Status', width: 'w-40' },
    { key: 'cpu', label: 'CPU', width: 'w-40', tooltip: 'CPU usage / limit (marker = request)' },
    { key: 'memory', label: 'Memory', width: 'w-40', tooltip: 'Memory usage / limit (marker = request)' },
    { key: 'restarts', label: 'Restarts', width: 'w-28' },
    { key: 'gpu', label: 'GPU', width: 'w-20', tooltip: 'Effective GPU request (requests, falling back to limits) summed across containers', defaultVisible: false },
    { key: 'podIP', label: 'Pod IP', width: 'w-32', defaultVisible: false },
    { key: 'node', label: 'Node', width: 'w-44', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  deployments: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-40' },
    { key: 'ready', label: 'Ready', width: 'w-24', tooltip: 'Ready pods / Desired replicas' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-32', hideOnMobile: true, tooltip: 'Number of pods running the current pod template' },
    { key: 'available', label: 'Available', width: 'w-28', hideOnMobile: true, tooltip: 'Number of pods available (ready for minReadySeconds)' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  daemonsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-40' },
    { key: 'desired', label: 'Desired', width: 'w-20', tooltip: 'Number of nodes that should run the daemon pod (based on node selector)' },
    { key: 'ready', label: 'Ready', width: 'w-20', tooltip: 'Number of pods that are ready (passing readiness probes)' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-32', hideOnMobile: true, tooltip: 'Number of pods running the current pod template spec' },
    { key: 'available', label: 'Available', width: 'w-28', hideOnMobile: true, tooltip: 'Number of pods available (ready for minReadySeconds duration)' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  statefulsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-40' },
    { key: 'ready', label: 'Ready', width: 'w-24', tooltip: 'Ready pods / Desired replicas' },
    { key: 'upToDate', label: 'Up-to-date', width: 'w-32', hideOnMobile: true, tooltip: 'Number of pods running the current pod template' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  replicasets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'ready', label: 'Ready', width: 'w-24' },
    { key: 'owner', label: 'Owner', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  services: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'type', label: 'Type', width: 'w-28' },
    { key: 'selector', label: 'Selector', width: 'w-48', hideOnMobile: true },
    { key: 'endpoints', label: 'Endpoints', width: 'w-24' },
    { key: 'ports', label: 'Ports', width: 'w-40' },
    { key: 'externalIP', label: 'External', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  endpointslices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'service', label: 'Service', width: 'w-48' },
    { key: 'addressType', label: 'Address Type', width: 'w-28' },
    { key: 'endpoints', label: 'Endpoints', width: 'w-32' },
    { key: 'addresses', label: 'Addresses', width: 'w-24' },
    { key: 'ports', label: 'Ports', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  ingresses: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'class', label: 'Class', width: 'w-24 shrink-0' },
    { key: 'hosts', label: 'Hosts', width: 'min-w-48' },
    { key: 'rules', label: 'Rules', width: 'min-w-56', hideOnMobile: true },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'address', label: 'Address', width: 'min-w-32', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  nodes: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-44' },
    { key: 'roles', label: 'Roles', width: 'w-28' },
    { key: 'cpu', label: 'CPU', width: 'w-40', tooltip: 'Current CPU usage / allocatable' },
    { key: 'memory', label: 'Memory', width: 'w-40', tooltip: 'Current memory usage / allocatable' },
    { key: 'pods', label: 'Pods', width: 'w-28', tooltip: 'Pods running / allocatable' },
    { key: 'gpu', label: 'GPUs', width: 'w-20', tooltip: 'Allocatable GPUs', defaultVisible: false },
    { key: 'conditions', label: 'Conditions', width: 'w-40', hideOnMobile: true },
    { key: 'taints', label: 'Taints', width: 'w-24', hideOnMobile: true },
    { key: 'version', label: 'Version', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  configmaps: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'keys', label: 'Keys', width: 'w-48' },
    { key: 'size', label: 'Size', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  secrets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'type', label: 'Type', width: 'w-28' },
    { key: 'keys', label: 'Keys', width: 'w-20' },
    { key: 'expires', label: 'Expires', width: 'w-24', tooltip: 'Certificate expiry for TLS secrets' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  jobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'completions', label: 'Completions', width: 'w-32' },
    { key: 'duration', label: 'Duration', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cronjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'schedule', label: 'Schedule', width: 'w-40' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'lastRun', label: 'Last Run', width: 'w-28', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  hpas: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'target', label: 'Target', width: 'w-48' },
    { key: 'replicas', label: 'Replicas', width: 'w-32' },
    { key: 'metrics', label: 'Metrics', width: 'w-36', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  horizontalpodautoscalers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'target', label: 'Target', width: 'w-48' },
    { key: 'replicas', label: 'Replicas', width: 'w-32' },
    { key: 'metrics', label: 'Metrics', width: 'w-36', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  persistentvolumeclaims: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'capacity', label: 'Capacity', width: 'w-24' },
    { key: 'storageClass', label: 'Storage Class', width: 'w-40', hideOnMobile: true },
    { key: 'accessModes', label: 'Access', width: 'w-20', tooltip: 'Access modes: RWO=ReadWriteOnce, RWX=ReadWriteMany, ROX=ReadOnlyMany' },
    { key: 'volume', label: 'Volume', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  rollouts: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-32' },
    { key: 'ready', label: 'Ready', width: 'w-24', tooltip: 'Available / Desired replicas' },
    { key: 'strategy', label: 'Strategy', width: 'w-24' },
    { key: 'step', label: 'Step', width: 'w-20', hideOnMobile: true, tooltip: 'Current canary step / Total steps' },
    { key: 'images', label: 'Images', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  analysisruns: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Phase', width: 'w-28' },
    { key: 'trigger', label: 'Trigger', width: 'w-32', tooltip: 'Which part of the Rollout started this run' },
    { key: 'metrics', label: 'Metrics', width: 'w-40', hideOnMobile: true, tooltip: 'Per-metric verdicts' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  workflows: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Phase', width: 'w-28' },
    { key: 'duration', label: 'Duration', width: 'w-24' },
    { key: 'progress', label: 'Progress', width: 'w-24', hideOnMobile: true, tooltip: 'Succeeded steps / Total steps' },
    { key: 'template', label: 'Template', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cronworkflows: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'schedule', label: 'Schedule', width: 'w-40' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'lastRun', label: 'Last Run', width: 'w-28', hideOnMobile: true },
    { key: 'template', label: 'Template', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  certificates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Ready', width: 'w-24' },
    { key: 'domains', label: 'Domains', width: 'w-56' },
    { key: 'issuer', label: 'Issuer', width: 'w-36', hideOnMobile: true },
    { key: 'expires', label: 'Expires', width: 'w-24', tooltip: 'Days until certificate expires' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  persistentvolumes: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'capacity', label: 'Capacity', width: 'w-24' },
    { key: 'accessModes', label: 'Access', width: 'w-20', tooltip: 'RWO=ReadWriteOnce, ROX=ReadOnlyMany, RWX=ReadWriteMany' },
    { key: 'reclaimPolicy', label: 'Reclaim', width: 'w-20' },
    { key: 'storageClass', label: 'Storage Class', width: 'w-40', hideOnMobile: true },
    { key: 'claim', label: 'Claim', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  storageclasses: [
    { key: 'name', label: 'Name' },
    { key: 'provisioner', label: 'Provisioner', width: 'w-48' },
    { key: 'reclaimPolicy', label: 'Reclaim', width: 'w-20' },
    { key: 'bindingMode', label: 'Binding Mode', width: 'w-36' },
    { key: 'expansion', label: 'Expansion', width: 'w-24', tooltip: 'Whether volumes can be expanded after creation' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  certificaterequests: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'issuer', label: 'Issuer', width: 'w-36' },
    { key: 'approved', label: 'Approved', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterissuers: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Ready', width: 'w-24' },
    { key: 'issuerType', label: 'Type', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  issuers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Ready', width: 'w-24' },
    { key: 'issuerType', label: 'Type', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  orders: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'state', label: 'State', width: 'w-24' },
    { key: 'domains', label: 'Domains', width: 'w-48' },
    { key: 'issuer', label: 'Issuer', width: 'w-36', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  challenges: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'challengeType', label: 'Type', width: 'w-20' },
    { key: 'state', label: 'State', width: 'w-24' },
    { key: 'domain', label: 'Domain', width: 'w-48' },
    { key: 'presented', label: 'Presented', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  gateways: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'class', label: 'Class', width: 'w-36' },
    { key: 'listeners', label: 'Listeners', width: 'w-40', tooltip: 'Protocol:Port for each listener' },
    { key: 'routes', label: 'Routes', width: 'w-20', tooltip: 'Total attached routes across all listeners' },
    { key: 'addresses', label: 'Addresses', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  httproutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-20', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  istiogateways: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'servers', label: 'Servers', width: 'w-24' },
    { key: 'selector', label: 'Selector', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  gatewayclasses: [
    { key: 'name', label: 'Name' },
    { key: 'controller', label: 'Controller', width: 'w-64', tooltip: 'Gateway controller implementation (spec.controllerName)' },
    { key: 'description', label: 'Description', width: 'w-64', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  nodepools: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'nodeClass', label: 'Node Class', width: 'w-36' },
    { key: 'limits', label: 'Limits', width: 'w-36', tooltip: 'CPU and memory limits' },
    { key: 'disruption', label: 'Disruption', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  nodeclaims: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'instanceType', label: 'Instance Type', width: 'w-32' },
    { key: 'capacityType', label: 'Capacity', width: 'w-24', tooltip: 'Spot or On-Demand' },
    { key: 'zone', label: 'Zone', width: 'w-28', hideOnMobile: true },
    { key: 'nodePool', label: 'Node Pool', width: 'w-32' },
    { key: 'nodeName', label: 'Node', width: 'w-40', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  ec2nodeclasses: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'ami', label: 'AMI', width: 'w-36', tooltip: 'AMI selector alias or ID' },
    { key: 'role', label: 'IAM Role', width: 'w-48' },
    { key: 'volumeSize', label: 'Volume', width: 'w-24', tooltip: 'Root volume size' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  scaledobjects: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'target', label: 'Target', width: 'w-48', tooltip: 'Scale target workload' },
    { key: 'replicas', label: 'Replicas', width: 'w-28', tooltip: 'Min-Max replica range' },
    { key: 'triggerTypes', label: 'Trigger Types', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  resourceclaims: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28', tooltip: 'Allocated + reserved, allocated-but-unreserved, or pending' },
    { key: 'deviceClass', label: 'Device Class', width: 'w-44' },
    { key: 'allocated', label: 'Allocated Driver', width: 'w-44' },
    { key: 'reservedFor', label: 'Reserved For', width: 'w-44' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  resourceclaimtemplates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'deviceClass', label: 'Device Class', width: 'w-44' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  deviceclasses: [
    { key: 'name', label: 'Name' },
    { key: 'selectors', label: 'Selectors', width: 'w-28', tooltip: 'CEL device selector count' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  resourceslices: [
    { key: 'name', label: 'Name' },
    { key: 'driver', label: 'Driver', width: 'w-44' },
    { key: 'pool', label: 'Pool', width: 'w-36' },
    { key: 'node', label: 'Node', width: 'w-44' },
    { key: 'devices', label: 'Devices', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  nvidiaclusterpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'components', label: 'Components', width: 'w-64', tooltip: 'Enabled GPU Operator components' },
    { key: 'mig', label: 'MIG', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  nvidiadrivers: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'driverType', label: 'Type', width: 'w-28' },
    { key: 'version', label: 'Version', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterqueues: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'cohort', label: 'Cohort', width: 'w-32' },
    { key: 'pendingWorkloads', label: 'Pending', width: 'w-24', tooltip: 'Pending workloads' },
    { key: 'admittedWorkloads', label: 'Admitted', width: 'w-24', tooltip: 'Admitted workloads' },
    { key: 'flavors', label: 'Flavors', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  localqueues: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'clusterQueue', label: 'Cluster Queue', width: 'w-40' },
    { key: 'pendingWorkloads', label: 'Pending', width: 'w-24' },
    { key: 'admittedWorkloads', label: 'Admitted', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  workloads: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'queueName', label: 'Queue', width: 'w-36' },
    { key: 'admittedBy', label: 'Admitted By', width: 'w-36', tooltip: 'Admitting ClusterQueue' },
    { key: 'priority', label: 'Priority', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  resourceflavors: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'nodeLabels', label: 'Node Labels', width: 'w-28', tooltip: 'Node label selector count' },
    { key: 'taints', label: 'Taints', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  admissionchecks: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'controllerName', label: 'Controller', width: 'w-64' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  provisioningrequests: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'provisioningClassName', label: 'Class', width: 'w-56' },
    { key: 'podSets', label: 'Pod Sets', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  rayclusters: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'rayVersion', label: 'Ray Version', width: 'w-28' },
    { key: 'workers', label: 'Workers', width: 'w-24', tooltip: 'Available/desired worker replicas' },
    { key: 'headService', label: 'Head Service', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  rayjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'jobStatus', label: 'Job Status', width: 'w-28' },
    { key: 'deploymentStatus', label: 'Deployment', width: 'w-32' },
    { key: 'cluster', label: 'Cluster', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  rayservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'serviceStatus', label: 'Service Status', width: 'w-28' },
    { key: 'clusters', label: 'Clusters', width: 'w-56', tooltip: 'Active and pending RayCluster names' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  raycronjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'schedule', label: 'Schedule', width: 'w-32' },
    { key: 'suspend', label: 'Suspend', width: 'w-20' },
    { key: 'lastSchedule', label: 'Last Schedule', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  leaderworkersets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'replicas', label: 'Groups', width: 'w-20', tooltip: 'Desired leader-worker groups' },
    { key: 'size', label: 'Size', width: 'w-20', tooltip: 'Pods per group' },
    { key: 'ready', label: 'Ready', width: 'w-20', tooltip: 'Ready groups / desired groups' },
    { key: 'updated', label: 'Updated', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  jobsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'replicatedJobs', label: 'Repl. Jobs', width: 'w-24', tooltip: 'Number of replicated job templates' },
    { key: 'ready', label: 'Ready', width: 'w-20' },
    { key: 'succeeded', label: 'Succeeded', width: 'w-24' },
    { key: 'failed', label: 'Failed', width: 'w-20' },
    { key: 'restarts', label: 'Restarts', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  inferenceservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-36', tooltip: 'Ready condition + model load state' },
    { key: 'url', label: 'URL', width: 'w-64' },
    { key: 'modelFormat', label: 'Format', width: 'w-28' },
    { key: 'runtime', label: 'Runtime', width: 'w-40' },
    { key: 'deploymentMode', label: 'Mode', width: 'w-28', tooltip: 'Serverless, RawDeployment, or ModelMesh' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  servingruntimes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'modelFormats', label: 'Model Formats', width: 'w-44', tooltip: 'Supported model formats' },
    { key: 'image', label: 'Image', width: 'w-64' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterservingruntimes: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'modelFormats', label: 'Model Formats', width: 'w-44', tooltip: 'Supported model formats' },
    { key: 'image', label: 'Image', width: 'w-64' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  inferencegraphs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'nodes', label: 'Nodes', width: 'w-20', tooltip: 'Router nodes in the graph' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  trainedmodels: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'framework', label: 'Framework', width: 'w-28' },
    { key: 'storageUri', label: 'Storage URI', width: 'w-56' },
    { key: 'inferenceService', label: 'Inference Service', width: 'w-40', tooltip: 'Parent InferenceService' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  llminferenceservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'model', label: 'Model', width: 'w-56', tooltip: 'Model name or URI' },
    { key: 'replicas', label: 'Replicas', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  inferencepools: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-36', tooltip: 'Gateway acceptance and EPP resolution' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'targetPorts', label: 'Target Ports', width: 'w-28' },
    { key: 'extensionRef', label: 'Endpoint Picker', width: 'w-40', tooltip: 'EPP extension service' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  inferenceobjectives: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'poolRef', label: 'Pool', width: 'w-40', tooltip: 'Target InferencePool' },
    { key: 'priority', label: 'Priority', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  volcanojobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'queue', label: 'Queue', width: 'w-32' },
    { key: 'minAvailable', label: 'Min Available', width: 'w-28' },
    { key: 'pods', label: 'Pods', width: 'w-44', tooltip: 'Running / succeeded / failed pod counts' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  volcanoqueues: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'weight', label: 'Weight', width: 'w-20' },
    { key: 'capability', label: 'Capability', width: 'w-48' },
    { key: 'allocated', label: 'Allocated', width: 'w-48' },
    { key: 'podGroups', label: 'PodGroups', width: 'w-44', tooltip: 'PodGroup counts by state' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  volcanopodgroups: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-32' },
    { key: 'queue', label: 'Queue', width: 'w-32' },
    { key: 'minMember', label: 'Min Member', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  jobflows: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'flows', label: 'Flows', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  jobtemplates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'tasks', label: 'Tasks', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  kaiqueues: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'parentQueue', label: 'Parent', width: 'w-32' },
    { key: 'priority', label: 'Priority', width: 'w-20' },
    { key: 'quota', label: 'Quota', width: 'w-52', tooltip: 'GPU fractions, CPU millicpus, memory MB; -1 = unlimited' },
    { key: 'allocated', label: 'Allocated', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  kaipodgroups: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-32' },
    { key: 'queue', label: 'Queue', width: 'w-32' },
    { key: 'minMember', label: 'Min Member', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  kaitoworkspaces: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'instanceType', label: 'Instance Type', width: 'w-40', tooltip: 'GPU node SKU provisioned for the workspace' },
    { key: 'preset', label: 'Model', width: 'w-44', tooltip: 'Preset model (inference or tuning)' },
    { key: 'nodes', label: 'Nodes', width: 'w-20', tooltip: 'Worker nodes ready / target' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  ragengines: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'embedding', label: 'Embedding Model', width: 'w-56', tooltip: 'Local model ID/image or remote endpoint' },
    { key: 'instanceType', label: 'Instance Type', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  nimservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'model', label: 'Model / Image', width: 'w-64', tooltip: 'Served model name (falls back to NIM image)' },
    { key: 'replicas', label: 'Replicas', width: 'w-28', tooltip: 'Available / desired (HPA range when autoscaling)' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  nimcaches: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'source', label: 'Model Source', width: 'w-64', tooltip: 'NGC model puller image, or DataStore/HF model name' },
    { key: 'storage', label: 'Storage', width: 'w-24', tooltip: 'Requested PVC size' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  nimpipelines: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'services', label: 'Services', width: 'w-28', tooltip: 'NIM services in pipeline' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  deviceconfigs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'driver', label: 'Driver', width: 'w-40', tooltip: 'Out-of-tree driver install enabled (version)' },
    { key: 'devicePluginImage', label: 'Device Plugin', width: 'w-64' },
    { key: 'nodes', label: 'Nodes', width: 'w-20', tooltip: 'Device plugin DaemonSet available / desired' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  pytorchjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'replicas', label: 'Progress', width: 'w-48', tooltip: 'Active + succeeded / desired per replica type; failures shown separately' },
    { key: 'elapsed', label: 'Elapsed', width: 'w-24', tooltip: 'Start to completion (or now)' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  tfjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'replicas', label: 'Progress', width: 'w-56', tooltip: 'Active + succeeded / desired per replica type; failures shown separately' },
    { key: 'elapsed', label: 'Elapsed', width: 'w-24', tooltip: 'Start to completion (or now)' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  mpijobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'replicas', label: 'Progress', width: 'w-48', tooltip: 'Active + succeeded / desired per replica type; failures shown separately' },
    { key: 'elapsed', label: 'Elapsed', width: 'w-24', tooltip: 'Start to completion (or now)' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  trainjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'runtime', label: 'Runtime', width: 'w-44', tooltip: 'Training runtime reference' },
    { key: 'suspended', label: 'Suspended', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  scaledjobs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'target', label: 'Job Target', width: 'w-48' },
    { key: 'strategy', label: 'Strategy', width: 'w-28' },
    { key: 'triggerTypes', label: 'Trigger Types', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  triggerauthentications: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'secretTargetRef', label: 'Secret Refs', width: 'w-20' },
    { key: 'env', label: 'Env Vars', width: 'w-20' },
    { key: 'hashiCorpVault', label: 'Vault', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clustertriggerauthentications: [
    { key: 'name', label: 'Name' },
    { key: 'secretTargetRef', label: 'Secret Refs', width: 'w-20' },
    { key: 'env', label: 'Env Vars', width: 'w-20' },
    { key: 'hashiCorpVault', label: 'Vault', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  servicemonitors: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'endpoints', label: 'Endpoints', width: 'w-20', tooltip: 'Number of scrape endpoints' },
    { key: 'jobLabel', label: 'Job Label', width: 'w-32' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  prometheusrules: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'groups', label: 'Groups', width: 'w-20' },
    { key: 'rules', label: 'Rules', width: 'w-20', tooltip: 'Total alert + recording rules' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  podmonitors: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'endpoints', label: 'Endpoints', width: 'w-20', tooltip: 'Number of pod metrics endpoints' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // ============================================================================
  // KYVERNO / POLICY REPORT RESOURCES
  // ============================================================================
  policyreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'scope', label: 'Subject', width: 'w-56' },
    { key: 'status', label: 'Status', width: 'w-32' },
    { key: 'pass', label: 'Pass', width: 'w-16' },
    { key: 'fail', label: 'Fail', width: 'w-16' },
    { key: 'warn', label: 'Warn', width: 'w-20' },
    { key: 'error', label: 'Err', width: 'w-16' },
    { key: 'skip', label: 'Skip', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterpolicyreports: [
    { key: 'name', label: 'Name' },
    { key: 'scope', label: 'Subject', width: 'w-56' },
    { key: 'status', label: 'Status', width: 'w-32' },
    { key: 'pass', label: 'Pass', width: 'w-16' },
    { key: 'fail', label: 'Fail', width: 'w-16' },
    { key: 'warn', label: 'Warn', width: 'w-20' },
    { key: 'error', label: 'Err', width: 'w-16' },
    { key: 'skip', label: 'Skip', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  kyvernopolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'action', label: 'Action', width: 'w-36', tooltip: 'Enforcement at admission — Enforce blocks, Audit reports; Background only or Inactive when admission is disabled' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'action', label: 'Action', width: 'w-36', tooltip: 'Enforcement at admission — Enforce blocks, Audit reports; Background only or Inactive when admission is disabled' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Kyverno modern CEL family (policies.kyverno.io). "Enforcement" is the
  // effective posture, not spec.validationActions verbatim — a policy that
  // declares Deny with admission evaluation disabled blocks nothing.
  validatingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Enforcement', width: 'w-44', tooltip: 'Effective enforcement posture, accounting for whether admission evaluation is enabled' },
    { key: 'appliesTo', label: 'Applies To', width: 'min-w-40', tooltip: 'Resources matched by spec.matchConstraints' },
    { key: 'rules', label: 'Rules', width: 'w-20', tooltip: 'CEL validation expressions' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  namespacedvalidatingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Enforcement', width: 'w-44', tooltip: 'Effective enforcement posture, accounting for whether admission evaluation is enabled' },
    { key: 'appliesTo', label: 'Applies To', width: 'min-w-40' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  imagevalidatingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Enforcement', width: 'w-44' },
    { key: 'images', label: 'Images', width: 'min-w-40', tooltip: 'Image references this policy verifies' },
    { key: 'attestors', label: 'Attestors', width: 'w-20', tooltip: 'Trusted signing authorities' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  namespacedimagevalidatingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Enforcement', width: 'w-44' },
    { key: 'images', label: 'Images', width: 'min-w-40' },
    { key: 'attestors', label: 'Attestors', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  mutatingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Enforcement', width: 'w-44' },
    { key: 'appliesTo', label: 'Applies To', width: 'min-w-40' },
    { key: 'rules', label: 'Mutations', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  namespacedmutatingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Enforcement', width: 'w-44' },
    { key: 'appliesTo', label: 'Applies To', width: 'min-w-40' },
    { key: 'rules', label: 'Mutations', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  generatingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Enforcement', width: 'w-44' },
    { key: 'appliesTo', label: 'Applies To', width: 'min-w-40' },
    { key: 'rules', label: 'Generates', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  namespacedgeneratingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Enforcement', width: 'w-44' },
    { key: 'appliesTo', label: 'Applies To', width: 'min-w-40' },
    { key: 'rules', label: 'Generates', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // The Deleting kinds are the only modern family whose headline column is the
  // schedule rather than the enforcement posture, so they carry their own
  // health signal. It is Last Run, NOT readiness: Kyverno 1.18.2 declares a
  // READY printer column on these CRDs but never populates
  // status.conditionStatus.ready, so `kubectl get deletingpolicies` prints it
  // blank and a Ready column here would read "unknown" on every row forever.
  // It also has nothing to catch — an uncompilable policy is rejected at
  // admission and never exists. What does go wrong is a policy that exists and
  // silently never fires, which lastExecutionTime shows.
  //
  // The other eight kinds deliberately have no readiness column either: their
  // status cell already returns "Not Ready" IN PLACE OF the posture (see
  // getModernKyvernoPolicyStatus), so a second one would be redundant.
  deletingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'lastRun', label: 'Last Run', width: 'w-28', tooltip: 'When the schedule last fired. A scheduled policy that has never run is the failure worth catching — Kyverno rejects uncompilable policies at admission, so a broken one never exists to flag.' },
    { key: 'schedule', label: 'Schedule', width: 'w-32', tooltip: 'Cron schedule on which matched resources are deleted' },
    { key: 'appliesTo', label: 'Deletes', width: 'min-w-40' },
    { key: 'rules', label: 'Conditions', width: 'w-28', tooltip: 'CEL conditions narrowing what is deleted; none means every matched resource' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  namespaceddeletingpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'lastRun', label: 'Last Run', width: 'w-28', tooltip: 'When the schedule last fired. A scheduled policy that has never run is the failure worth catching — Kyverno rejects uncompilable policies at admission, so a broken one never exists to flag.' },
    { key: 'schedule', label: 'Schedule', width: 'w-32' },
    { key: 'appliesTo', label: 'Deletes', width: 'min-w-40' },
    { key: 'rules', label: 'Conditions', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  policyexceptions: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Exempts', width: 'w-28', tooltip: 'How many policies this exception bypasses' },
    { key: 'policies', label: 'Policies', width: 'min-w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cleanuppolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'schedule', label: 'Schedule', width: 'w-32' },
    { key: 'appliesTo', label: 'Deletes', width: 'min-w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clustercleanuppolicies: [
    { key: 'name', label: 'Name' },
    { key: 'schedule', label: 'Schedule', width: 'w-32' },
    { key: 'appliesTo', label: 'Deletes', width: 'min-w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  grpcroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-20', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  tcproutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-20', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  tlsroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'hostnames', label: 'Hostnames', width: 'w-48', tooltip: 'SNI hostnames for TLS routing' },
    { key: 'parents', label: 'Gateways', width: 'w-36' },
    { key: 'backends', label: 'Backends', width: 'w-48', tooltip: 'Backend services receiving traffic' },
    { key: 'rules', label: 'Rules', width: 'w-20', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  sealedsecrets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Synced', width: 'w-24' },
    { key: 'keys', label: 'Keys', width: 'w-20' },
    { key: 'type', label: 'Type', width: 'w-36', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  workflowtemplates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'entrypoint', label: 'Entrypoint', width: 'w-36' },
    { key: 'templates', label: 'Templates', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterworkflowtemplates: [
    { key: 'name', label: 'Name' },
    { key: 'entrypoint', label: 'Entrypoint', width: 'w-36' },
    { key: 'templates', label: 'Templates', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  networkpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'policyTypes', label: 'Types', width: 'w-28' },
    { key: 'selector', label: 'Pod Selector', width: 'w-48' },
    { key: 'rules', label: 'Rules', width: 'w-24', tooltip: 'Ingress / Egress rule count' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  caliconetworkpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'namespaceSelector', label: 'Namespace Selector', width: 'w-48', defaultVisible: false },
    { key: 'serviceAccountSelector', label: 'Service Account Selector', width: 'w-48', defaultVisible: false },
    { key: 'tier', label: 'Tier', width: 'w-28' },
    { key: 'order', label: 'Order', width: 'w-24' },
    { key: 'types', label: 'Types', width: 'w-28' },
    { key: 'rules', label: 'Rules', width: 'w-24', tooltip: 'Ingress / Egress rule count' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  calicoglobalnetworkpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'namespaceSelector', label: 'Namespace Selector', width: 'w-48', defaultVisible: false },
    { key: 'serviceAccountSelector', label: 'Service Account Selector', width: 'w-48', defaultVisible: false },
    { key: 'tier', label: 'Tier', width: 'w-28' },
    { key: 'order', label: 'Order', width: 'w-24' },
    { key: 'types', label: 'Types', width: 'w-28' },
    { key: 'rules', label: 'Rules', width: 'w-24', tooltip: 'Ingress / Egress rule count' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  calicostagednetworkpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'namespaceSelector', label: 'Namespace Selector', width: 'w-48', defaultVisible: false },
    { key: 'serviceAccountSelector', label: 'Service Account Selector', width: 'w-48', defaultVisible: false },
    { key: 'tier', label: 'Tier', width: 'w-28' },
    { key: 'order', label: 'Order', width: 'w-24' },
    { key: 'stagedAction', label: 'Staged Action', width: 'w-32' },
    { key: 'types', label: 'Types', width: 'w-28' },
    { key: 'rules', label: 'Rules', width: 'w-24', tooltip: 'Ingress / Egress rule count' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  calicostagedglobalnetworkpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'namespaceSelector', label: 'Namespace Selector', width: 'w-48', defaultVisible: false },
    { key: 'serviceAccountSelector', label: 'Service Account Selector', width: 'w-48', defaultVisible: false },
    { key: 'tier', label: 'Tier', width: 'w-28' },
    { key: 'order', label: 'Order', width: 'w-24' },
    { key: 'stagedAction', label: 'Staged Action', width: 'w-32' },
    { key: 'types', label: 'Types', width: 'w-28' },
    { key: 'rules', label: 'Rules', width: 'w-24', tooltip: 'Ingress / Egress rule count' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  calicoippools: [
    { key: 'name', label: 'Name' },
    { key: 'cidr', label: 'CIDR', width: 'w-44' },
    { key: 'blockSize', label: 'Block Size', width: 'w-28' },
    { key: 'encapsulation', label: 'Encapsulation', width: 'w-36' },
    { key: 'natOutgoing', label: 'NAT Outgoing', width: 'w-32' },
    { key: 'disabled', label: 'Disabled', width: 'w-28' },
    { key: 'allowedUses', label: 'Allowed Uses', width: 'w-40', defaultVisible: false },
    { key: 'nodeSelector', label: 'Node Selector', width: 'w-48', defaultVisible: false },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  calicohostendpoints: [
    { key: 'name', label: 'Name' },
    { key: 'node', label: 'Node', width: 'w-48' },
    { key: 'interfaceName', label: 'Interface', width: 'w-32' },
    { key: 'expectedIPs', label: 'Expected IPs', width: 'w-48' },
    { key: 'profiles', label: 'Profiles', width: 'w-48', defaultVisible: false },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  calicotiers: [
    { key: 'name', label: 'Name' },
    { key: 'order', label: 'Order', width: 'w-24' },
    { key: 'defaultAction', label: 'Default Action', width: 'w-36' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  calicostagedkubernetesnetworkpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'selector', label: 'Pod Selector', width: 'w-48' },
    { key: 'stagedAction', label: 'Staged Action', width: 'w-32' },
    { key: 'types', label: 'Types', width: 'w-28' },
    { key: 'rules', label: 'Rules', width: 'w-24', tooltip: 'Ingress / Egress rule count' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  poddisruptionbudgets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'budget', label: 'Budget', width: 'w-36' },
    { key: 'healthy', label: 'Healthy', width: 'w-24' },
    { key: 'allowed', label: 'Allowed', width: 'w-24', tooltip: 'Number of disruptions currently allowed' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  serviceaccounts: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'automount', label: 'Automount', width: 'w-32', tooltip: 'Whether token is automatically mounted in pods' },
    { key: 'secrets', label: 'Secrets', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  roles: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterroles: [
    { key: 'name', label: 'Name' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  rolebindings: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'role', label: 'Role', width: 'w-48' },
    { key: 'subjects', label: 'Subjects', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterrolebindings: [
    { key: 'name', label: 'Name' },
    { key: 'role', label: 'Role', width: 'w-48' },
    { key: 'subjects', label: 'Subjects', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  ingressclasses: [
    { key: 'name', label: 'Name' },
    { key: 'controller', label: 'Controller', width: 'w-64' },
    { key: 'default', label: 'Default', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  leases: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    { key: 'holder', label: 'Holder', width: 'w-48' },
    { key: 'renewTime', label: 'Last Renewed', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  priorityclasses: [
    { key: 'name', label: 'Name' },
    { key: 'value', label: 'Value', width: 'w-24' },
    { key: 'globalDefault', label: 'Global Default', width: 'w-36' },
    { key: 'preemptionPolicy', label: 'Preemption', width: 'w-32' },
    { key: 'description', label: 'Description', width: 'w-64', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  runtimeclasses: [
    { key: 'name', label: 'Name' },
    { key: 'handler', label: 'Handler', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  mutatingwebhookconfigurations: [
    { key: 'name', label: 'Name' },
    { key: 'webhooks', label: 'Webhooks', width: 'w-28' },
    { key: 'failurePolicy', label: 'Failure Policy', width: 'w-36' },
    { key: 'target', label: 'Target', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  validatingwebhookconfigurations: [
    { key: 'name', label: 'Name' },
    { key: 'webhooks', label: 'Webhooks', width: 'w-28' },
    { key: 'failurePolicy', label: 'Failure Policy', width: 'w-36' },
    { key: 'target', label: 'Target', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  events: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'type', label: 'Type', width: 'w-24' },
    { key: 'reason', label: 'Reason', width: 'w-32' },
    { key: 'message', label: 'Message', width: 'w-64' },
    { key: 'object', label: 'Object', width: 'w-48', hideOnMobile: true },
    { key: 'count', label: 'Count', width: 'w-20' },
    { key: 'lastSeen', label: 'Last Seen', width: 'w-28' },
  ],
  // ============================================================================
  // FLUXCD GITOPS RESOURCES
  // ============================================================================
  gitrepositories: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'url', label: 'URL', width: 'w-64' },
    { key: 'ref', label: 'Ref', width: 'w-32', tooltip: 'Branch, tag, or semver' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'revision', label: 'Revision', width: 'w-24', hideOnMobile: true, tooltip: 'Last fetched commit SHA' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  ocirepositories: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'url', label: 'URL', width: 'w-64' },
    { key: 'ref', label: 'Tag', width: 'w-24', tooltip: 'OCI tag or semver' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'revision', label: 'Digest', width: 'w-24', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  helmrepositories: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'url', label: 'URL', width: 'w-64' },
    { key: 'type', label: 'Type', width: 'w-20', tooltip: 'default (Helm) or oci' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  kustomizations: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'source', label: 'Source', width: 'w-48', tooltip: 'Source GitRepository or OCIRepository' },
    { key: 'path', label: 'Path', width: 'w-36', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'revision', label: 'Revision', width: 'w-48', hideOnMobile: true, tooltip: 'Applied git revision' },
    { key: 'inventory', label: 'Resources', width: 'w-24', tooltip: 'Number of managed resources' },
    { key: 'lastUpdated', label: 'Last Updated', width: 'w-28', tooltip: 'Time since last successful reconciliation' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  helmreleases: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'chart', label: 'Chart', width: 'w-40' },
    { key: 'version', label: 'Version', width: 'w-24' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'message', label: 'Message', width: 'w-64', hideOnMobile: true, tooltip: 'Last diagnostic message — distinguishes dependency-wait, install/upgrade failure, and test failure' },
    { key: 'revision', label: 'Rev', width: 'w-16', hideOnMobile: true, tooltip: 'Helm release revision number' },
    { key: 'lastUpdated', label: 'Last Updated', width: 'w-28', tooltip: 'Time since last successful reconciliation' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  alerts: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'provider', label: 'Provider', width: 'w-40' },
    { key: 'events', label: 'Events', width: 'w-24', tooltip: 'Number of event sources' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // ============================================================================
  // ARGOCD GITOPS RESOURCES
  // ============================================================================
  applications: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'project', label: 'Project', width: 'w-28' },
    { key: 'sync', label: 'Sync', width: 'w-24' },
    { key: 'health', label: 'Health', width: 'w-24' },
    { key: 'repo', label: 'Repository', width: 'w-48', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  applicationsets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'generators', label: 'Generators', width: 'w-32' },
    { key: 'template', label: 'Template', width: 'w-40', hideOnMobile: true },
    { key: 'applications', label: 'Apps', width: 'w-20', tooltip: 'Number of generated applications' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  appprojects: [
    { key: 'name', label: 'Name' },
    { key: 'description', label: 'Description', width: 'w-64' },
    { key: 'destinations', label: 'Destinations', width: 'w-24', tooltip: 'Allowed cluster/namespace destinations' },
    { key: 'sources', label: 'Sources', width: 'w-20', tooltip: 'Allowed source repositories' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Trivy Operator
  vulnerabilityreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'container', label: 'Container', width: 'w-28' },
    { key: 'image', label: 'Image', width: 'w-48' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical vulnerabilities' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High vulnerabilities' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium vulnerabilities' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low vulnerabilities' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  configauditreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  exposedsecretreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'container', label: 'Container', width: 'w-28' },
    { key: 'image', label: 'Image', width: 'w-48' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical secrets' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High secrets' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium secrets' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low secrets' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  rbacassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clusterrbacassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clustercompliancereports: [
    { key: 'name', label: 'Name' },
    { key: 'title', label: 'Framework', width: 'w-64' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'pass', label: 'Pass', width: 'w-16' },
    { key: 'fail', label: 'Fail', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  sbomreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'container', label: 'Container', width: 'w-28' },
    { key: 'components', label: 'Components', width: 'w-24' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clustersbomreports: [
    { key: 'name', label: 'Name' },
    { key: 'components', label: 'Components', width: 'w-24' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  infraassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  clusterinfraassessmentreports: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'critical', label: 'C', width: 'w-12', tooltip: 'Critical findings' },
    { key: 'high', label: 'H', width: 'w-12', tooltip: 'High findings' },
    { key: 'medium', label: 'M', width: 'w-12', tooltip: 'Medium findings' },
    { key: 'low', label: 'L', width: 'w-12', tooltip: 'Low findings' },
    { key: 'age', label: 'Age', width: 'w-16' },
  ],
  // External Secrets Operator
  externalsecrets: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'store', label: 'Store', width: 'w-36' },
    { key: 'provider', label: 'Provider', width: 'w-28' },
    { key: 'refreshInterval', label: 'Refresh', width: 'w-24' },
    { key: 'lastSync', label: 'Last Sync', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterexternalsecrets: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'namespaces', label: 'Namespaces', width: 'w-24' },
    { key: 'failed', label: 'Failed', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  secretstores: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'provider', label: 'Provider', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clustersecretstores: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'provider', label: 'Provider', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // ============================================================================
  // VELERO BACKUP & DISASTER RECOVERY
  // ============================================================================
  backups: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-36' },
    { key: 'storageLocation', label: 'Storage', width: 'w-36' },
    { key: 'namespaces', label: 'Scope', width: 'w-24', tooltip: 'Included namespaces (* = all)' },
    { key: 'duration', label: 'Duration', width: 'w-24' },
    { key: 'expiry', label: 'Expires', width: 'w-24' },
    { key: 'errors', label: 'Errors', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // `restores` and `schedules` are keyed group-qualified (see
  // GROUP_QUALIFIED_COLUMN_KEYS): rancher/backup-restore-operator ships
  // restores.resources.cattle.io and several operators ship their own
  // `schedules` kind. Only velero.io resolves to these column sets;
  // everything else falls through to the generic columns.
  velerorestores: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-36' },
    { key: 'backupName', label: 'Backup', width: 'w-40' },
    { key: 'duration', label: 'Duration', width: 'w-24' },
    { key: 'errors', label: 'Errors', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  veleroschedules: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-36' },
    { key: 'schedule', label: 'Schedule', width: 'w-40' },
    { key: 'lastBackup', label: 'Last Backup', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // No status column on purpose: the VSL controller never populates
  // status.phase, so a badge would read "Unknown" on every row forever.
  volumesnapshotlocations: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'provider', label: 'Provider', width: 'w-32' },
    { key: 'config', label: 'Config' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // repositoryType is kopia|restic, and it is what `kubectl get
  // backuprepositories` shows. It earns a column because restic is being retired
  // (no new backups since v1.17, restore dropped in v1.19), which makes it a
  // migration liability worth spotting by scanning rather than by opening each
  // repository one at a time.
  backuprepositories: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-36' },
    { key: 'repositoryType', label: 'Type', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  backupstoragelocations: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-36' },
    { key: 'provider', label: 'Provider', width: 'w-24' },
    { key: 'bucket', label: 'Bucket', width: 'w-40' },
    { key: 'default', label: 'Default', width: 'w-24' },
    { key: 'lastValidation', label: 'Validated', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // ============================================================================
  // CLOUDNATIVEPG (CNPG) POSTGRESQL
  // ============================================================================
  cnpgclusters: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    // w-28 leaves the "Status" label 0.23px short once the sort AND filter
    // affordances are both present, which renders "STAT…" — a sub-pixel miss
    // costs two characters because the ellipsis needs its own room. The filter
    // icon appears as soon as a column holds more than one distinct value, so
    // this is latent on any status column, not specific to the current data.
    // w-44 fits "WAL Archiving Failing" (120.2px against a 127px label budget).
    // CNPG's own phases are mapped to short display states — see
    // getCNPGClusterDisplayState; a 315px sentence fits no column at all.
    { key: 'status', label: 'Status', width: 'w-44' },
    { key: 'instances', label: 'Instances', width: 'w-28', tooltip: 'Ready/Total' },
    { key: 'primary', label: 'Primary', width: 'w-36' },
    { key: 'image', label: 'Image', width: 'w-28' },
    // No Storage column: it rendered spec.storage.size, the configured REQUEST.
    // The useful number is actual usage (kubectl cnpg status shows "Size: 158M")
    // and that isn't in the CR. A plausible-but-wrong-meaning number is worse
    // than an absent one — the reader can't tell which meaning they're getting.
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // `databases`, `publications` and `subscriptions` are crowded plurals — other
  // database operators ship all three, and Knative owns the unqualified
  // `subscriptions` set. Keyed under cnpg* and reached only through
  // GROUP_QUALIFIED_COLUMN_KEYS, so a foreign CRD gets the generic columns
  // instead of a Cluster column it can never fill.
  // Kyverno's working records. Both are keyed by things the reader has to see
  // to know whether anything is wrong: which policy queued the work and what it
  // was queued against, and which resource a scan is about.
  updaterequests: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'status', label: 'State', width: 'w-28' },
    { key: 'type', label: 'Type', width: 'w-24' },
    { key: 'policy', label: 'Policy', width: 'w-52' },
    { key: 'triggers', label: 'Triggered By', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  ephemeralreports: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-32' },
    { key: 'status', label: 'Outcome', width: 'w-28' },
    { key: 'subject', label: 'About', width: 'w-56' },
    { key: 'source', label: 'Produced By', width: 'w-36' },
    { key: 'results', label: 'Results', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  clusterephemeralreports: [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Outcome', width: 'w-28' },
    { key: 'subject', label: 'About', width: 'w-56' },
    { key: 'source', label: 'Produced By', width: 'w-36' },
    { key: 'results', label: 'Results', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cnpgdatabases: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Applied', width: 'w-32' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    { key: 'target', label: 'Database', width: 'w-40' },
    { key: 'message', label: 'Message', width: 'w-64' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cnpgpublications: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Applied', width: 'w-32' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    { key: 'dbname', label: 'In Database', width: 'w-40' },
    { key: 'message', label: 'Message', width: 'w-64' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cnpgsubscriptions: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Applied', width: 'w-32' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    { key: 'dbname', label: 'In Database', width: 'w-40' },
    { key: 'message', label: 'Message', width: 'w-64' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cnpgimagecatalogs: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'majors', label: 'Major Versions', width: 'w-40' },
    { key: 'images', label: 'Images', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cnpgclusterimagecatalogs: [
    { key: 'name', label: 'Name' },
    { key: 'majors', label: 'Major Versions', width: 'w-40' },
    { key: 'images', label: 'Images', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  barmanobjectstores: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-40' },
    // Sized rather than flexible: every sibling table gives its long value a
    // fixed width, and leaving two columns unsized let Name absorb the row at
    // 1920px — truncating the destination and pushing Retention off-screen.
    { key: 'destination', label: 'Destination', width: 'w-64' },
    { key: 'servers', label: 'Servers', width: 'w-32' },
    { key: 'retention', label: 'Retention', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  cnpgbackups: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-44' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    // `barmanObjectStore` is the longest method and the field that decides how
    // the rest of the row reads; at w-36 it was permanently ellipsised.
    { key: 'method', label: 'Method', width: 'w-40' },
    { key: 'started', label: 'Started', width: 'w-24' },
    { key: 'duration', label: 'Duration', width: 'w-24' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  scheduledbackups: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    { key: 'schedule', label: 'Schedule', width: 'w-36' },
    { key: 'lastSchedule', label: 'Last Run', width: 'w-28' },
    { key: 'suspended', label: 'Suspended', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  poolers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    // "Not Scheduled" (83.5px) is kept rather than shortened to "Unscheduled":
    // ScheduledBackup also renders a literal "Scheduled" badge in this same
    // column, so a standalone adjective would read as "has no cron" — a claim
    // about a field Poolers don't have. w-36 buys the words.
    { key: 'status', label: 'Status', width: 'w-36' },
    { key: 'cluster', label: 'Cluster', width: 'w-36' },
    // Sized for the HEADER, not the value: `rw`/`ro` need 27px, but "Type" plus
    // the sort affordance needs more than w-16 leaves, and the label rendered
    // as "T…".
    { key: 'type', label: 'Type', width: 'w-24' },
    { key: 'poolMode', label: 'Pool Mode', width: 'w-32' },
    // status.instances counts pods trying to be scheduled, not ready ones.
    { key: 'instances', label: 'Instances', width: 'w-28', tooltip: 'Scheduled/Total' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // ============================================================================
  // ISTIO SERVICE MESH
  // ============================================================================
  virtualservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-48' },
    { key: 'gateways', label: 'Gateways', width: 'w-40' },
    { key: 'routes', label: 'Routes', width: 'w-20', tooltip: 'HTTP + TCP + TLS routes' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  destinationrules: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'host', label: 'Host', width: 'w-48' },
    { key: 'subsets', label: 'Subsets', width: 'w-20' },
    { key: 'loadBalancer', label: 'LB Policy', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  serviceentries: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-48' },
    { key: 'location', label: 'Location', width: 'w-28' },
    { key: 'ports', label: 'Ports', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  peerauthentications: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'mode', label: 'mTLS Mode', width: 'w-28' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  authorizationpolicies: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-24' },
    { key: 'action', label: 'Action', width: 'w-24' },
    { key: 'rules', label: 'Rules', width: 'w-20' },
    { key: 'selector', label: 'Selector', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Knative Serving
  knativeservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'url', label: 'URL', width: 'w-56' },
    { key: 'latestRevision', label: 'Latest Revision', width: 'w-44' },
    { key: 'traffic', label: 'Traffic', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  knativeconfigurations: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'latestCreated', label: 'Latest Created', width: 'w-48' },
    { key: 'latestReady', label: 'Latest Ready', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  knativerevisions: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'routing', label: 'Traffic', width: 'w-20', tooltip: 'Whether this revision is receiving traffic' },
    { key: 'image', label: 'Image', width: 'w-48' },
    { key: 'concurrency', label: 'Concurrency', width: 'w-32' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  knativeroutes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'url', label: 'URL', width: 'w-56' },
    { key: 'traffic', label: 'Traffic', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Knative Eventing
  brokers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'address', label: 'Address', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  triggers: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'broker', label: 'Broker', width: 'w-36' },
    { key: 'subscriber', label: 'Subscriber', width: 'w-48' },
    { key: 'filter', label: 'Filter', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  eventtypes: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'type', label: 'Type', width: 'w-48' },
    { key: 'source', label: 'Source', width: 'w-44' },
    { key: 'reference', label: 'Reference', width: 'w-40' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Knative Sources
  pingsources: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'schedule', label: 'Schedule', width: 'w-36' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'data', label: 'Data', width: 'w-36' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  apiserversources: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  containersources: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  sinkbindings: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'sink', label: 'Sink', width: 'w-48' },
    { key: 'subject', label: 'Subject', width: 'w-48' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Knative Messaging
  channels: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'address', label: 'Address', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  inmemorychannels: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'address', label: 'Address', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Keyed under knative*, reached only through GROUP_QUALIFIED_COLUMN_KEYS, so a
  // third operator's `subscriptions` gets the generic columns instead of Channel
  // and Subscriber it can never fill.
  knativesubscriptions: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'channel', label: 'Channel', width: 'w-44' },
    { key: 'subscriber', label: 'Subscriber', width: 'w-44' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Knative Flows
  sequences: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'steps', label: 'Steps', width: 'w-16' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  parallels: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'branches', label: 'Branches', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Knative Networking
  knativeingresses: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'ingressClass', label: 'Class', width: 'w-24' },
    { key: 'hosts', label: 'Hosts', width: 'w-56' },
    { key: 'visibility', label: 'Visibility', width: 'w-28' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  knativecertificates: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'dnsNames', label: 'DNS Names', width: 'w-56' },
    { key: 'secretName', label: 'Secret', width: 'w-44' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  serverlessservices: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'mode', label: 'Mode', width: 'w-20' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  domainmappings: [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-36' },
    { key: 'status', label: 'Status', width: 'w-28' },
    { key: 'url', label: 'URL', width: 'w-56' },
    { key: 'age', label: 'Age', width: 'w-24' },
  ],
  // Traefik
  ingressroutes: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'entrypoints', label: 'Entry Points', width: 'w-32 shrink-0' },
    { key: 'hosts', label: 'Hosts', width: 'min-w-44' },
    { key: 'routes', label: 'Routes', width: 'min-w-48', hideOnMobile: true },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'middlewares', label: 'MW', width: 'w-14 shrink-0', tooltip: 'Unique middleware count' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  ingressroutetcps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'entrypoints', label: 'Entry Points', width: 'w-32 shrink-0' },
    { key: 'hosts', label: 'Hosts', width: 'min-w-44' },
    { key: 'routes', label: 'Routes', width: 'min-w-48', hideOnMobile: true },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'middlewares', label: 'MW', width: 'w-14 shrink-0', tooltip: 'Unique middleware count' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  ingressrouteudps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'entrypoints', label: 'Entry Points', width: 'w-32 shrink-0' },
    { key: 'routes', label: 'Routes', width: 'min-w-48' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  middlewares: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'type', label: 'Type', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  middlewaretcps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'type', label: 'Type', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  traefikservices: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'type', label: 'Type', width: 'w-36 shrink-0' },
    { key: 'targets', label: 'Targets', width: 'min-w-48' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  serverstransports: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'serverName', label: 'Server Name', width: 'w-40' },
    { key: 'insecure', label: 'Skip Verify', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  serverstransporttcps: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'serverName', label: 'Server Name', width: 'w-40' },
    { key: 'insecure', label: 'Skip Verify', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  tlsoptions: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'minVersion', label: 'Min TLS', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  tlsstores: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  // Cluster API (CAPI)
  capiclusters: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'provider', label: 'Provider', width: 'w-20 shrink-0' },
    { key: 'class', label: 'Class', width: 'w-32 shrink-0' },
    { key: 'cpReplicas', label: 'CP Ready', width: 'w-24 shrink-0', tooltip: 'Control plane replicas (ready/desired)' },
    { key: 'workerReplicas', label: 'Workers', width: 'w-20 shrink-0', tooltip: 'Worker replicas (ready/desired)' },
    { key: 'phase', label: 'Phase', width: 'w-28 shrink-0' },
    { key: 'version', label: 'Version', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  machinedeployments: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'cluster', label: 'Cluster', width: 'w-32 shrink-0' },
    { key: 'ready', label: 'Ready', width: 'w-20 shrink-0', tooltip: 'Ready replicas / desired' },
    { key: 'phase', label: 'Phase', width: 'w-28 shrink-0' },
    { key: 'version', label: 'Version', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  machines: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'cluster', label: 'Cluster', width: 'w-32 shrink-0' },
    { key: 'role', label: 'Role', width: 'w-28 shrink-0' },
    { key: 'phase', label: 'Phase', width: 'w-28 shrink-0' },
    { key: 'node', label: 'Node', width: 'w-32 shrink-0' },
    { key: 'version', label: 'Version', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  machinesets: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'cluster', label: 'Cluster', width: 'w-32 shrink-0' },
    { key: 'ready', label: 'Ready', width: 'w-20 shrink-0' },
    { key: 'phase', label: 'Phase', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  machinepools: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'cluster', label: 'Cluster', width: 'w-32 shrink-0' },
    { key: 'ready', label: 'Ready', width: 'w-20 shrink-0' },
    { key: 'phase', label: 'Phase', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  kubeadmcontrolplanes: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'cluster', label: 'Cluster', width: 'w-32 shrink-0' },
    { key: 'ready', label: 'Ready', width: 'w-20 shrink-0' },
    { key: 'initialized', label: 'Initialized', width: 'w-28 shrink-0' },
    { key: 'version', label: 'Version', width: 'w-24 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  clusterclasses: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  machinehealthchecks: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'cluster', label: 'Cluster', width: 'w-32 shrink-0' },
    { key: 'healthy', label: 'Healthy', width: 'w-20 shrink-0', tooltip: 'Healthy machines / expected' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  // AWS CAPI Infrastructure Provider
  awsmanagedcontrolplanes: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'eksCluster', label: 'EKS Cluster', width: 'w-36 shrink-0' },
    { key: 'region', label: 'Region', width: 'w-28 shrink-0' },
    { key: 'version', label: 'Version', width: 'w-24 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  awsmanagedmachinepools: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'instanceType', label: 'Instance', width: 'w-28 shrink-0' },
    { key: 'replicas', label: 'Ready', width: 'w-20 shrink-0', tooltip: 'Ready replicas' },
    { key: 'capacityType', label: 'Capacity', width: 'w-28 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  awsmachines: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'instanceType', label: 'Instance', width: 'w-28 shrink-0' },
    { key: 'instanceState', label: 'State', width: 'w-24 shrink-0' },
    { key: 'instanceID', label: 'Instance ID', width: 'w-40 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  awsmachinetemplates: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'instanceType', label: 'Instance', width: 'w-28 shrink-0' },
    { key: 'capacity', label: 'Capacity', width: 'w-32 shrink-0', tooltip: 'Computed CPU/memory' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  awsmanagedclusters: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'endpoint', label: 'Endpoint', width: 'min-w-44' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  // GCP CAPI Infrastructure Provider
  gcpmanagedcontrolplanes: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'gkeCluster', label: 'GKE Cluster', width: 'w-36 shrink-0' },
    { key: 'project', label: 'Project', width: 'w-36 shrink-0' },
    { key: 'location', label: 'Location', width: 'w-28 shrink-0' },
    { key: 'version', label: 'Version', width: 'w-24 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  gcpmanagedmachinepools: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'machineType', label: 'Machine Type', width: 'w-32 shrink-0' },
    { key: 'replicas', label: 'Replicas', width: 'w-20 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  gcpmachines: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'instanceType', label: 'Instance', width: 'w-28 shrink-0' },
    { key: 'zone', label: 'Zone', width: 'w-28 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  gcpmachinetemplates: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'instanceType', label: 'Instance', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  gcpmanagedclusters: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'project', label: 'Project', width: 'w-36 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  // Azure CAPI Infrastructure Provider
  azuremanagedcontrolplanes: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'location', label: 'Location', width: 'w-28 shrink-0' },
    { key: 'resourceGroup', label: 'Resource Group', width: 'w-40 shrink-0' },
    { key: 'version', label: 'Version', width: 'w-24 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  azuremanagedmachinepools: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'sku', label: 'VM Size', width: 'w-32 shrink-0' },
    { key: 'mode', label: 'Mode', width: 'w-20 shrink-0' },
    { key: 'replicas', label: 'Replicas', width: 'w-20 shrink-0' },
    { key: 'priority', label: 'Priority', width: 'w-24 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  azuremachines: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'vmSize', label: 'VM Size', width: 'w-32 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  azuremachinetemplates: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'vmSize', label: 'VM Size', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  azuremanagedclusters: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  // Contour
  httpproxies: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-36 shrink-0' },
    { key: 'fqdn', label: 'FQDN', width: 'min-w-44' },
    { key: 'routes', label: 'Routes', width: 'w-20 shrink-0' },
    { key: 'includes', label: 'Includes', width: 'w-24 shrink-0' },
    { key: 'tls', label: 'TLS', width: 'w-14 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-28 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  // Crossplane — Managed Resources are unbounded (one kind per provider CRD);
  // routed via isLikelyCrossplaneMRGroup, not by exact kind plural.
  crossplanemanagedresources: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'namespace', label: 'Namespace', width: 'w-32 shrink-0' },
    { key: 'kind', label: 'Kind', width: 'w-32 shrink-0' },
    { key: 'external', label: 'External Name', width: 'min-w-48', hideOnMobile: true },
    { key: 'provider', label: 'Provider Config', width: 'w-40 shrink-0', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  providers: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'package', label: 'Package', width: 'min-w-48' },
    { key: 'revision', label: 'Revision', width: 'w-36 shrink-0', hideOnMobile: true },
    { key: 'status', label: 'Status', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  providerconfigs: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'credentials', label: 'Credentials', width: 'w-36 shrink-0' },
    { key: 'status', label: 'Status', width: 'w-32 shrink-0' },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  compositions: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'composite', label: 'Composite Kind', width: 'w-44 shrink-0' },
    { key: 'mode', label: 'Mode', width: 'w-24 shrink-0' },
    { key: 'functions', label: 'Functions', width: 'w-28 shrink-0', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
  compositeresourcedefinitions: [
    { key: 'name', label: 'Name', width: 'min-w-40' },
    { key: 'kind', label: 'Kind', width: 'w-40 shrink-0' },
    { key: 'claim', label: 'Claim Kind', width: 'w-40 shrink-0', hideOnMobile: true },
    { key: 'age', label: 'Age', width: 'w-24 shrink-0' },
  ],
}

// Map (plural, group) → KNOWN_COLUMNS key for kinds that collide with core K8s
const GROUP_QUALIFIED_COLUMN_KEYS: Record<string, Record<string, string>> = {
  networkpolicy: {
    'networking.k8s.io': 'networkpolicies',
    'projectcalico.org': 'caliconetworkpolicies',
    'crd.projectcalico.org': 'caliconetworkpolicies',
  },
  networkpolicies: {
    'networking.k8s.io': 'networkpolicies',
    'projectcalico.org': 'caliconetworkpolicies',
    'crd.projectcalico.org': 'caliconetworkpolicies',
  },
  clusters: { 'postgresql.cnpg.io': 'cnpgclusters', 'cluster.x-k8s.io': 'capiclusters' },
  // Velero owns the unqualified `backups` column set; CNPG Backups carry a
  // completely different shape (cluster + method, no storage location/expiry).
  backups: { 'postgresql.cnpg.io': 'cnpgbackups' },
  clusterpolicies: { 'nvidia.com': 'nvidiaclusterpolicies' },
  // Kyverno's namespaced Policy. `policies` is a plural several projects use,
  // so it is only Kyverno's when the group says so; the curated set and its
  // cells already existed but nothing routed to them.
  policies: { 'kyverno.io': 'kyvernopolicies' },
  // Istio Gateway is not a Gateway API Gateway: it has servers and a workload
  // selector, and none of Class/Listeners/Routes/Addresses.
  gateways: { 'networking.istio.io': 'istiogateways' },
  job: { batch: 'jobs', 'batch.volcano.sh': 'volcanojobs' },
  jobs: { batch: 'jobs', 'batch.volcano.sh': 'volcanojobs' },
  queue: { 'scheduling.volcano.sh': 'volcanoqueues', 'scheduling.run.ai': 'kaiqueues' },
  queues: { 'scheduling.volcano.sh': 'volcanoqueues', 'scheduling.run.ai': 'kaiqueues' },
  podgroup: { 'scheduling.volcano.sh': 'volcanopodgroups', 'scheduling.run.ai': 'kaipodgroups' },
  podgroups: { 'scheduling.volcano.sh': 'volcanopodgroups', 'scheduling.run.ai': 'kaipodgroups' },
  workspace: { 'kaito.sh': 'kaitoworkspaces' },
  workspaces: { 'kaito.sh': 'kaitoworkspaces' },
  services: { 'serving.knative.dev': 'knativeservices' },
  configurations: { 'serving.knative.dev': 'knativeconfigurations' },
  revisions: { 'serving.knative.dev': 'knativerevisions' },
  routes: { 'serving.knative.dev': 'knativeroutes' },
  ingresses: { 'networking.internal.knative.dev': 'knativeingresses' },
  certificates: { 'networking.internal.knative.dev': 'knativecertificates' },
  restores: { 'velero.io': 'velerorestores' },
  schedules: { 'velero.io': 'veleroschedules' },
  // CNPG's declarative objects. Knative owns the unqualified `subscriptions`
  // set, and `databases` / `publications` are shipped by several database
  // operators — none of which have a CNPG Cluster to point at.
  databases: { 'postgresql.cnpg.io': 'cnpgdatabases' },
  publications: { 'postgresql.cnpg.io': 'cnpgpublications' },
  subscriptions: {
    'postgresql.cnpg.io': 'cnpgsubscriptions',
    'messaging.knative.dev': 'knativesubscriptions',
  },
  imagecatalogs: { 'postgresql.cnpg.io': 'cnpgimagecatalogs' },
  clusterimagecatalogs: { 'postgresql.cnpg.io': 'cnpgclusterimagecatalogs' },
  objectstores: { 'barmancloud.cnpg.io': 'barmanobjectstores' },
  ippools: {
    'projectcalico.org': 'calicoippools',
    'crd.projectcalico.org': 'calicoippools',
  },
  hostendpoints: {
    'projectcalico.org': 'calicohostendpoints',
    'crd.projectcalico.org': 'calicohostendpoints',
  },
  tiers: {
    'projectcalico.org': 'calicotiers',
    'crd.projectcalico.org': 'calicotiers',
  },
  globalnetworkpolicies: {
    'projectcalico.org': 'calicoglobalnetworkpolicies',
    'crd.projectcalico.org': 'calicoglobalnetworkpolicies',
  },
  globalnetworkpolicy: {
    'projectcalico.org': 'calicoglobalnetworkpolicies',
    'crd.projectcalico.org': 'calicoglobalnetworkpolicies',
  },
  stagednetworkpolicies: {
    'projectcalico.org': 'calicostagednetworkpolicies',
    'crd.projectcalico.org': 'calicostagednetworkpolicies',
  },
  stagednetworkpolicy: {
    'projectcalico.org': 'calicostagednetworkpolicies',
    'crd.projectcalico.org': 'calicostagednetworkpolicies',
  },
  stagedglobalnetworkpolicies: {
    'projectcalico.org': 'calicostagedglobalnetworkpolicies',
    'crd.projectcalico.org': 'calicostagedglobalnetworkpolicies',
  },
  stagedglobalnetworkpolicy: {
    'projectcalico.org': 'calicostagedglobalnetworkpolicies',
    'crd.projectcalico.org': 'calicostagedglobalnetworkpolicies',
  },
  stagedkubernetesnetworkpolicies: {
    'projectcalico.org': 'calicostagedkubernetesnetworkpolicies',
    'crd.projectcalico.org': 'calicostagedkubernetesnetworkpolicies',
  },
  stagedkubernetesnetworkpolicy: {
    'projectcalico.org': 'calicostagedkubernetesnetworkpolicies',
    'crd.projectcalico.org': 'calicostagedkubernetesnetworkpolicies',
  },
}

const GROUP_QUALIFIED_ONLY_COLUMN_KEYS = new Set([
  'networkpolicy', 'networkpolicies',
  'job', 'jobs',
  // Other projects serve their own IPPool CRD, so only a Calico group earns the
  // Calico columns.
  'ippool', 'ippools',
  'globalnetworkpolicy', 'globalnetworkpolicies',
  'stagednetworkpolicy', 'stagednetworkpolicies',
  'stagedglobalnetworkpolicy', 'stagedglobalnetworkpolicies',
  'stagedkubernetesnetworkpolicy', 'stagedkubernetesnetworkpolicies',
])

const CALICO_ONLY_COLUMN_KEYS = new Set([
  'globalnetworkpolicy', 'globalnetworkpolicies',
  'stagednetworkpolicy', 'stagednetworkpolicies',
  'stagedglobalnetworkpolicy', 'stagedglobalnetworkpolicies',
  'stagedkubernetesnetworkpolicy', 'stagedkubernetesnetworkpolicies',
])

// Normalize a kind name to its plural API form used in KNOWN_COLUMNS keys.
// Handles CRD singular names from URLs: 'ScaledObject' → 'scaledobjects', 'NodePool' → 'nodepools'
// When group is provided, resolves collisions (e.g., 'services' + 'serving.knative.dev' → 'knativeservices')
export function normalizeKindToPlural(kind: string, group?: string): string {
  const lower = kind.toLowerCase()
  // Check group-qualified mapping first for collision resolution
  if (group && GROUP_QUALIFIED_COLUMN_KEYS[lower]?.[group]) {
    return GROUP_QUALIFIED_COLUMN_KEYS[lower][group]
  }
  if (group && GROUP_QUALIFIED_ONLY_COLUMN_KEYS.has(lower)) return `__generic_${lower}`
  if (CALICO_ONLY_COLUMN_KEYS.has(lower)) return `__generic_${lower}`
  if (KNOWN_COLUMNS[lower]) return lower
  // Try adding 's' but avoid double-s (e.g., "ingress" → "ingresss")
  if (!lower.endsWith('s') && KNOWN_COLUMNS[lower + 's']) return lower + 's'
  // Try 'es' for kinds ending in s/sh/ch/x/z (e.g., "ingress" → "ingresses")
  if (KNOWN_COLUMNS[lower + 'es']) return lower + 'es'
  // Try 'ies' for kinds ending in y (e.g., "httpproxy" → "httpproxies")
  if (lower.endsWith('y') && KNOWN_COLUMNS[lower.slice(0, -1) + 'ies']) return lower.slice(0, -1) + 'ies'
  return lower
}

// Crossplane Managed Resources are unbounded (each provider ships its own
// CRDs under per-service groups like s3.aws.upbound.io, compute.gcp.upbound.io).
// Route any non-core-Crossplane resource in these groups to the generic MR
// column set. ProviderConfig is excluded — those have their own renderer.
function isLikelyCrossplaneMRGroup(kind: string, group: string): boolean {
  if (!group) return false
  const k = kind.toLowerCase()
  if (k === 'providerconfig' || k === 'providerconfigs') return false
  if (group.endsWith('.upbound.io')) return true
  if (group === 'kubernetes.crossplane.io' || group === 'helm.crossplane.io') return true
  // Reserved Crossplane core groups — never MRs. protection.crossplane.io
  // (Usage/ClusterUsage) and ops.crossplane.io (Operations) are Crossplane v2's
  // new core groups: lifecycle/orchestration objects, not provider resources.
  if (
    group === 'crossplane.io' ||
    group === 'pkg.crossplane.io' ||
    group === 'apiextensions.crossplane.io' ||
    group === 'protection.crossplane.io' ||
    group === 'ops.crossplane.io'
  ) {
    return false
  }
  // Any other *.crossplane.io subgroup is presumed to be a provider's MR group.
  if (group.endsWith('.crossplane.io')) return true
  return false
}

/**
 * API groups the Kubernetes API server reserves. A CRD cannot be created in
 * one, so a curated set keyed on a core kind can never be claimed by a foreign
 * resource and needs no ownership entry. Note the deliberate omissions:
 * gateway.networking.k8s.io, snapshot.storage.k8s.io and cluster.x-k8s.io end
 * in k8s.io but ARE CRD groups, so this is an explicit list, not a suffix test.
 */
const CORE_API_GROUPS = new Set([
  '', 'apps', 'batch', 'autoscaling', 'policy', 'networking.k8s.io',
  'rbac.authorization.k8s.io', 'storage.k8s.io', 'apiextensions.k8s.io',
  'coordination.k8s.io', 'discovery.k8s.io', 'scheduling.k8s.io',
  'admissionregistration.k8s.io', 'certificates.k8s.io', 'node.k8s.io',
  'flowcontrol.apiserver.k8s.io', 'authentication.k8s.io', 'authorization.k8s.io',
  'apiregistration.k8s.io', 'events.k8s.io', 'resource.k8s.io',
  'internal.apiserver.k8s.io', 'storagemigration.k8s.io',
])

// ProviderConfig groups are unbounded — one per provider — so they are matched
// by suffix. Managed resources take the isLikelyCrossplaneMRGroup branch above.
function isCuratedUnboundedGroup(key: string, group: string): boolean {
  if (key !== 'providerconfigs') return false
  return group.endsWith('.crossplane.io') || group.endsWith('.upbound.io')
}

/**
 * Which API group(s) each curated column set was written for.
 *
 * A CRD is not the kind Radar curated just because it reuses its plural:
 * chaos-mesh ships workflows.chaos-mesh.org, Istio ships gateways, and Flux
 * ships providers. Rendering Argo's or Gateway API's columns for those reads
 * every field off a schema the object does not have. Anything not claimed here
 * falls through to the generic set (and, for a CRD, to its own printer
 * columns), which is the honest answer for a kind Radar has never seen.
 *
 * Core kinds need no entry: the API server reserves their groups, so a CRD
 * cannot occupy one. See CORE_API_GROUPS.
 */
const CURATED_COLUMN_GROUPS: Record<string, readonly string[]> = {
  challenges: ['acme.cert-manager.io'],
  orders: ['acme.cert-manager.io'],
  compositeresourcedefinitions: ['apiextensions.crossplane.io'],
  compositions: ['apiextensions.crossplane.io'],
  clustercompliancereports: ['aquasecurity.github.io'],
  clusterinfraassessmentreports: ['aquasecurity.github.io'],
  clusterrbacassessmentreports: ['aquasecurity.github.io'],
  clustersbomreports: ['aquasecurity.github.io'],
  configauditreports: ['aquasecurity.github.io'],
  exposedsecretreports: ['aquasecurity.github.io'],
  infraassessmentreports: ['aquasecurity.github.io'],
  rbacassessmentreports: ['aquasecurity.github.io'],
  sbomreports: ['aquasecurity.github.io'],
  vulnerabilityreports: ['aquasecurity.github.io'],
  analysisruns: ['argoproj.io'],
  applications: ['argoproj.io'],
  applicationsets: ['argoproj.io'],
  appprojects: ['argoproj.io'],
  clusterworkflowtemplates: ['argoproj.io'],
  cronworkflows: ['argoproj.io'],
  rollouts: ['argoproj.io'],
  workflows: ['argoproj.io'],
  workflowtemplates: ['argoproj.io'],
  barmanobjectstores: ['barmancloud.cnpg.io'],
  sealedsecrets: ['bitnami.com'],
  certificaterequests: ['cert-manager.io'],
  certificates: ['cert-manager.io'],
  clusterissuers: ['cert-manager.io'],
  issuers: ['cert-manager.io'],
  capiclusters: ['cluster.x-k8s.io'],
  clusterclasses: ['cluster.x-k8s.io'],
  machinedeployments: ['cluster.x-k8s.io'],
  machinehealthchecks: ['cluster.x-k8s.io'],
  machinepools: ['cluster.x-k8s.io'],
  machines: ['cluster.x-k8s.io'],
  machinesets: ['cluster.x-k8s.io'],
  kubeadmcontrolplanes: ['controlplane.cluster.x-k8s.io'],
  brokers: ['eventing.knative.dev'],
  eventtypes: ['eventing.knative.dev'],
  triggers: ['eventing.knative.dev'],
  clusterexternalsecrets: ['external-secrets.io'],
  clustersecretstores: ['external-secrets.io'],
  externalsecrets: ['external-secrets.io'],
  secretstores: ['external-secrets.io'],
  parallels: ['flows.knative.dev'],
  sequences: ['flows.knative.dev'],
  gatewayclasses: ['gateway.networking.k8s.io'],
  gateways: ['gateway.networking.k8s.io'],
  istiogateways: ['networking.istio.io'],
  grpcroutes: ['gateway.networking.k8s.io'],
  httproutes: ['gateway.networking.k8s.io'],
  tcproutes: ['gateway.networking.k8s.io'],
  tlsroutes: ['gateway.networking.k8s.io'],
  helmreleases: ['helm.toolkit.fluxcd.io'],
  awsmachines: ['infrastructure.cluster.x-k8s.io'],
  awsmachinetemplates: ['infrastructure.cluster.x-k8s.io'],
  awsmanagedclusters: ['infrastructure.cluster.x-k8s.io'],
  awsmanagedcontrolplanes: ['controlplane.cluster.x-k8s.io'],
  awsmanagedmachinepools: ['infrastructure.cluster.x-k8s.io'],
  azuremachines: ['infrastructure.cluster.x-k8s.io'],
  azuremachinetemplates: ['infrastructure.cluster.x-k8s.io'],
  azuremanagedclusters: ['infrastructure.cluster.x-k8s.io'],
  azuremanagedcontrolplanes: ['infrastructure.cluster.x-k8s.io'],
  azuremanagedmachinepools: ['infrastructure.cluster.x-k8s.io'],
  gcpmachines: ['infrastructure.cluster.x-k8s.io'],
  gcpmachinetemplates: ['infrastructure.cluster.x-k8s.io'],
  gcpmanagedclusters: ['infrastructure.cluster.x-k8s.io'],
  gcpmanagedcontrolplanes: ['infrastructure.cluster.x-k8s.io'],
  gcpmanagedmachinepools: ['infrastructure.cluster.x-k8s.io'],
  ec2nodeclasses: ['karpenter.k8s.aws'],
  nodeclaims: ['karpenter.sh'],
  nodepools: ['karpenter.sh'],
  clustertriggerauthentications: ['keda.sh'],
  scaledjobs: ['keda.sh'],
  scaledobjects: ['keda.sh'],
  triggerauthentications: ['keda.sh'],
  kustomizations: ['kustomize.toolkit.fluxcd.io'],
  cleanuppolicies: ['kyverno.io'],
  clustercleanuppolicies: ['kyverno.io'],
  clusterpolicies: ['kyverno.io'],
  kyvernopolicies: ['kyverno.io'],
  updaterequests: ['kyverno.io'],
  policyexceptions: ['kyverno.io', 'policies.kyverno.io'],
  channels: ['messaging.knative.dev'],
  inmemorychannels: ['messaging.knative.dev'],
  knativesubscriptions: ['messaging.knative.dev'],
  podmonitors: ['monitoring.coreos.com'],
  prometheusrules: ['monitoring.coreos.com'],
  servicemonitors: ['monitoring.coreos.com'],
  knativecertificates: ['networking.internal.knative.dev'],
  knativeingresses: ['networking.internal.knative.dev'],
  serverlessservices: ['networking.internal.knative.dev'],
  destinationrules: ['networking.istio.io'],
  serviceentries: ['networking.istio.io'],
  virtualservices: ['networking.istio.io'],
  alerts: ['notification.toolkit.fluxcd.io'],
  nvidiaclusterpolicies: ['nvidia.com'],
  nvidiadrivers: ['nvidia.com'],
  providers: ['pkg.crossplane.io'],
  deletingpolicies: ['policies.kyverno.io'],
  generatingpolicies: ['policies.kyverno.io'],
  imagevalidatingpolicies: ['policies.kyverno.io'],
  mutatingpolicies: ['policies.kyverno.io'],
  namespaceddeletingpolicies: ['policies.kyverno.io'],
  namespacedgeneratingpolicies: ['policies.kyverno.io'],
  namespacedimagevalidatingpolicies: ['policies.kyverno.io'],
  namespacedmutatingpolicies: ['policies.kyverno.io'],
  namespacedvalidatingpolicies: ['policies.kyverno.io'],
  validatingpolicies: ['policies.kyverno.io'],
  cnpgbackups: ['postgresql.cnpg.io'],
  cnpgclusterimagecatalogs: ['postgresql.cnpg.io'],
  cnpgclusters: ['postgresql.cnpg.io'],
  cnpgdatabases: ['postgresql.cnpg.io'],
  cnpgimagecatalogs: ['postgresql.cnpg.io'],
  cnpgpublications: ['postgresql.cnpg.io'],
  cnpgsubscriptions: ['postgresql.cnpg.io'],
  poolers: ['postgresql.cnpg.io'],
  scheduledbackups: ['postgresql.cnpg.io'],
  calicoglobalnetworkpolicies: ['projectcalico.org', 'crd.projectcalico.org'],
  calicohostendpoints: ['projectcalico.org', 'crd.projectcalico.org'],
  calicoippools: ['projectcalico.org', 'crd.projectcalico.org'],
  caliconetworkpolicies: ['projectcalico.org', 'crd.projectcalico.org'],
  calicostagedglobalnetworkpolicies: ['projectcalico.org', 'crd.projectcalico.org'],
  calicostagedkubernetesnetworkpolicies: ['projectcalico.org', 'crd.projectcalico.org'],
  calicostagednetworkpolicies: ['projectcalico.org', 'crd.projectcalico.org'],
  calicotiers: ['projectcalico.org', 'crd.projectcalico.org'],
  httpproxies: ['projectcontour.io'],
  clusterephemeralreports: ['reports.kyverno.io'],
  ephemeralreports: ['reports.kyverno.io'],
  authorizationpolicies: ['security.istio.io'],
  peerauthentications: ['security.istio.io'],
  domainmappings: ['serving.knative.dev'],
  knativeconfigurations: ['serving.knative.dev'],
  knativerevisions: ['serving.knative.dev'],
  knativeroutes: ['serving.knative.dev'],
  knativeservices: ['serving.knative.dev'],
  gitrepositories: ['source.toolkit.fluxcd.io'],
  helmrepositories: ['source.toolkit.fluxcd.io'],
  ocirepositories: ['source.toolkit.fluxcd.io'],
  apiserversources: ['sources.knative.dev'],
  containersources: ['sources.knative.dev'],
  pingsources: ['sources.knative.dev'],
  sinkbindings: ['sources.knative.dev'],
  ingressroutes: ['traefik.io', 'traefik.containo.us'],
  ingressroutetcps: ['traefik.io', 'traefik.containo.us'],
  ingressrouteudps: ['traefik.io', 'traefik.containo.us'],
  middlewares: ['traefik.io', 'traefik.containo.us'],
  middlewaretcps: ['traefik.io', 'traefik.containo.us'],
  serverstransports: ['traefik.io', 'traefik.containo.us'],
  serverstransporttcps: ['traefik.io', 'traefik.containo.us'],
  tlsoptions: ['traefik.io', 'traefik.containo.us'],
  tlsstores: ['traefik.io', 'traefik.containo.us'],
  traefikservices: ['traefik.io', 'traefik.containo.us'],
  backuprepositories: ['velero.io'],
  backups: ['velero.io'],
  backupstoragelocations: ['velero.io'],
  velerorestores: ['velero.io'],
  veleroschedules: ['velero.io'],
  volumesnapshotlocations: ['velero.io'],
  clusterpolicyreports: ['wgpolicyk8s.io'],
  policyreports: ['wgpolicyk8s.io'],
  deviceconfigs: ['amd.com'],
  nimcaches: ['apps.nvidia.com'],
  nimpipelines: ['apps.nvidia.com'],
  nimservices: ['apps.nvidia.com'],
  provisioningrequests: ['autoscaling.x-k8s.io'],
  volcanojobs: ['batch.volcano.sh'],
  jobflows: ['flow.volcano.sh'],
  jobtemplates: ['flow.volcano.sh'],
  inferencepools: ['inference.networking.k8s.io', 'inference.networking.x-k8s.io'],
  inferenceobjectives: ['inference.networking.x-k8s.io', 'llm-d.ai'],
  jobsets: ['jobset.x-k8s.io'],
  kaitoworkspaces: ['kaito.sh'],
  ragengines: ['kaito.sh'],
  admissionchecks: ['kueue.x-k8s.io'],
  clusterqueues: ['kueue.x-k8s.io'],
  localqueues: ['kueue.x-k8s.io'],
  resourceflavors: ['kueue.x-k8s.io'],
  workloads: ['kueue.x-k8s.io'],
  mpijobs: ['kubeflow.org'],
  pytorchjobs: ['kubeflow.org'],
  tfjobs: ['kubeflow.org'],
  leaderworkersets: ['leaderworkerset.x-k8s.io'],
  rayclusters: ['ray.io'],
  raycronjobs: ['ray.io'],
  rayjobs: ['ray.io'],
  rayservices: ['ray.io'],
  kaipodgroups: ['scheduling.run.ai'],
  kaiqueues: ['scheduling.run.ai'],
  volcanopodgroups: ['scheduling.volcano.sh'],
  volcanoqueues: ['scheduling.volcano.sh'],
  clusterservingruntimes: ['serving.kserve.io'],
  inferencegraphs: ['serving.kserve.io'],
  inferenceservices: ['serving.kserve.io'],
  llminferenceservices: ['serving.kserve.io'],
  servingruntimes: ['serving.kserve.io'],
  trainedmodels: ['serving.kserve.io'],
  trainjobs: ['trainer.kubeflow.org'],
}

function getColumnsForKind(kind: string, group?: string): Column[] {
  // Ahead of the curated lookup: a provider ships one CRD per service, so a
  // managed resource routinely collides with a curated plural (acm's
  // Certificate, s3's Service). Resolving the collision the other way would
  // return DEFAULT_COLUMNS and never reach this branch at all.
  if (group && isLikelyCrossplaneMRGroup(kind, group)) {
    return KNOWN_COLUMNS.crossplanemanagedresources
  }
  const key = normalizeKindToPlural(kind, group)
  const curated = KNOWN_COLUMNS[key]
  if (curated) {
    const owners = CURATED_COLUMN_GROUPS[key]
    if (group && owners) return owners.includes(group) ? curated : DEFAULT_COLUMNS
    // A core kind cannot be shadowed by a CRD, so it keeps its columns without
    // an ownership entry. Anything else has to be claimed for this exact group.
    if (!group || CORE_API_GROUPS.has(group)) return curated
    if (isCuratedUnboundedGroup(key, group)) return curated
    return DEFAULT_COLUMNS
  }
  return DEFAULT_COLUMNS
}

/**
 * Whether Radar hand-curates this kind's columns. The single definition of
 * "curated": the printer-column XOR, the storage-key qualifier, and the host's
 * decision whether to request table mode at all must all agree, or the server
 * does work the client throws away — or worse, they disagree about which set a
 * kind is on.
 */
export function hasCuratedColumns(kind: string, group?: string): boolean {
  return getColumnsForKind(kind, group) !== DEFAULT_COLUMNS
}

export function getCellFilterKind(kind: string, group?: string): string {
  const normalized = normalizeKindToPlural(kind, group)
  if (!group || hasCuratedColumns(kind, group) || normalized.startsWith('__generic_')) return normalized
  return `__generic_${normalized}`
}

// Get the default visible columns for a kind
function getDefaultVisibleColumns(columns: Column[]): Set<string> {
  return new Set(columns.filter(c => c.defaultVisible !== false).map(c => c.key))
}

// Materializes one vendor-declared printer column into a self-contained
// ExtraColumn, so it rides the same render/sort/filter rails as a user's custom
// column. Values were evaluated server-side and arrive keyed by uid.
function buildPrinterColumn(def: PrinterColumnDef, index: number, table: PrinterTable): ExtraColumn {
  const read = (resource: any) => readPrinterCell(table, resource?.metadata?.uid, index)
  return {
    key: printerColumnKey(def),
    label: def.name,
    width: printerColumnWidth(def),
    // Columns in kubectl's `-o wide` tier are declared but not shown by
    // default; the column picker still offers them.
    defaultVisible: (def.priority ?? 0) === 0,
    tooltip: def.description || undefined,
    render: (resource: any) => {
      const value = read(resource)
      // `date` cells arrive already humanized ("5m") — the API server's table
      // converter does that, so re-formatting here would parse "5m" as a date.
      const text = formatPrinterCell(value)
      if (!text) return <span className="text-sm text-theme-text-tertiary">-</span>
      // A column the vendor named Ready/Healthy/Established carries the only
      // scannable signal these kinds have — they have no curated Status column.
      // Rendering it as grey text would leave a screen of uniform "True".
      const tone = printerCellTone(def, value)
      if (tone) {
        return (
          // The value, not the column's description — the header tooltip
          // carries that. A cell tooltip exists to reveal a truncated value.
          <Tooltip content={text}>
            <span className={clsx('badge', healthColors[tone])}>{text}</span>
          </Tooltip>
        )
      }
      return (
        <Tooltip content={text}>
          <span className="text-sm text-theme-text-secondary truncate block">{text}</span>
        </Tooltip>
      )
    },
    getSortValue: (resource: any) => printerCellSortValue(read(resource), def),
    getFilterValue: (resource: any) => formatPrinterCell(read(resource)),
  }
}

// Printer columns fill in only where Radar has no curated set for the kind —
// the two are exclusive, never merged. A curated set is hand-tuned and often
// encodes why a vendor-obvious field is the wrong one to show, so it wins.
function buildPrinterColumns(
  kind: string,
  group: string | undefined,
  table: PrinterTable | null | undefined,
): ExtraColumn[] {
  if (!table?.columns?.length) return []
  if (!printerTableMatchesKind(table, kind, group)) return []
  if (hasCuratedColumns(kind, group)) return []
  return table.columns.map((def, i) => buildPrinterColumn(def, i, table))
}

// The kind's effective column set. Printer columns replace the generic
// Name/Namespace/Status/Age set rather than joining it: the vendor's columns
// are the answer to the same question the generic Status column was guessing
// at, and showing both would ask the reader which one to believe. Name,
// Namespace and Age stay because Radar renders those itself (the server drops
// the CRD's own duplicates of them before sending).
function columnsForKindWithPrinter(
  kind: string,
  group: string | undefined,
  printerColumns: ExtraColumn[],
): Column[] {
  const curated = getColumnsForKind(kind, group)
  if (!printerColumns.length || hasCuratedColumns(kind, group)) return curated
  return [
    { key: 'name', label: 'Name' },
    { key: 'namespace', label: 'Namespace', width: 'w-48' },
    ...printerColumns,
    { key: 'age', label: 'Age', width: 'w-24' },
  ]
}

/**
 * The visible-column set for a kind the user has saved settings for.
 *
 * Saved choices win, with two seeded exceptions for columns the blob predates —
 * a user cannot have decided to hide something they were never shown:
 *   - host extras, which are injected by the embedding app;
 *   - printer columns, but only when the blob names none at all. Once it names
 *     one, the user has seen the set and an absent key is a deliberate hide.
 */
export function mergeSavedVisibleColumns(
  savedVisible: string[],
  extraKeys: string[],
  printerColumns: ExtraColumn[],
): Set<string> {
  const merged = new Set(savedVisible)
  for (const k of extraKeys) merged.add(k)
  if (!savedVisible.some(k => k.startsWith(PRINTER_COLUMN_PREFIX))) {
    for (const c of printerColumns) {
      if (c.defaultVisible !== false) merged.add(c.key)
    }
  }
  return merged
}

/** Storage/reset identity for a selection. Two CRDs can share a plural across
 * API groups, and their columns are unrelated. */
export function selectedKindIdentityOf(kind: { name: string; group?: string }): string {
  return `${kind.name} ${kind.group ?? ''}`
}

// Host-injected leading columns minus any whose key collides with a built-in.
// Rendering two columns under one key duplicates React keys and corrupts the
// visibleColumns / columnWidths maps, so a colliding extra is dropped (dev-warn).
// Shared by the allColumns memo and the settings-load effect so the collision
// rule can't drift between them.
function filterHostExtras(
  extraLeadingColumns: ExtraColumn[] | undefined,
  builtinKeys: Set<string>,
  kindName: string,
): ExtraColumn[] {
  if (!extraLeadingColumns?.length) return []
  return extraLeadingColumns.filter(c => {
    if (builtinKeys.has(c.key)) {
      if (import.meta.env?.DEV) {
        // eslint-disable-next-line no-console
        console.warn(`[ResourcesView] extraLeadingColumns key "${c.key}" collides with a built-in column for kind "${kindName}" — extra ignored`)
      }
      return false
    }
    return true
  })
}

// localStorage helpers for column settings
const COLUMN_SETTINGS_PREFIX = 'radar-columns-'

// Storage identity for a kind's column settings. normalizeKindToPlural falls
// through to the bare plural for anything outside the curated collision
// catalog, so two uncurated CRDs sharing a plural in different API groups —
// Crossplane ships two `Usage` kinds — would share one blob and leak column
// visibility, widths and custom columns between them. Harmless while both got
// identical generic columns; not harmless once each has its own printer
// columns. Only the fallthrough kinds are qualified, so curated and core kinds
// keep the key their saved preferences are already under.
export function columnSettingsKey(kind: string, group?: string): string {
  const normalized = normalizeKindToPlural(kind, group)
  if (group && !hasCuratedColumns(kind, group)) {
    return `${normalized}.${group}`
  }
  return normalized
}

interface ColumnSettings {
  visible: string[]
  widths: Record<string, number>
  custom?: CustomColumnDef[]
}

function loadColumnSettings(kind: string, group?: string): ColumnSettings | null {
  try {
    const key = COLUMN_SETTINGS_PREFIX + columnSettingsKey(kind, group)
    const raw = localStorage.getItem(key)
    if (raw) return JSON.parse(raw)
  } catch { /* ignore */ }
  return null
}

function saveColumnSettings(kind: string, group: string | undefined, settings: ColumnSettings) {
  try {
    const key = COLUMN_SETTINGS_PREFIX + columnSettingsKey(kind, group)
    localStorage.setItem(key, JSON.stringify(settings))
  } catch { /* ignore */ }
}

function clearColumnSettings(kind: string, group?: string) {
  try {
    const key = COLUMN_SETTINGS_PREFIX + columnSettingsKey(kind, group)
    localStorage.removeItem(key)
  } catch { /* ignore */ }
}

// Metrics context for passing top metrics to cell renderers without prop drilling
interface MetricsLookup {
  pods: Map<string, TopPodMetrics>   // key: "namespace/name"
  nodes: Map<string, TopNodeMetrics> // key: node name
}

const MetricsContext = React.createContext<MetricsLookup>({ pods: new Map(), nodes: new Map() })

// Context for deeply nested sub-components (PodCell, SecretCell) to access injected platform data
interface ResourcesViewData {
  onNavigate?: (path: string, options?: { replace?: boolean }) => void
  certExpiry?: Record<string, { expired?: boolean; daysLeft: number }>
  certExpiryError?: boolean
  /** Cluster Audit findings keyed by "namespace/name". Raw compatibility
   *  counts render danger as High and warning as Medium. */
  auditBadges?: Record<string, { danger: number; warning: number; messages?: AuditBadgeMessage[] }>
  onOpenLogs?: (params: { namespace: string; podName: string; containers: string[]; containerName?: string }) => void
  onOpenWorkloadLogs?: (params: { namespace: string; workloadKind: string; workloadName: string }) => void
}

export const ResourcesViewDataContext = React.createContext<ResourcesViewData>({})

export interface ResourceQueryResult {
  resourceName?: string
  group?: string
  data?: any[]
  isLoading: boolean
  error?: any
  refetch?: () => void
  dataUpdatedAt?: number
}

export interface LargeListGuardState {
  kind: string
  count?: number
  reason?: 'too-many' | 'count-unavailable'
  limit: number
  namespaces: string[]
}

function formatLargeListScope(namespaces: string[]): string {
  if (namespaces.length === 0) return 'all namespaces'
  if (namespaces.length === 1) return namespaces[0]
  return `${namespaces.length.toLocaleString()} namespaces`
}

interface ResourcesViewProps {
  namespaces: string[]
  selectedResource?: SelectedResource | null
  onResourceClick?: (resource: SelectedResource | null) => void
  onResourceClickYaml?: NavigateToResource
  onKindChange?: () => void // Called when user changes resource type in sidebar
  // Injected data (replacing hooks)
  apiResources?: APIResource[]
  /** @deprecated Use resourceCounts + resourceForbidden + selectedKindQuery instead */
  resourceQueries?: ResourceQueryResult[]
  // Lightweight counts for sidebar badges (from /api/resource-counts)
  resourceCounts?: Record<string, number | null>
  resourceForbidden?: string[]
  /** Per-kind reason a forbidden kind is hidden ("rbac_denied" | "unavailable"),
   *  keyed by the same count key as resourceForbidden. Drives RestrictedState copy. */
  resourceReasons?: Record<string, string>
  resourceUnavailable?: string[]
  // Single query for the currently selected kind's full data
  selectedKindQuery?: ResourceQueryResult
  // Cluster/SSE connection health — the list is SSE-invalidated ("Auto-updating"),
  // so it must degrade to "Reconnecting…" when the stream drops.
  connectionState?: FreshnessConnection
  largeListGuard?: LargeListGuardState | null
  topPodMetrics?: TopPodMetrics[]
  topNodeMetrics?: TopNodeMetrics[]
  certExpiry?: Record<string, { expired?: boolean; daysLeft: number }>
  certExpiryError?: boolean
  /** Cluster Audit findings keyed by "namespace/name". Raw compatibility
   *  counts render danger as High and warning as Medium. */
  auditBadges?: Record<string, { danger: number; warning: number; messages?: AuditBadgeMessage[] }>
  // Pinned kinds
  pinned?: Array<{ name: string; kind: string; group: string }>
  togglePin?: (kind: { name: string; kind: string; group: string }) => void
  isPinned?: (kind: string, group?: string) => boolean
  // Navigation
  locationSearch?: string
  locationPathname?: string
  onNavigate?: (path: string, options?: { replace?: boolean }) => void
  /** URL prefix for this view (default: '/resources'). Must match the route path. */
  basePath?: string
  // Dock actions
  onOpenLogs?: (params: { namespace: string; podName: string; containers: string[]; containerName?: string }) => void
  onOpenWorkloadLogs?: (params: { namespace: string; workloadKind: string; workloadName: string }) => void
  // Callback when selected kind changes — used by parent to fetch data for the selected kind
  onSelectedKindChange?: (kind: { name: string; kind: string; group: string }) => void
  /** When true, the sidebar is not rendered. Useful when a standalone ResourcesSidebar is used externally. */
  hideSidebar?: boolean
  /** Callback when the [+] create button is clicked. Receives the currently selected kind info. */
  onCreateResource?: (kind: { name: string; kind: string; group: string } | null) => void
  /** Default kind when the URL does not include one. */
  defaultKind?: SelectedKindInfo
  /** Columns prepended to KNOWN_COLUMNS for every kind. For example, a
   *  multi-cluster host can inject a leading Cluster column. Each extra
   *  column is self-contained (own render/sort/filter), so the host
   *  doesn't need to extend KNOWN_COLUMNS or per-kind cell renderers.
   *  When undefined, behavior is byte-identical to single-cluster mode. */
  extraLeadingColumns?: ExtraColumn[]
  /** Vendor-declared columns from the kind's CRD (additionalPrinterColumns),
   *  evaluated server-side. Used ONLY for kinds Radar has no curated column
   *  set for — the two are exclusive. Hosts that don't fetch them get the
   *  generic columns and nothing changes. */
  printerTable?: PrinterTable | null
  /** Escape hatch for full-page-nav row selection: receive the FULL
   *  resource object on row click / Enter / `d` / search-Enter, instead
   *  of the stripped {kind, namespace, name, group} shape onResourceClick
   *  gets. Lets the parent read injected fields (e.g. multi-cluster
   *  row-level metadata for cross-tree drill-in nav) without a parallel
   *  (kind, ns, name) → owner lookup that wouldn't dedup for resources
   *  sharing a namespaced name across cluster boundaries. When set,
   *  fires INSTEAD OF onResourceClick on those selection paths.
   *
   *  Not honored on: `y` (open YAML — routes through onResourceClickYaml)
   *  and `l` (open logs — different intent). URL deep-link hydration on
   *  mount (`?resource=ns/name`) only has stripped params and always
   *  calls onResourceClick — hosts using onRowSelect for full-page nav
   *  should also wire onResourceClick to handle the deep-link-on-load
   *  case. */
  onRowSelect?: (resource: any) => void
  /**
   * When provided, the name cell renders as a real `<a href>` instead of
   * relying on per-cell click handlers for navigation. Restores ⌘-click /
   * middle-click / "Copy link" / hover URL preview / screen-reader link
   * semantics. Hosts using full-page navigation should prefer this over
   * `onRowSelect`; the anchor will own navigation and the rest of the row
   * remains clickable for selection (drawer open).
   */
  rowHrefFor?: (resource: any) => string
  /**
   * Overrides the default compare-mode submit (which navigates to
   * `/compare?kind=...&a=...&b=...`). Hosts use this to route to a
   * different URL — e.g. Radar Hub's `/fleet/compare` with cluster IDs.
   * Picks arrive in click order, each carrying `clusterId`/`clusterName`
   * when `resolveRowCluster` is set (which is what keeps cross-cluster
   * picks distinct in the equality check).
   */
  onCompareSubmit?: (
    picks: NamespacedRef[],
    kind: { name: string; group?: string },
  ) => void
  /**
   * Resolves the cluster scope for a row when compare-mode is enabled.
   * Stamps `clusterId`/`clusterName` onto each pick so the same `ns/name`
   * in two different clusters is treated as two distinct picks. Hub-web
   * returns `row._cluster` here; OSS leaves it unset and picks key on
   * namespace+name only.
   */
  resolveRowCluster?: (resource: any) => { id: string; name: string } | undefined
  /**
   * Clears the global namespace selection (the header NamespaceSwitcher state).
   * When wired, the "Clear filters" button also drops the active namespaces;
   * otherwise it only resets the view-local filter state. Host-owned because
   * the switcher lives outside this component and may persist server-side.
   */
  onClearNamespaces?: () => void
  // Bulk operations
  onBulkDelete?: (items: BulkResourceItem[], options?: { force?: boolean; onSuccess?: () => void }) => void
  isBulkDeleting?: boolean
  onBulkRestart?: (items: BulkResourceItem[], options?: { onSuccess?: () => void }) => void
  isBulkRestarting?: boolean
  onBulkScale?: (items: BulkResourceItem[], replicas: number, options?: { onSuccess?: () => void }) => void
  isBulkScaling?: boolean
}

// Default selected kind
const DEFAULT_KIND_INFO: SelectedKindInfo = { name: 'pods', kind: 'Pod', group: '' }
export const LOADED_RESOURCE_COUNT_TTL_MS = 5 * 60 * 1000

interface LoadedResourceCount {
  count: number
  expiresAt: number
}

type LoadedResourceCountCache = Record<string, LoadedResourceCount>

function resourceCountKey(resource: Pick<APIResource, 'group' | 'kind'>): string {
  return resource.group ? `${resource.group}/${resource.kind}` : resource.kind
}

export function resourceQueryMatchesSelectedKind(
  query: Pick<ResourceQueryResult, 'resourceName' | 'group'> | undefined,
  selectedKind: SelectedKindInfo,
): boolean {
  if (!query?.resourceName) return true
  return query.resourceName === selectedKind.name && (query.group ?? '') === selectedKind.group
}

// Read initial state from URL — kind is in the path: {basePath}/{kind}
//
// Hosts that own their own URL shape (an embedding host with custom
// routes outside the `{basePath}/{kind}` convention) pass a synthetic
// pathname/search via the locationPathname/locationSearch props. When
// those are provided, prefer them over window.location — otherwise the
// host's URL wouldn't resolve against the synthetic basePath and we'd
// fall through to DEFAULT_KIND.
function getInitialKindFromURL(
  basePath: string = '/resources',
  defaultKind: SelectedKindInfo = DEFAULT_KIND_INFO,
  locationPathname?: string,
  locationSearch?: string,
): SelectedKindInfo {
  // Prefer injected pathname/search from the host router. Using `||` would incorrectly fall back to
  // window when the host passes '' before hydration. SSR has no window.
  const pathname =
    locationPathname !== undefined
      ? locationPathname
      : typeof window !== 'undefined'
        ? window.location.pathname
        : ''
  const search =
    locationSearch !== undefined
      ? locationSearch
      : typeof window !== 'undefined'
        ? window.location.search
        : ''
  const base = basePath.replace(/\/$/, '') // strip trailing slash
  let kind: string | null = null
  if (pathname.startsWith(base + '/')) {
    kind = pathname.slice(base.length + 1).split('/')[0] || null
  }
  const group = new URLSearchParams(search).get('apiGroup') || ''
  if (kind) {
    const coreMatch = findAPIResourceForRoute(undefined, kind, group)
    if (coreMatch) {
      return { name: coreMatch.name, kind: coreMatch.kind, group: coreMatch.group }
    }
    return { name: kind, kind: kind, group }
  }
  return defaultKind
}

export function canonicalizeSelectedKind(
  selectedKind: SelectedKindInfo,
  resourcesToCount: Array<Pick<APIResource, 'name' | 'kind' | 'group'>>,
  apiResources?: APIResource[],
): SelectedKindInfo | null {
  const nameMatch = resourcesToCount.find(r =>
    r.name === selectedKind.name && r.group === selectedKind.group
  )
  if (nameMatch) {
    return selectedKind.kind === nameMatch.kind
      ? null
      : { name: nameMatch.name, kind: nameMatch.kind, group: nameMatch.group }
  }

  const match = apiResources?.find(r =>
    r.kind === selectedKind.kind && r.group === selectedKind.group
  )
  return match ? { name: match.name, kind: match.kind, group: match.group } : null
}

export function deriveSidebarResourceCounts(
  resourcesToCount: Array<Pick<APIResource, 'kind' | 'group'>>,
  resourceCounts: Record<string, number | null> | undefined,
  resourceUnavailable: string[] | undefined,
  loadedCountCache: LoadedResourceCountCache,
  now: number = Date.now(),
): Record<string, number | null> {
  const unavailableKinds = new Set(resourceUnavailable ?? [])
  const results: Record<string, number | null> = {}

  for (const resource of resourcesToCount) {
    const key = resourceCountKey(resource)
    const cached = loadedCountCache[key]
    if (resourceCounts && key in resourceCounts) {
      results[key] = resourceCounts[key] ?? null
    } else if (cached && cached.expiresAt > now) {
      results[key] = cached.count
    } else if (unavailableKinds.has(key)) {
      results[key] = null
    } else {
      results[key] = null
    }
  }

  return results
}

// Get initial filters from URL
function getInitialFiltersFromURL() {
  const params = new URLSearchParams(window.location.search)
  // Parse generic column filters
  const filtersParam = params.get('filters')
  const columnFilters = parseColumnFilters(filtersParam)
  const columnFilterExcludes = parseColumnFilterExcludes(filtersParam)
  const result = {
    search: params.get('search') || '',
    regex: params.get('regex') === 'true',
    columnFilters,
    columnFilterExcludes,
    problemFilters: params.get('problems')?.split(',').filter(Boolean) || [],
    showInactive: params.get('showInactive') === 'true',
    labelSelector: params.get('labels') || '', // e.g., "app=caretta,version=v1"
    ownerKind: params.get('ownerKind') || '', // e.g., "DaemonSet"
    ownerName: params.get('ownerName') || '', // e.g., "app-caretta"
  }
  return result
}

// Sort state type
type SortDirection = 'asc' | 'desc' | null

export function ResourcesView({
  namespaces, selectedResource, onResourceClick, onResourceClickYaml, onKindChange,
  apiResources: apiResourcesProp,
  resourceQueries: resourceQueriesProp,
  resourceCounts: resourceCountsProp,
  resourceForbidden: resourceForbiddenProp,
  resourceReasons,
  resourceUnavailable: resourceUnavailableProp,
  selectedKindQuery: selectedKindQueryProp,
  connectionState,
  largeListGuard,
  topPodMetrics,
  topNodeMetrics,
  certExpiry,
  certExpiryError,
  auditBadges,
  pinned = [],
  togglePin = () => {},
  isPinned = () => false,
  locationSearch,
  locationPathname,
  onNavigate,
  basePath = '/resources',
  onOpenLogs,
  onOpenWorkloadLogs,
  onSelectedKindChange,
  hideSidebar = false,
  onCreateResource,
  defaultKind = DEFAULT_KIND_INFO,
  extraLeadingColumns,
  printerTable,
  onRowSelect,
  rowHrefFor,
  onCompareSubmit,
  resolveRowCluster,
  onClearNamespaces,
  onBulkDelete,
  isBulkDeleting = false,
  onBulkRestart,
  isBulkRestarting = false,
  onBulkScale,
  isBulkScaling = false,
}: ResourcesViewProps) {
  const initialFilters = getInitialFiltersFromURL()
  const [selectedKind, setSelectedKind] = useState<SelectedKindInfo>(() => getInitialKindFromURL(basePath, defaultKind, locationPathname, locationSearch))
  // Sync selectedKind from URL when the URL changes (browser back, external sidebar navigation).
  // Deps are URL-derived only — including selectedKind.name/group would race against pending
  // navigation: a sidebar click flips state before navigate() lands, this effect re-reads the
  // stale URL, and reverts the kind. The window into a stale URL between state change and URL
  // update is what produced the "blink and fail to navigate" bug.
  useEffect(() => {
    const kindFromURL = getInitialKindFromURL(basePath, defaultKind, locationPathname, locationSearch)
    setSelectedKind((prev) =>
      kindFromURL.name !== prev.name || kindFromURL.group !== prev.group ? kindFromURL : prev,
    )
  }, [basePath, defaultKind, locationPathname, locationSearch])
  // Notify parent of selected kind changes (including initial mount)
  useEffect(() => {
    onSelectedKindChange?.(selectedKind)
    setBulkMode(false)
    setCheckedResources(new Set())
    setShowBulkDeleteConfirm(false)
    setShowBulkRestartConfirm(false)
    setShowBulkScaleDialog(false)
    setBulkForceDelete(false)
  }, [selectedKind.name, selectedKind.group]) // eslint-disable-line react-hooks/exhaustive-deps
  const [searchTerm, setSearchTerm] = useState(initialFilters.search)
  // Typing must never wait on the list or the router. The input renders raw
  // searchTerm; filtering consumes the deferred copy (React yields to keep
  // keystrokes responsive on large lists); the URL write consumes the
  // debounced copy (one history entry per pause, not one per keystroke —
  // per-keystroke navigations re-render the whole route tree and, when
  // renders lag, the URL→state sync effect could revert in-flight input).
  // Clearing skips the debounce so the × cleans the URL immediately.
  const deferredSearchTerm = useDeferredValue(searchTerm)
  const debouncedSearchTerm = useDebouncedValue(searchTerm, 300, (v) => v === '')
  const [regexMode, setRegexMode] = useState(initialFilters.regex)
  const [sortColumn, setSortColumn] = useState<string | null>(null)
  const [sortDirection, setSortDirection] = useState<SortDirection>(null)
  // Filter state
  const [columnFilters, setColumnFilters] = useState<Record<string, string[]>>(initialFilters.columnFilters)
  const [columnFilterExcludes, setColumnFilterExcludes] = useState<Record<string, boolean>>(initialFilters.columnFilterExcludes)
  const [problemFilters, setProblemFilters] = useState<string[]>(initialFilters.problemFilters)
  const [openColumnFilter, setOpenColumnFilter] = useState<string | null>(null)
  const [columnFilterSearch, setColumnFilterSearch] = useState('')
  const columnFilterDropdownRef = useRef<HTMLDivElement>(null)
  const [showProblemsDropdown, setShowProblemsDropdown] = useState(false)
  const problemsDropdownRef = useRef<HTMLDivElement>(null)
  const [showLabelsDropdown, setShowLabelsDropdown] = useState(false)
  const [labelSearch, setLabelSearch] = useState('')
  const labelsDropdownRef = useRef<HTMLDivElement>(null)
  // ReplicaSet-specific: hide inactive by default
  const [showInactiveReplicaSets, setShowInactiveReplicaSets] = useState(initialFilters.showInactive)
  // Column visibility and resize state
  const [visibleColumns, setVisibleColumns] = useState<Set<string>>(new Set())
  const [columnWidths, setColumnWidths] = useState<Record<string, number>>({})
  const [showColumnPicker, setShowColumnPicker] = useState(false)
  const [customColumns, setCustomColumns] = useState<CustomColumnDef[]>([])
  const [customColumnDraft, setCustomColumnDraft] = useState<CustomColumnDef>({ source: 'label', path: '' })
  // Per-instance so two ResourcesView tables on one page (e.g. fleet compare)
  // don't share a datalist id and cross-suggest each other's keys.
  const customColKeysListId = useId()
  const columnPickerRef = useRef<HTMLDivElement>(null)
  // Label/owner filtering for deep-linking from workload details
  const [labelSelector, setLabelSelector] = useState<string>(initialFilters.labelSelector)
  const [ownerKind, setOwnerKind] = useState<string>(initialFilters.ownerKind)
  const [ownerName, setOwnerName] = useState<string>(initialFilters.ownerName)

  // Multi-select state for bulk operations. Checkboxes only render while
  // bulk mode is active — entered via the toolbar toggle — so mutating
  // actions stay out of the way during normal browsing.
  const [bulkMode, setBulkMode] = useState(false)
  const [checkedResources, setCheckedResources] = useState<Set<string>>(new Set())
  const [showBulkDeleteConfirm, setShowBulkDeleteConfirm] = useState(false)
  const [showBulkRestartConfirm, setShowBulkRestartConfirm] = useState(false)
  const [showBulkScaleDialog, setShowBulkScaleDialog] = useState(false)
  const [bulkForceDelete, setBulkForceDelete] = useState(false)
  const [bulkScaleReplicas, setBulkScaleReplicas] = useState(0)

  const exitBulkMode = useCallback(() => {
    setBulkMode(false)
    setCheckedResources(new Set())
  }, [])

  // Column filter helpers
  const clearColumnFilter = useCallback((key: string) => {
    setColumnFilters(prev => {
      const next = { ...prev }
      delete next[key]
      return next
    })
    setColumnFilterExcludes(prev => {
      if (!prev[key]) return prev
      const next = { ...prev }
      delete next[key]
      return next
    })
  }, [])

  const setColumnFilterMode = useCallback((key: string, exclude: boolean) => {
    setColumnFilterExcludes(prev => {
      if (Boolean(prev[key]) === exclude) return prev
      const next = { ...prev }
      if (exclude) {
        next[key] = true
      } else {
        delete next[key]
      }
      return next
    })
  }, [])

  const toggleColumnFilterValue = useCallback((key: string, value: string) => {
    setColumnFilters(prev => {
      const next = { ...prev }
      const current = next[key] || []
      const idx = current.indexOf(value)
      if (idx >= 0) {
        const updated = current.filter(v => v !== value)
        if (updated.length === 0) {
          delete next[key]
          // The operator is meaningless with no selected values — drop it so a
          // stale exclude flag doesn't linger in state or re-arm on reselect.
          setColumnFilterExcludes(inv => {
            if (!inv[key]) return inv
            const nextInv = { ...inv }
            delete nextInv[key]
            return nextInv
          })
        } else {
          next[key] = updated
        }
      } else {
        next[key] = [...current, value]
      }
      return next
    })
  }, [])

  // Track if this is the initial mount to avoid re-syncing on first render
  const isInitialMount = useRef(true)
  const isSyncingFromURL = useRef(false)
  // Track whether the initial mount effect has processed the ?resource= param
  const hasProcessedInitialResource = useRef(false)
  // Set by sidebar kind change to push a browser history entry (vs replace for filter changes)
  const shouldPushHistory = useRef(false)
  // Used by the URL-write effect to distinguish drawer-to-drawer navigation (A -> B, push)
  // from initial open (null -> X) and close (X -> null), which stay as URL replaces.
  const prevSelectedResourceRef = useRef<SelectedResource | null>(null)

  // Ref to search input for keyboard shortcut
  const searchInputRef = useRef<HTMLInputElement>(null)
  // Resize state
  const resizingColumn = useRef<string | null>(null)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(0)
  const [resizeLineX, setResizeLineX] = useState<number | null>(null)
  const tableContainerRef = useRef<HTMLDivElement>(null)

  // Bulk metrics for table columns — provided via props
  const metricsLookup = useMemo<MetricsLookup>(() => {
    const pods = new Map<string, TopPodMetrics>()
    const nodes = new Map<string, TopNodeMetrics>()
    if (topPodMetrics) {
      for (const m of topPodMetrics) {
        pods.set(`${m.namespace}/${m.name}`, m)
      }
    }
    if (topNodeMetrics) {
      for (const m of topNodeMetrics) {
        nodes.set(m.name, m)
      }
    }
    return { pods, nodes }
  }, [topPodMetrics, topNodeMetrics])

  // User-defined label/annotation columns materialized as self-contained
  // ExtraColumns — appended after the built-ins and wired through the same
  // render/sort/filter override rails (extraColumnsByKey).
  const builtCustomColumns = useMemo(() => customColumns.map(buildCustomColumn), [customColumns])
  const customColumnKeySet = useMemo(() => new Set(builtCustomColumns.map(c => c.key)), [builtCustomColumns])

  // Prepend extraLeadingColumns (host-injected leading columns) before
  // the kind-specific KNOWN_COLUMNS entries. When extras are undefined,
  // this collapses to single-cluster behavior. Built-in keys win on
  // collision: a colliding extra is filtered out (with a dev-mode warn)
  // so we don't render two columns sharing one key — that would yield
  // duplicate React keys and corrupt visibleColumns / columnWidths state.
  const builtPrinterColumns = useMemo(
    () => buildPrinterColumns(selectedKind.name, selectedKind.group, printerTable),
    [selectedKind.name, selectedKind.group, printerTable],
  )
  const printerColumnsKey = useMemo(
    () => printerTableKey(builtPrinterColumns.length ? printerTable : null),
    [printerTable, builtPrinterColumns],
  )

  const allColumns = useMemo(() => {
    const kindColumns = columnsForKindWithPrinter(selectedKind.name, selectedKind.group, builtPrinterColumns)
    if (!extraLeadingColumns?.length && !builtCustomColumns.length) return kindColumns
    const builtinKeys = new Set(kindColumns.map(c => c.key))
    const filteredExtras = filterHostExtras(extraLeadingColumns, builtinKeys, selectedKind.name)
    return [...filteredExtras, ...kindColumns, ...builtCustomColumns]
  }, [selectedKind.name, selectedKind.group, extraLeadingColumns, builtCustomColumns, builtPrinterColumns])

  // Map of extra column keys for fast O(1) lookup on each render path
  // (cell render, sort, column-filter unique-values).
  // Recompute header-derived widths once the web font swaps in (see watchHeaderFont).
  const headerFontEpoch = useHeaderFontEpoch()

  const extraColumnsByKey = useMemo(() => {
    const m = new Map<string, ExtraColumn>()
    extraLeadingColumns?.forEach(c => m.set(c.key, c))
    builtPrinterColumns.forEach(c => m.set(c.key, c))
    builtCustomColumns.forEach(c => m.set(c.key, c))
    return m
  }, [extraLeadingColumns, builtCustomColumns, builtPrinterColumns])

  // Guards the save effect from persisting on the initial load of each kind
  // (set false by the load effect, flipped true on its first skipped save).
  const isColumnSettingsLoaded = useRef(false)
  // Whether the user has a persisted column blob for this kind — data-driven
  // column defaults (GPU auto-show) must never override explicit user choices.
  const hadSavedColumnSettings = useRef(false)
  // Kinds where GPU auto-show already fired this mount. Firing is one-shot:
  // without this, hiding the auto-shown column re-triggers the effect (it
  // depends on visibleColumns) and instantly reverts the user's hide.
  const gpuAutoShownKinds = useRef<Set<string>>(new Set())
  useEffect(() => {
    // Re-arm the skip-initial-save guard per kind. This effect repopulates
    // visible/widths/custom for the new kind via async setState, so the save
    // effect (also keyed to selectedKind) that runs in this same commit still
    // sees the PREVIOUS kind's columns in state and would persist them under
    // the new kind's storage key. Skipping the first save after each load
    // prevents that cross-kind write.
    isColumnSettingsLoaded.current = false
    const saved = loadColumnSettings(selectedKind.name, selectedKind.group)
    hadSavedColumnSettings.current = !!saved
    // Reconstruct the effective column list locally rather than reading
    // `allColumns` — keying this effect to the kind (not allColumns) keeps
    // runtime custom-column add/remove from re-running it and clobbering the
    // in-session edit (and avoids a setCustomColumns → allColumns → re-run loop).
    // sanitize guards the localStorage boundary: a corrupted/hand-edited blob
    // (non-array, blank path, bad source) must not crash the later .map or add dead columns.
    const savedCustom = sanitizeCustomColumnDefs(saved?.custom)
    setCustomColumns(savedCustom)
    const kindColumns = columnsForKindWithPrinter(selectedKind.name, selectedKind.group, builtPrinterColumns)
    const builtinKeys = new Set(kindColumns.map(c => c.key))
    const extras = filterHostExtras(extraLeadingColumns, builtinKeys, selectedKind.name)
    const effective = [...extras, ...kindColumns, ...savedCustom.map(buildCustomColumn)]
    // Host-injected extra columns (e.g. fleet Cluster) default to visible
    // even when the saved column-visibility blob predates them — a naive
    // Set(saved.visible) would silently hide host columns the user has
    // never been shown. Trade-off: a user can't permanently hide a host
    // extra column via the column picker — next mount re-adds it. Track
    // a sibling "hidden-extras" set if this becomes a real complaint.
    const extraKeys = extras.map(c => c.key)
    if (saved) {
      // If saved columns are just the defaults but this kind has specialized columns,
      // discard the stale save and use the specialized columns instead. User-added
      // custom columns mean the blob isn't a stale pure-defaults save — keep it, or
      // the migration would clear them from storage (and un-hide hidden ones).
      const defaultKeys = DEFAULT_COLUMNS.map(c => c.key)
      const isStaleDefaults = kindColumns !== DEFAULT_COLUMNS &&
        !savedCustom.length &&
        saved.visible.length === defaultKeys.length &&
        saved.visible.every(v => defaultKeys.includes(v))
      if (isStaleDefaults) {
        clearColumnSettings(selectedKind.name, selectedKind.group)
        hadSavedColumnSettings.current = false
        setVisibleColumns(getDefaultVisibleColumns(effective))
        setColumnWidths({})
      } else {
        setVisibleColumns(mergeSavedVisibleColumns(saved.visible, extraKeys, builtPrinterColumns))
        setColumnWidths(saved.widths || {})
      }
    } else {
      setVisibleColumns(getDefaultVisibleColumns(effective))
      setColumnWidths({})
    }
    // printerColumnsKey, not builtPrinterColumns: the built array is a new
    // identity on every refetch, and re-running this effect would reset the
    // user's in-session column choices each time the list polls. The key only
    // changes when the kind changes or an operator upgrade changes the CRD.
  }, [selectedKind.name, selectedKind.group, extraLeadingColumns, printerColumnsKey])

  // Save column settings when they change (skip the initial load of each kind)
  useEffect(() => {
    if (visibleColumns.size === 0) return // not loaded yet
    if (!isColumnSettingsLoaded.current) {
      isColumnSettingsLoaded.current = true
      return
    }
    saveColumnSettings(selectedKind.name, selectedKind.group, {
      visible: Array.from(visibleColumns),
      widths: columnWidths,
      custom: customColumns,
    })
    // A persisted blob blocks GPU auto-show from here on — same rule in-session
    // as across sessions, so a later data refresh can't override user choices.
    hadSavedColumnSettings.current = true
  }, [visibleColumns, columnWidths, customColumns, selectedKind.name, selectedKind.group])

  // Close column picker on outside click or Escape
  useEffect(() => {
    if (!showColumnPicker) return
    const handleClick = (e: MouseEvent) => {
      if (columnPickerRef.current && !columnPickerRef.current.contains(e.target as Node)) {
        setShowColumnPicker(false)
      }
    }
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.stopPropagation(); setShowColumnPicker(false) }
    }
    document.addEventListener('mousedown', handleClick)
    document.addEventListener('keydown', handleKey, true)
    return () => { document.removeEventListener('mousedown', handleClick); document.removeEventListener('keydown', handleKey, true) }
  }, [showColumnPicker])

  // Whether any columns have been resized (triggers switch to fixed grid sizes + spacer)
  const hasResizedColumns = Object.keys(columnWidths).length > 0

  // Column resize handlers
  const handleResizeStart = useCallback((e: React.MouseEvent, colKey: string, currentWidth: number) => {
    e.preventDefault()
    e.stopPropagation()
    resizingColumn.current = colKey
    resizeStartX.current = e.clientX
    resizeStartWidth.current = currentWidth

    // On first resize, snapshot all column widths from the DOM to switch to fixed grid sizes
    const container = tableContainerRef.current
    setColumnWidths(prev => {
      const hasWidths = Object.keys(prev).length > 0
      if (hasWidths) return prev
      // Snapshot all visible <th> widths
      const ths = container?.querySelectorAll('thead th')
      if (!ths) return prev
      const snapped: Record<string, number> = {}
      const cols = allColumns.filter(c => visibleColumns.has(c.key))
      ths.forEach((th, i) => {
        if (cols[i]) {
          snapped[cols[i].key] = th.getBoundingClientRect().width
        }
      })
      return snapped
    })

    // Show the resize line at the initial position
    const containerRect = container?.getBoundingClientRect()
    if (containerRect) {
      setResizeLineX(e.clientX - containerRect.left + (container?.scrollLeft ?? 0))
    }

    const handleMouseMove = (me: MouseEvent) => {
      if (!resizingColumn.current) return
      const diff = me.clientX - resizeStartX.current

      // Allow shrinking to a small minimum — content truncates with ellipsis
      const minW = 48

      const newWidth = Math.max(minW, resizeStartWidth.current + diff)
      setColumnWidths(prev => ({ ...prev, [resizingColumn.current!]: newWidth }))
      // Update resize line position, clamped to the constrained width
      const rect = container?.getBoundingClientRect()
      if (rect) {
        const clampedClientX = resizeStartX.current + (newWidth - resizeStartWidth.current)
        setResizeLineX(clampedClientX - rect.left + (container?.scrollLeft ?? 0))
      }
    }

    const handleMouseUp = () => {
      resizingColumn.current = null
      setResizeLineX(null)
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      // Suppress the click event that fires after mouseup to prevent accidental sort toggle
      document.addEventListener('click', (e) => e.stopPropagation(), { capture: true, once: true })
    }

    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
  }, [allColumns, visibleColumns])

  // Toggle column visibility
  const toggleColumnVisibility = useCallback((colKey: string) => {
    setVisibleColumns(prev => {
      const next = new Set(prev)
      if (next.has(colKey)) {
        next.delete(colKey)
      } else {
        next.add(colKey)
      }
      return next
    })
  }, [])

  // Reset column settings to defaults (also drops user-added custom columns)
  const resetColumnSettings = useCallback(() => {
    clearColumnSettings(selectedKind.name, selectedKind.group)
    setCustomColumns([])
    const customKeys = new Set(builtCustomColumns.map(c => c.key))
    setVisibleColumns(getDefaultVisibleColumns(allColumns.filter(c => !customKeys.has(c.key))))
    setColumnWidths({})
    isColumnSettingsLoaded.current = false
    // Back to data-driven defaults: re-arm GPU auto-show for this kind.
    hadSavedColumnSettings.current = false
    gpuAutoShownKinds.current.delete(selectedKind.name.toLowerCase())
  }, [selectedKind.name, selectedKind.group, allColumns, builtCustomColumns])

  // Add a user-defined label/annotation column (dedupe by key) and show it.
  const addCustomColumn = useCallback((def: CustomColumnDef) => {
    const path = def.path.trim()
    if (!path) return
    const key = customColumnKey({ ...def, path })
    setCustomColumns(prev => prev.some(c => customColumnKey(c) === key) ? prev : [...prev, { ...def, path }])
    setVisibleColumns(prev => {
      if (prev.has(key)) return prev
      const next = new Set(prev)
      next.add(key)
      return next
    })
  }, [])

  // Remove a user-defined column entirely (not just hide it).
  const removeCustomColumn = useCallback((key: string) => {
    setCustomColumns(prev => prev.filter(c => customColumnKey(c) !== key))
    setVisibleColumns(prev => {
      if (!prev.has(key)) return prev
      const next = new Set(prev)
      next.delete(key)
      return next
    })
    // Drop any filter/sort that targeted the removed column — otherwise the
    // table keeps filtering (and syncing to the URL) on a key with no column
    // and no UI to clear it, and sort silently references a gone column.
    setColumnFilters(prev => {
      if (!(key in prev)) return prev
      const next = { ...prev }
      delete next[key]
      return next
    })
    setColumnFilterExcludes(prev => {
      if (!(key in prev)) return prev
      const next = { ...prev }
      delete next[key]
      return next
    })
    if (sortColumn === key) {
      setSortColumn(null)
      setSortDirection(null)
    }
  }, [sortColumn])

  // Keyboard shortcut: / to focus search
  useRegisterShortcut({
    id: 'resources-search',
    keys: '/',
    description: 'Focus search',
    category: 'Search',
    scope: 'resources',
    handler: () => searchInputRef.current?.focus(),
  })

  // Keyboard navigation: highlighted row state
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const virtuosoRef = useRef<TableVirtuosoHandle>(null)

  // Compare mode is only meaningful when the host can route picks. Either
  // path qualifies: onNavigate for the default same-cluster `/compare?...`
  // URL, or onCompareSubmit for hosts that build their own URL (e.g.
  // Radar Hub's `/fleet/compare` with cluster IDs).
  const compareEnabled = !!onNavigate || !!onCompareSubmit
  const [compareMode, setCompareMode] = useState(false)
  const [comparePicks, setComparePicks] = useState<CompareTrayPick[]>([])

  // Kind change invalidates picks AND mode — leaving the tray on with empty
  // pills after the user moves to a different kind is more confusing than helpful.
  useEffect(() => {
    setComparePicks([])
    setCompareMode(false)
  }, [selectedKind.name, selectedKind.group])

  const exitCompareMode = useCallback(() => {
    setCompareMode(false)
    setComparePicks([])
  }, [])

  const toggleComparePick = useCallback((resource: any) => {
    if (!resource?.metadata?.name) return
    const ns = resource.metadata.namespace || ''
    // Stamp clusterId on the pick when the host provides a resolver
    // (cross-cluster compare). Without it, two rows with the same ns+name
    // from different clusters would collapse into one pick.
    const cluster = resolveRowCluster?.(resource)
    setComparePicks(prev => togglePick(prev, {
      namespace: ns,
      name: resource.metadata.name,
      clusterId: cluster?.id,
      clusterName: cluster?.name,
    }))
  }, [resolveRowCluster])

  const handleCompareNavigate = useCallback(() => {
    if (comparePicks.length !== 2) return
    if (onCompareSubmit) {
      onCompareSubmit(comparePicks, { name: selectedKind.name, group: selectedKind.group })
      return
    }
    if (!onNavigate) return
    const params = new URLSearchParams()
    params.set('kind', selectedKind.name)
    if (selectedKind.group) params.set('apiGroup', selectedKind.group)
    params.set('a', refToParam(comparePicks[0]))
    params.set('b', refToParam(comparePicks[1]))
    onNavigate(`/compare?${params.toString()}`)
  }, [comparePicks, selectedKind.name, selectedKind.group, onNavigate, onCompareSubmit])

  // Reset highlight when kind, search, sort, or namespace changes
  const namespacesKey = namespaces.join(',')
  useEffect(() => { setHighlightedIndex(-1) }, [selectedKind.name, searchTerm, regexMode, sortColumn, sortDirection, namespacesKey])

  // Scroll highlighted row into view
  useEffect(() => {
    if (highlightedIndex >= 0) {
      virtuosoRef.current?.scrollToIndex({ index: highlightedIndex, align: 'center', behavior: 'auto' })
    }
  }, [highlightedIndex])

  // Open logs for pod / workload resources — provided via props
  const openLogs = onOpenLogs
  const openWorkloadLogs = onOpenWorkloadLogs

  // Helper: get resource at highlighted index
  const getHighlightedResource = useCallback(() => {
    // filteredResources is computed later in the component — use a ref to access it
    return highlightedResourceRef.current
  }, [])

  // Selection handler shared by row click, Enter / `d` shortcuts, and the
  // search-Enter path. Honors the onRowSelect escape hatch when set so
  // full-page-nav consumers stay consistent across activation paths;
  // otherwise falls through to the drawer-pattern onResourceClick with
  // the stripped {kind, namespace, name, group} shape.
  // (Distinct from `y` / `l` shortcuts, which open YAML / logs rather
  // than "select the row" — those route to their own callbacks.)
  const selectResource = useCallback((resource: any, isSelected = false) => {
    if (!resource?.metadata?.name) return
    if (onRowSelect) {
      onRowSelect(resource)
      return
    }
    const stripped = {
      kind: selectedKind.name,
      namespace: resource.metadata.namespace || '',
      name: resource.metadata.name,
      group: selectedKind.group,
    }
    onResourceClick?.(isSelected ? null : stripped)
  }, [onRowSelect, onResourceClick, selectedKind.name, selectedKind.group])

  // Register navigation shortcuts
  useRegisterShortcuts([
    {
      id: 'resources-nav-down',
      keys: 'j',
      description: 'Next row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => {
        const max = filteredResourceCountRef.current - 1
        return i < max ? i + 1 : i
      }),
    },
    {
      id: 'resources-nav-down-arrow',
      keys: 'ArrowDown',
      description: 'Next row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => {
        const max = filteredResourceCountRef.current - 1
        return i < max ? i + 1 : i
      }),
    },
    {
      id: 'resources-nav-up',
      keys: 'k',
      description: 'Previous row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => i > 0 ? i - 1 : 0),
    },
    {
      id: 'resources-nav-up-arrow',
      keys: 'ArrowUp',
      description: 'Previous row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(i => i > 0 ? i - 1 : 0),
    },
    {
      id: 'resources-nav-top',
      keys: 'g g',
      description: 'Jump to first row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(0),
    },
    {
      id: 'resources-nav-bottom',
      keys: 'G',
      description: 'Jump to last row',
      category: 'Table',
      scope: 'resources',
      handler: () => setHighlightedIndex(Math.max(0, filteredResourceCountRef.current - 1)),
    },
    {
      id: 'resources-open',
      keys: 'Enter',
      description: 'Open resource detail',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (compareMode) toggleComparePick(res)
        else selectResource(res)
      },
      enabled: highlightedIndex >= 0,
    },
    {
      id: 'resources-open-detail',
      keys: 'd',
      description: 'Open resource detail',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (compareMode) toggleComparePick(res)
        else selectResource(res)
      },
      enabled: highlightedIndex >= 0,
    },
    {
      id: 'resources-open-yaml',
      keys: 'y',
      description: 'Open YAML view',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (!res?.metadata?.name) return
        const cb = onResourceClickYaml || onResourceClick
        cb?.({ kind: selectedKind.name, namespace: res.metadata.namespace || '', name: res.metadata.name, group: selectedKind.group })
      },
      enabled: highlightedIndex >= 0 && !compareMode,
    },
    {
      id: 'resources-logs',
      keys: 'l',
      description: 'Open logs',
      category: 'Resource Actions',
      scope: 'resources',
      handler: () => {
        const res = getHighlightedResource()
        if (!res) return
        const kindLower = selectedKind.name.toLowerCase()
        const ns = res.metadata?.namespace || ''
        const name = res.metadata?.name || ''
        if (kindLower === 'pods') {
          // For pods, use openLogs directly (not workload logs)
          const containers = (res.spec?.containers || []).map((c: { name: string }) => c.name)
          if (containers.length > 0) {
            openLogs?.({ namespace: ns, podName: name, containers })
          }
        } else if (['deployments', 'statefulsets', 'daemonsets', 'replicasets', 'workflows', 'cronjobs', 'cronworkflows', 'workflowtemplates', 'clusterworkflowtemplates', 'scaledjobs'].includes(kindLower) || isCoreBatchJob(kindLower, selectedKind.group)) {
          openWorkloadLogs?.({ namespace: ns, workloadKind: selectedKind.kind, workloadName: name })
        }
      },
      enabled: highlightedIndex >= 0 && !compareMode && (
        ['pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets', 'workflows', 'cronjobs', 'cronworkflows', 'workflowtemplates', 'clusterworkflowtemplates', 'scaledjobs'].includes(selectedKind.name.toLowerCase()) ||
        isCoreBatchJob(selectedKind.name, selectedKind.group)
      ),
    },
    {
      id: 'resources-sort-name',
      keys: 'N',
      description: 'Sort by name',
      category: 'Table',
      scope: 'resources',
      handler: () => handleSort('name'),
    },
    {
      id: 'resources-sort-age',
      keys: 'A',
      description: 'Sort by age',
      category: 'Table',
      scope: 'resources',
      handler: () => handleSort('age'),
    },
    {
      id: 'resources-sort-status',
      keys: 'S',
      description: 'Sort by status',
      category: 'Table',
      scope: 'resources',
      handler: () => handleSort('status'),
    },
    {
      id: 'resources-bulk-select',
      keys: 'b',
      description: 'Toggle bulk select',
      category: 'Table',
      scope: 'resources',
      handler: () => {
        if (bulkMode) { exitBulkMode(); return }
        if (!canBulkSelect) return
        exitCompareMode()
        setBulkMode(true)
      },
    },
    {
      id: 'resources-clear-highlight',
      keys: 'Escape',
      description: 'Clear highlight / blur search',
      category: 'Table',
      scope: 'resources',
      handler: () => {
        if (showColumnPicker) { setShowColumnPicker(false); return }
        if (openColumnFilter) { setOpenColumnFilter(null); return }
        if (showProblemsDropdown) { setShowProblemsDropdown(false); return }
        if (showLabelsDropdown) { setShowLabelsDropdown(false); return }
        if (compareMode) { exitCompareMode(); return }
        if (bulkMode) { exitBulkMode(); return }
        if (highlightedIndex >= 0) setHighlightedIndex(-1)
        else searchInputRef.current?.blur()
      },
    },
  ])

  // Refs for accessing filteredResources inside shortcuts (computed later in component)
  const filteredResourceCountRef = useRef(0)
  const highlightedResourceRef = useRef<any>(null)

  // Ref for flat kind list used by [ / ] sidebar navigation (populated from categories)
  const flatKindListRef = useRef<SelectedKindInfo[]>([])

  // Sidebar kind navigation: [ = previous kind, ] = next kind
  useRegisterShortcuts([
    {
      id: 'resources-prev-kind',
      keys: '[',
      description: 'Previous resource kind',
      category: 'Table',
      scope: 'resources',
      handler: () => {
        const list = flatKindListRef.current
        if (list.length === 0) return
        const idx = list.findIndex(k => k.name === selectedKind.name && k.group === selectedKind.group)
        const prev = idx > 0 ? list[idx - 1] : list[list.length - 1]
        shouldPushHistory.current = true
        setSelectedKind(prev)
        onKindChange?.()
      },
    },
    {
      id: 'resources-next-kind',
      keys: ']',
      description: 'Next resource kind',
      category: 'Table',
      scope: 'resources',
      handler: () => {
        const list = flatKindListRef.current
        if (list.length === 0) return
        const idx = list.findIndex(k => k.name === selectedKind.name && k.group === selectedKind.group)
        const next = idx < list.length - 1 ? list[idx + 1] : list[0]
        shouldPushHistory.current = true
        setSelectedKind(next)
        onKindChange?.()
      },
    },
  ])

  // Close dropdowns on outside click
  useEffect(() => {
    const anyOpen = showProblemsDropdown || showLabelsDropdown || openColumnFilter
    if (!anyOpen) return
    const handleClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement
      if (showProblemsDropdown && problemsDropdownRef.current && !problemsDropdownRef.current.contains(target)) {
        setShowProblemsDropdown(false)
      }
      if (showLabelsDropdown && labelsDropdownRef.current && !labelsDropdownRef.current.contains(target)) {
        setShowLabelsDropdown(false)
      }
      if (openColumnFilter && !target.closest('[data-column-filter-trigger]') && columnFilterDropdownRef.current && !columnFilterDropdownRef.current.contains(target)) {
        setOpenColumnFilter(null)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [showProblemsDropdown, showLabelsDropdown, openColumnFilter])

  // Sync state from URL when navigation occurs (e.g., deep linking from WorkloadRenderer)
  useEffect(() => {
    if (isInitialMount.current) {
      isInitialMount.current = false
      return
    }

    // Mark that we're syncing from URL to prevent URL write-back
    isSyncingFromURL.current = true

    // Re-read URL params and update state
    const newKind = getInitialKindFromURL(basePath, defaultKind, locationPathname, locationSearch)
    const newFilters = getInitialFiltersFromURL()

    // Update kind if it changed
    if (newKind.name !== selectedKind.name || newKind.group !== selectedKind.group) {
      setSelectedKind(newKind)
    }

    // Update owner filter if it changed
    if (newFilters.ownerKind !== ownerKind || newFilters.ownerName !== ownerName) {
      setOwnerKind(newFilters.ownerKind)
      setOwnerName(newFilters.ownerName)
    }

    // Update search if it changed — unless this navigation is the echo of our
    // own debounced URL write (its search equals the value we just wrote), or
    // the user is mid-typing in the box. During typing the URL intentionally
    // lags the input, so an un-guarded sync here would revert in-flight
    // keystrokes whenever a write echo lands mid-burst. Genuinely external
    // navigations (deep links, back/forward) carry a different search value
    // and still sync.
    const isOwnWriteEcho =
      newFilters.search === debouncedSearchTerm ||
      (typeof document !== 'undefined' && document.activeElement === searchInputRef.current)
    if (!isOwnWriteEcho && newFilters.search !== searchTerm) {
      setSearchTerm(newFilters.search)
    }

    // Update regex mode if it changed
    if (newFilters.regex !== regexMode) {
      setRegexMode(newFilters.regex)
    }

    // Update column filters if changed
    const newFiltersStr = serializeColumnFilters(newFilters.columnFilters)
    const currentFiltersStr = serializeColumnFilters(columnFilters)
    if (newFiltersStr !== currentFiltersStr) {
      setColumnFilters(newFilters.columnFilters)
    }

    // Update column filter operators (include/exclude) if changed
    const excludeKeys = (m: Record<string, boolean>) => Object.keys(m).filter(k => m[k]).sort().join(',')
    if (excludeKeys(newFilters.columnFilterExcludes) !== excludeKeys(columnFilterExcludes)) {
      setColumnFilterExcludes(newFilters.columnFilterExcludes)
    }

    // Reset the flag after a tick to allow normal URL updates
    requestAnimationFrame(() => {
      isSyncingFromURL.current = false
    })
  }, [locationPathname, locationSearch, defaultKind, basePath]) // Re-run when injected URL path or search params change

  const navigate = useMemo(() => {
    if (!onNavigate) return (_pathOrObj: any, _opts?: any) => {}
    // Adapter: react-router-dom's navigate can be called as navigate(path) or navigate({ pathname, search }, { replace })
    return (pathOrObj: any, opts?: any) => {
      if (typeof pathOrObj === 'string') {
        onNavigate(pathOrObj, opts)
      } else if (pathOrObj && typeof pathOrObj === 'object') {
        const path = pathOrObj.pathname + (pathOrObj.search ? `?${pathOrObj.search}` : '')
        onNavigate(path, opts)
      }
    }
  }, [onNavigate])

  // Update URL with all state
  const updateURL = useCallback((
    kindInfo: SelectedKindInfo,
    search: string,
    regex: boolean,
    colFilters: Record<string, string[]>,
    colExcludes: Record<string, boolean>,
    problems: string[],
    showInactive: boolean,
    resourceNs?: string,
    resourceName?: string,
    pushHistory?: boolean
  ) => {
    // Preserve existing params (like namespace from App)
    const params = new URLSearchParams(window.location.search)

    // Kind is now in the path (/resources/{kind}), not a query param
    params.delete('kind')
    if (kindInfo.group) {
      params.set('apiGroup', kindInfo.group)
    } else {
      params.delete('apiGroup')
    }
    if (search) {
      params.set('search', search)
    } else {
      params.delete('search')
    }
    if (regex) {
      params.set('regex', 'true')
    } else {
      params.delete('regex')
    }
    // Write column filters as `filters` param; the exclude operator is folded
    // into each column entry, so it can never drift from the values it negates.
    // Guard against a stale exclude flag on a column with no active values.
    const activeExcludes = Object.fromEntries(
      Object.entries(colExcludes).filter(([k, on]) => on && (colFilters[k]?.length ?? 0) > 0)
    )
    const filtersStr = serializeColumnFilters(colFilters, activeExcludes)
    if (filtersStr) {
      params.set('filters', filtersStr)
    } else {
      params.delete('filters')
    }
    if (problems.length > 0) {
      params.set('problems', problems.join(','))
    } else {
      params.delete('problems')
    }
    if (showInactive) {
      params.set('showInactive', 'true')
    } else {
      params.delete('showInactive')
    }
    if (resourceName) {
      // Namespaced: ns/name, cluster-scoped: just name
      params.set('resource', resourceNs ? `${resourceNs}/${resourceName}` : resourceName)
    } else {
      params.delete('resource')
      // `full` (over-list fullscreen) and `tab` are resource-scoped — when no
      // resource is selected they're stale; drop them so they can't leak onto a
      // later selection (e.g. after a kind switch or closing the drawer).
      params.delete('full')
      params.delete('tab')
    }

    const newPath = `${basePath}/${kindInfo.name}`
    const queryStr = params.toString()

    // No-op guard: if the target URL already matches the address bar, skip the
    // navigate. Without this, a state catch-up after browser POP (App-level
    // POP→state sync re-running this effect) would push a duplicate entry on
    // top of the popped state — making the next Back appear to do nothing or
    // (with multi-namespace name collisions) jump to a sibling resource via
    // auto-resolution. Reading window.location avoids needing host-injected
    // navigationType.
    if (typeof window !== 'undefined') {
      const currentPathname = window.location.pathname
      const currentSearch = window.location.search.replace(/^\?/, '')
      // Compare using basename-relative target path against window.pathname,
      // which may include a host basename (e.g. /c/{cluster}). Treat a path
      // suffix match as equal so embedded hosts don't false-trigger a write.
      const pathMatches = currentPathname === newPath || currentPathname.endsWith(newPath)
      if (pathMatches && currentSearch === queryStr) {
        return
      }
    }

    // Route both push and replace through `navigate` (which honors the
    // onNavigate prop). The previous direct `window.history.replaceState`
    // bypass meant a host that wants to suppress URL writes (passing
    // `onNavigate={() => {}}`) couldn't suppress filter-change
    // updates — they'd still rewrite the address bar through history.
    navigate({ pathname: newPath, search: queryStr }, { replace: !pushHistory })
  }, [navigate, basePath])

  const clearAllFilters = useCallback(() => {
    setSearchTerm('')
    setRegexMode(false)
    setColumnFilters({})
    setColumnFilterExcludes({})
    setProblemFilters([])
    setLabelSelector('')
    setOwnerKind('')
    setOwnerName('')
    setShowInactiveReplicaSets(false)
    // Filter-only URL params. Path + kind + namespace + other cross-view
    // params are out of scope here; the host's onClearNamespaces (and its
    // own state→URL sync) owns namespace cleanup.
    const params = new URLSearchParams(window.location.search)
    for (const key of ['search', 'regex', 'filters', 'problems', 'labels', 'ownerKind', 'ownerName', 'showInactive']) {
      params.delete(key)
    }
    navigate({ pathname: window.location.pathname, search: params.toString() }, { replace: true })
    onClearNamespaces?.()
  }, [navigate, onClearNamespaces])

  // Update URL when any filter changes
  useEffect(() => {
    // Skip URL update if we're syncing FROM the URL (e.g., browser back button)
    if (isSyncingFromURL.current) {
      prevSelectedResourceRef.current = selectedResource ?? null
      return
    }
    // Skip on initial mount so we don't strip ?resource= before the mount effect reads it
    if (!hasProcessedInitialResource.current) {
      prevSelectedResourceRef.current = selectedResource ?? null
      return
    }
    // Skip URL update if selectedResource's kind doesn't match selectedKind (still syncing)
    if (selectedResource) {
      const resourceKindLower = selectedResource.kind.toLowerCase()
      if (selectedKind.name.toLowerCase() !== resourceKindLower) {

        return // Wait for kind sync effect to run first
      }
    }
    // Push history for navigations (so browser back works); replace for filter / drawer-toggle changes.
    // A navigation is one of: explicit sidebar/keyboard kind switch (shouldPushHistory),
    // kind change driven by external setSelectedResource (pathname differs from target — e.g. clicking a
    // Parent Gateway from a TCPRoute drawer), or a drawer-to-drawer switch within the same kind
    // (selectedResource A -> B, both non-null and different). Initial open (null -> X) and close (X -> null)
    // stay as replace because they don't represent a destination the user wants to "go back" to.
    const targetPath = `${basePath}/${selectedKind.name}`
    // Compare basename-relative paths. Hosts that mount the app under a non-empty basename
    // (e.g. Radar Hub at /c/{cluster}) inject `locationPathname` from useLocation(), which strips
    // the basename — `window.location.pathname` still includes it, so reading window directly
    // would never match `targetPath` (basename-relative) and force every URL write to push.
    const currentPath =
      locationPathname !== undefined
        ? locationPathname
        : typeof window !== 'undefined'
          ? window.location.pathname
          : ''
    const pathChanged = currentPath !== targetPath
    const prev = prevSelectedResourceRef.current
    const current = selectedResource ?? null
    const drawerSwitched =
      prev !== null && current !== null &&
      (prev.namespace !== current.namespace ||
        prev.name !== current.name ||
        prev.kind !== current.kind ||
        (prev.group ?? '') !== (current.group ?? ''))
    const pushHistory = shouldPushHistory.current || pathChanged || drawerSwitched
    shouldPushHistory.current = false
    prevSelectedResourceRef.current = current

    updateURL(selectedKind, debouncedSearchTerm, regexMode, columnFilters, columnFilterExcludes, problemFilters, showInactiveReplicaSets, selectedResource?.namespace, selectedResource?.name, pushHistory)
  }, [selectedKind, debouncedSearchTerm, regexMode, columnFilters, columnFilterExcludes, problemFilters, showInactiveReplicaSets, selectedResource, updateURL, basePath, locationPathname])

  // Handle resource click from URL on mount
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const resourceParam = params.get('resource')
    if (resourceParam && onResourceClick) {
      const slashIndex = resourceParam.indexOf('/')
      if (slashIndex > 0) {
        // Namespaced: ?resource=namespace/name
        const ns = resourceParam.slice(0, slashIndex)
        const name = resourceParam.slice(slashIndex + 1)
        onResourceClick({ kind: selectedKind.name, namespace: ns, name, group: selectedKind.group })
      } else {
        // Cluster-scoped: ?resource=name (no namespace)
        onResourceClick({ kind: selectedKind.name, namespace: '', name: resourceParam, group: selectedKind.group })
      }
    }
    // Signal that initial resource param has been processed — URL update effect can now run
    hasProcessedInitialResource.current = true

    // If the URL has no kind segment (e.g., /resources), update to include the default kind.
    // Route through `navigate` so an onNavigate-suppressing host can opt out.
    const base = basePath.replace(/\/$/, '')
    const path = window.location.pathname
    if (path === base || path === base + '/') {
      const search = window.location.search
      navigate({ pathname: `${base}/${selectedKind.name}`, search: search.replace(/^\?/, '') }, { replace: true })
    }
  }, []) // Only on mount

  // API resources for dynamic sidebar — provided via props
  const apiResources = apiResourcesProp

  // Sync selectedKind when selectedResource changes from external navigation (e.g., from Helm view)
  // Also re-runs when apiResources loads, to correct CRD kinds that were initially resolved via fallback
  useEffect(() => {
    if (!selectedResource) return

    const resourceKindLower = selectedResource.kind.toLowerCase()

    // Prefer matching from resourcesToCount (deduped list used for queries) to ensure group consistency.
    // Raw apiResources can have duplicates (e.g., Event in both v1 and events.k8s.io) where find()
    // returns a different group than categorizeResources() deduped to, causing query index mismatch.
    // When selectedResource has a group, match on group too to handle collisions (e.g., KNative Service vs core Service).
    const resourceGroup = selectedResource.group ?? ''
    const countMatch = resourcesToCount.find(r =>
      (r.name.toLowerCase() === resourceKindLower || r.kind.toLowerCase() === resourceKindLower) &&
      r.group === resourceGroup
    ) ?? resourcesToCount.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )

    if (countMatch) {
      if (selectedKind.name === countMatch.name && selectedKind.kind === countMatch.kind && selectedKind.group === countMatch.group) return
      setOwnerKind('')
      setOwnerName('')
      setSelectedKind({ name: countMatch.name, kind: countMatch.kind, group: countMatch.group })
      return
    }

    // Fall back to raw API resources for kinds not yet in categories
    const apiMatch = apiResources?.find(r =>
      (r.name.toLowerCase() === resourceKindLower || r.kind.toLowerCase() === resourceKindLower) &&
      r.group === resourceGroup
    ) ?? apiResources?.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )
    const coreMatch = CORE_RESOURCES.find(r =>
      r.name.toLowerCase() === resourceKindLower ||
      r.kind.toLowerCase() === resourceKindLower
    )
    const match = apiMatch || coreMatch

    if (match) {
      if (selectedKind.name === match.name && selectedKind.kind === match.kind && selectedKind.group === match.group) return
      setOwnerKind('')
      setOwnerName('')
      setSelectedKind({ name: match.name, kind: match.kind, group: match.group })
    } else {
      // Last resort fallback: derive singular, preserve group from navigation
      const singular = resourceKindLower.endsWith('s')
        ? resourceKindLower.slice(0, -1).charAt(0).toUpperCase() + resourceKindLower.slice(1, -1)
        : resourceKindLower.charAt(0).toUpperCase() + resourceKindLower.slice(1)
      const group = selectedResource.group ?? ''
      if (selectedKind.name === resourceKindLower && selectedKind.group === group) return
      setOwnerKind('')
      setOwnerName('')
      setSelectedKind({ name: resourceKindLower, kind: singular, group })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedResource, apiResources])

  // Categorize resources for sidebar
  const categories = useMemo(() => {
    if (!apiResources) return null
    return categorizeResources(apiResources)
  }, [apiResources])

  // Get resources to count - use kind as unique key since name can conflict (e.g., pods vs PodMetrics)
  const resourcesToCount = useMemo(() => {
    if (categories) {
      return categories.flatMap(c => c.resources).map(r => ({
        kind: r.kind,
        name: r.name,
        group: r.group,
      }))
    }
    return CORE_RESOURCES.map(r => ({
      kind: r.kind,
      name: r.name,
      group: r.group,
    }))
  }, [categories])

  // Correct selectedKind when apiResources loads (handles URL deep-links to CRD resources)
  // getInitialKindFromURL can't look up CRDs, so name may be wrong (e.g., 'HTTPRoute' instead of 'httproutes')
  useEffect(() => {
    if (!apiResources) return
    const canonicalKind = canonicalizeSelectedKind(selectedKind, resourcesToCount, apiResources)
    if (canonicalKind) {
      setSelectedKind(canonicalKind)
    }
  }, [apiResources, resourcesToCount, selectedKind.name, selectedKind.kind, selectedKind.group])

  // Resource data — prefer new lightweight props over legacy resourceQueries array
  const resourceQueries = resourceQueriesProp ?? []
  const useNewCountsMode = resourceCountsProp !== undefined || selectedKindQueryProp !== undefined

  // Find the selected kind's query
  const selectedQueryIndex = useMemo(() => {
    return resourcesToCount.findIndex(r =>
      r.name === selectedKind.name && r.group === selectedKind.group
    )
  }, [resourcesToCount, selectedKind.name, selectedKind.group])

  const selectedQuery = selectedKindQueryProp ?? resourceQueries[selectedQueryIndex]
  const selectedQueryMatchesKind = resourceQueryMatchesSelectedKind(selectedQuery, selectedKind)
  const resources = selectedQueryMatchesKind ? selectedQuery?.data : undefined

  // Auto-surface the GPU column the first time GPU-bearing rows load for a kind
  // the user has never customized — a static default can't know whether the
  // cluster has GPUs, and hiding the signal on GPU clusters buries the point.
  // The resulting save persists it; explicit user hides are never overridden
  // (hadSavedColumnSettings guard).
  useEffect(() => {
    if (hadSavedColumnSettings.current) return
    const kindLower = selectedKind.name.toLowerCase()
    if (kindLower !== 'pods' && kindLower !== 'nodes') return
    if (gpuAutoShownKinds.current.has(kindLower)) return
    if (visibleColumns.size === 0 || visibleColumns.has('gpu')) return
    const rows = (resources as any[] | undefined) ?? []
    if (rows.length === 0) return
    const hasGpu = kindLower === 'pods'
      ? rows.some(r => getPodGpuCount(r) > 0)
      : rows.some(r => getNodeGpuCount(r) > 0)
    if (hasGpu) {
      gpuAutoShownKinds.current.add(kindLower)
      setVisibleColumns(prev => new Set([...prev, 'gpu']))
    }
  }, [resources, selectedKind.name, visibleColumns])

  // Label/annotation keys present on the loaded rows — powers the
  // add-custom-column autocomplete so users don't type keys blind. Sampled
  // to bound cost on large lists; keys converge fast across rows of one kind.
  const customColumnKeySuggestions = useMemo(() => {
    const labels = new Set<string>()
    const annotations = new Set<string>()
    const rows = (resources as any[] | undefined) ?? []
    for (let i = 0; i < rows.length && i < 500; i++) {
      const meta = rows[i]?.metadata
      if (meta?.labels) for (const k of Object.keys(meta.labels)) labels.add(k)
      if (meta?.annotations) for (const k of Object.keys(meta.annotations)) annotations.add(k)
    }
    return {
      label: Array.from(labels).sort(),
      annotation: Array.from(annotations).sort(),
    }
  }, [resources])
  const isLoading = selectedQueryMatchesKind ? (selectedQuery?.isLoading ?? true) : true
  const selectedQueryError = selectedQueryMatchesKind ? selectedQuery?.error : undefined
  const selectedKindCountKey = selectedKind.group
    ? `${selectedKind.group}/${selectedKind.kind}`
    : selectedKind.kind
  const selectedQueryHasLoadedCount = Array.isArray(resources) && !isLoading && !selectedQueryError && !largeListGuard
  const selectedLoadedResourceCount = Array.isArray(resources) ? resources.length : undefined
  const [loadedCountCache, setLoadedCountCache] = useState<LoadedResourceCountCache>({})
  const namespaceScopeKey = useMemo(
    () => namespaces.length === 0 ? '' : [...namespaces].sort().join('\0'),
    [namespaces],
  )
  const loadedCountScopeRef = useRef(namespaceScopeKey)

  useEffect(() => {
    if (loadedCountScopeRef.current === namespaceScopeKey) return
    loadedCountScopeRef.current = namespaceScopeKey
    setLoadedCountCache({})
  }, [namespaceScopeKey])

  useEffect(() => {
    if (!selectedQueryHasLoadedCount || selectedLoadedResourceCount == null) return
    const now = Date.now()
    const loadedAt = selectedQuery?.dataUpdatedAt && selectedQuery.dataUpdatedAt > 0
      ? selectedQuery.dataUpdatedAt
      : now
    const expiresAt = loadedAt + LOADED_RESOURCE_COUNT_TTL_MS

    setLoadedCountCache(prev => {
      const next: LoadedResourceCountCache = {}
      let changed = false
      for (const [key, cached] of Object.entries(prev)) {
        if (cached.expiresAt > now || key === selectedKindCountKey) {
          next[key] = cached
        } else {
          changed = true
        }
      }

      const existing = next[selectedKindCountKey]
      if (existing?.count === selectedLoadedResourceCount && existing.expiresAt === expiresAt) {
        return changed ? next : prev
      }

      return {
        ...next,
        [selectedKindCountKey]: { count: selectedLoadedResourceCount, expiresAt },
      }
    })
  }, [selectedQueryHasLoadedCount, selectedLoadedResourceCount, selectedKindCountKey, selectedQuery?.dataUpdatedAt])

  // Derive counts — prefer lightweight resourceCounts prop over full query data
  const counts = useMemo(() => {
    if (useNewCountsMode) {
      const results = deriveSidebarResourceCounts(resourcesToCount, resourceCountsProp, resourceUnavailableProp, loadedCountCache)
      if (selectedQueryHasLoadedCount && selectedLoadedResourceCount != null) {
        results[selectedKindCountKey] = selectedLoadedResourceCount
      }
      return results
    }
    // Legacy: derive counts from full query data
    const results: Record<string, number | null> = {}
    resourcesToCount.forEach((resource, index) => {
      const data = resourceQueries[index]?.data
      const key = resource.group ? `${resource.group}/${resource.kind}` : resource.kind
      results[key] = Array.isArray(data) ? data.length : 0
    })
    return results
  }, [useNewCountsMode, resourcesToCount, resourceCountsProp, resourceUnavailableProp, loadedCountCache, selectedQueryHasLoadedCount, selectedLoadedResourceCount, selectedKindCountKey, resourceQueries])

  const sidebarResourceUnavailable = useMemo(() => {
    if (!resourceUnavailableProp?.length) {
      return resourceUnavailableProp
    }
    return resourceUnavailableProp.filter(key => counts[key] == null)
  }, [resourceUnavailableProp, counts])

  // Track which resource kinds returned 403 Forbidden
  const forbiddenKinds = useMemo(() => {
    if (useNewCountsMode) {
      return new Set(resourceForbiddenProp ?? [])
    }
    // Legacy: derive from full query errors
    const result = new Set<string>()
    resourcesToCount.forEach((resource, index) => {
      if (isForbiddenError(resourceQueries[index]?.error)) {
        result.add(resource.group ? `${resource.group}/${resource.kind}` : resource.kind)
      }
    })
    return result
  }, [useNewCountsMode, resourceForbiddenProp, resourcesToCount, resourceQueries])

  // Render the restricted state when EITHER the list query 403s (namespaced
  // denials) OR the selected kind is in the counts `forbidden` set. Denied
  // cluster-scoped kinds return 200 with `[]` from the list endpoint, so the
  // 403 signal alone misses them — they'd fall through to "No <kind> found".
  // Actual rows win over a stale counts `forbidden` entry: a kind can be marked
  // forbidden/unavailable in counts (e.g. an informer not yet synced at counts
  // time) while the list query has since returned data — show the table, not
  // RestrictedState. A 403 on the list itself never carries rows, so it still
  // forces the restricted state.
  const selectedHasRows = Array.isArray(resources) && resources.length > 0
  const isSelectedForbidden =
    isForbiddenError(selectedQueryError) ||
    (!selectedHasRows && forbiddenKinds.has(selectedKindCountKey))

  // Reset sort and filters when kind changes (but not when syncing from URL navigation)
  // Track previous kind to skip on mount (where the effect fires but kind hasn't actually changed)
  // Keyed on group as well as plural: two CRDs can share a plural across API
  // groups, and their printer columns are unrelated. Resetting on the plural
  // alone would carry a `printer:*` sort or filter onto a table with no such
  // column, where every row fails the filter and the list looks empty.
  const selectedKindIdentity = selectedKindIdentityOf(selectedKind)
  const prevKindRef = useRef(selectedKindIdentity)
  useEffect(() => {
    if (prevKindRef.current === selectedKindIdentity) {
      return
    }
    prevKindRef.current = selectedKindIdentity
    setSortColumn(null)
    setSortDirection(null)
    setOpenColumnFilter(null)
    if (!isSyncingFromURL.current) {
      setColumnFilters({})
      setColumnFilterExcludes({})
    }
    setProblemFilters([])
  }, [selectedKindIdentity])

  // Toggle sort for a column
  const handleSort = useCallback((column: string) => {
    if (sortColumn === column) {
      // Cycle: asc -> desc -> null
      if (sortDirection === 'asc') {
        setSortDirection('desc')
      } else if (sortDirection === 'desc') {
        setSortColumn(null)
        setSortDirection(null)
      } else {
        setSortDirection('asc')
      }
    } else {
      setSortColumn(column)
      setSortDirection('asc')
    }
  }, [sortColumn, sortDirection])

  // Get sortable value from a resource for a given column
  const getSortValue = useCallback((resource: any, column: string, kind?: string): string | number => {
    const meta = resource.metadata || {}
    const status = resource.status || {}
    const kindLower = kind?.toLowerCase() || ''

    switch (column) {
      case 'name':
        return meta.name || ''
      case 'namespace':
        return meta.namespace || ''
      case 'age':
        return meta.creationTimestamp ? new Date(meta.creationTimestamp).getTime() : 0
      case 'status':
        // Phase first for a curated kind: it keeps the most-used lists — Pods,
        // Deployments, Nodes — ordering as they always have.
        //
        // Everything else routes through getCellFilterValue, the same reader
        // the cell and the filter dropdown use, so all three agree on one
        // string. Deriving it here instead ordered rows on a status the row
        // never displayed: a Gateway shows `Programmed` while the generic
        // ladder, which knows neither Programmed nor Accepted, called it
        // `Accepted` — so a healthy Gateway and a pending one sorted equal.
        // getCellFilterValue ends in that same generic derivation, so an
        // uncurated kind still sorts on the text its badge shows.
        if (hasCuratedColumns(selectedKind.name, selectedKind.group) && status.phase) {
          return status.phase
        }
        return getCellFilterValue(resource, 'status', kindLower)
      case 'servers':
        // Numeric so 10 does not sort before 9.
        if (kindLower === 'istiogateways') return getIstioGatewayServerCount(resource)
        return ''
      case 'selector':
        if (kindLower === 'istiogateways') return getIstioGatewaySelectorString(resource)
        return ''
      case 'containers':
        // Pod containers column — sort by readiness ratio
        if (status.containerStatuses) {
          const ready = status.containerStatuses.filter((c: any) => c.ready).length
          const total = status.containerStatuses.length
          return total > 0 ? ready / total : 0
        }
        return 0
      case 'ready':
        if (kindLower === 'jobsets') return getJobSetReadyJobs(resource)
        // For DaemonSets, use numberReady/desiredNumberScheduled
        if (kindLower === 'daemonsets') {
          const desired = status.desiredNumberScheduled ?? 0
          const ready = status.numberReady ?? 0
          return desired > 0 ? ready / desired : 0
        }
        // For other workloads, use readyReplicas/replicas ratio
        const desiredReplicas = resource.spec?.replicas ?? 0
        const readyReplicas = status.readyReplicas ?? 0
        return desiredReplicas > 0 ? readyReplicas / desiredReplicas : 0
      case 'desired':
        // DaemonSet: desiredNumberScheduled
        return status.desiredNumberScheduled ?? 0
      case 'available':
        // DaemonSet: numberAvailable, others: availableReplicas
        return status.numberAvailable ?? status.availableReplicas ?? 0
      case 'upToDate':
        // DaemonSet: updatedNumberScheduled, others: updatedReplicas
        return status.updatedNumberScheduled ?? status.updatedReplicas ?? 0
      case 'restarts':
        if (kindLower === 'jobsets') return getJobSetRestarts(resource)
        return getPodRestarts(resource)
      case 'lastSeen': {
        const lastTs = resource.lastTimestamp || meta.creationTimestamp
        return lastTs ? new Date(lastTs).getTime() : 0
      }
      case 'count':
        return resource.count || 0
      case 'reason':
        return resource.reason || ''
      case 'object':
        return resource.involvedObject ? `${resource.involvedObject.kind}/${resource.involvedObject.name}` : ''
      case 'type':
        return resource.spec?.type || resource.type || ''
      case 'version':
        return status.nodeInfo?.kubeletVersion || ''
      case 'cpu': {
        if (kindLower === 'pods') {
          const key = `${meta.namespace}/${meta.name}`
          return metricsLookup.pods.get(key)?.cpu ?? 0
        }
        if (kindLower === 'nodes') {
          return metricsLookup.nodes.get(meta.name)?.cpu ?? 0
        }
        return 0
      }
      case 'memory': {
        if (kindLower === 'pods') {
          const key = `${meta.namespace}/${meta.name}`
          return metricsLookup.pods.get(key)?.memory ?? 0
        }
        if (kindLower === 'nodes') {
          return metricsLookup.nodes.get(meta.name)?.memory ?? 0
        }
        return 0
      }
      case 'pods': {
        if (kindLower === 'nodes') {
          return metricsLookup.nodes.get(meta.name)?.podCount ?? 0
        }
        return 0
      }
      case 'gpu': {
        if (kindLower === 'pods') return getPodGpuCount(resource)
        if (kindLower === 'nodes') return getNodeGpuCount(resource)
        return 0
      }
      default:
        return ''
    }
  }, [metricsLookup, selectedKind.name, selectedKind.group])

  // Helper to check if a pod matches problem filters
  const podMatchesProblemFilter = useCallback((pod: any, filters: string[]): boolean => {
    if (filters.length === 0) return true
    const problems = getPodProblems(pod)
    const restarts = getPodRestarts(pod)
    return filters.some(filter => podMatchesProblemCategory(problems, restarts, filter))
  }, [])


  // On an invalid pattern, fall back to a null matcher (search un-applied, all
  // rows shown) rather than zero results, so the table doesn't flash empty
  // while the user is mid-typing a pattern.
  const searchRegex = useMemo<{ re: RegExp | null; error: string | null }>(() => {
    if (!regexMode || !deferredSearchTerm) return { re: null, error: null }
    try {
      return { re: new RegExp(deferredSearchTerm, 'i'), error: null }
    } catch (e) {
      return { re: null, error: e instanceof Error ? e.message : 'Invalid regex' }
    }
  }, [regexMode, deferredSearchTerm])

  // Filter resources by search term, status, problems, and sort
  const filteredResources = useMemo(() => {
    if (!resources) return []

    let result = resources

    // Apply search filter
    if (deferredSearchTerm) {
      if (regexMode) {
        const re = searchRegex.re
        if (re) {
          result = result.filter((r: any) =>
            re.test(r.metadata?.name ?? '') ||
            re.test(r.metadata?.namespace ?? '')
          )
        }
      } else {
        const term = deferredSearchTerm.toLowerCase()
        result = result.filter((r: any) =>
          r.metadata?.name?.toLowerCase().includes(term) ||
          r.metadata?.namespace?.toLowerCase().includes(term)
        )
      }
    }

    // Apply column filters (generic, multi-select per column — OR within column, AND across columns)
    // Extra columns override the built-in getCellFilterValue
    // when the parent supplied a custom getFilterValue.
    const activeColFilters = Object.entries(columnFilters).filter(([, vals]) => vals.length > 0)
    if (activeColFilters.length > 0) {
      const cellFilterKind = getCellFilterKind(selectedKind.name, selectedKind.group)
      result = result.filter((r: any) =>
        activeColFilters.every(([col, vals]) => {
          const extra = extraColumnsByKey.get(col)
          const cellVal = extra?.getFilterValue ? extra.getFilterValue(r) : getCellFilterValue(r, col, cellFilterKind)
          const match = vals.includes(cellVal)
          return columnFilterExcludes[col] ? !match : match
        })
      )
    }

    // Apply problem filters
    if (problemFilters.length > 0) {
      const kindLower = normalizeKindToPlural(selectedKind.name, selectedKind.group)
      if (kindLower === 'pods') {
        result = result.filter((r: any) => podMatchesProblemFilter(r, problemFilters))
      } else if (WORKLOAD_KINDS.has(kindLower)) {
        result = result.filter((r: any) => {
          const problems = getWorkloadProblems(r, kindLower)
          return problemFilters.some(f => workloadMatchesProblemCategory(problems, f))
        })
      }
    }

    // Apply inactive ReplicaSet filter (default: hide inactive)
    if (selectedKind.name.toLowerCase() === 'replicasets' && !showInactiveReplicaSets) {
      result = result.filter((r: any) => isReplicaSetActive(r))
    }

    // Apply label selector filter (e.g., "app=caretta,version=v1") — OR logic: matches ANY selected label
    if (labelSelector) {
      const labelPairs = labelSelector.split(',').map(pair => {
        const [key, value] = pair.split('=')
        return { key: key?.trim(), value: value?.trim() }
      }).filter(p => p.key && p.value)

      result = result.filter((r: any) => {
        const labels = r.metadata?.labels || {}
        return labelPairs.some(({ key, value }) => labels[key] === value)
      })
    }

    // Apply owner filter (e.g., ownerKind=DaemonSet, ownerName=app-caretta)
    if (ownerKind && ownerName) {
      result = result.filter((r: any) => {
        const ownerRefs = r.metadata?.ownerReferences || []

        // For Deployment ownership: Pods are owned by ReplicaSets, not Deployments directly.
        // ReplicaSets created by Deployments are named "<deployment-name>-<hash>".
        if (ownerKind === 'Deployment') {
          return ownerRefs.some((ref: any) =>
            ref.kind === 'ReplicaSet' && ref.name.startsWith(ownerName + '-')
          )
        }

        // Direct owner match for other kinds (DaemonSet, StatefulSet, Job, etc.)
        return ownerRefs.some((ref: any) =>
          ref.kind === ownerKind && ref.name === ownerName
        )
      })
    }

    // Apply custom sorting if set. Extra columns override
    // the built-in getSortValue for their key.
    if (sortColumn && sortDirection) {
      const extra = extraColumnsByKey.get(sortColumn)
      // Normalized, matching the filter call sites: a group-qualified kind
      // (istiogateways, cnpgclusters) otherwise arrives here as its bare plural
      // and misses its own sort readers. Hoisted out of the comparator.
      // The ownership-aware wrapper also keeps a foreign CRD that reuses a
      // curated plural on the generic reader used by its cells and filters.
      const sortKind = getCellFilterKind(selectedKind.name, selectedKind.group)
      const keyOf = extra?.getSortValue ?? ((r: any) => getSortValue(r, sortColumn, sortKind))
      // Derived once per row, not once per comparison: a sort visits O(n log n)
      // pairs, and the status key now runs a per-kind status derivation.
      const decorated = result.map((r: any) => ({ r, k: keyOf(r) }))
      decorated.sort((a, b) => {
        let comparison = 0
        if (typeof a.k === 'number' && typeof b.k === 'number') {
          comparison = a.k - b.k
        } else {
          comparison = String(a.k).localeCompare(String(b.k))
        }
        return sortDirection === 'desc' ? -comparison : comparison
      })
      result = decorated.map(d => d.r)
    } else {
      // Default sort by kind
      const kindLower = normalizeKindToPlural(selectedKind.name, selectedKind.group)

      if (kindLower === 'pods') {
        // Completed pods at bottom, then sort by name for stability across refreshes
        result = [...result].sort((a: any, b: any) => {
          const aCompleted = a.status?.phase === 'Succeeded'
          const bCompleted = b.status?.phase === 'Succeeded'
          if (aCompleted && !bCompleted) return 1
          if (!aCompleted && bCompleted) return -1
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else if (kindLower === 'daemonsets') {
        // DaemonSets with 0 desired (empty/inactive) at bottom, then sort by ready desc
        result = [...result].sort((a: any, b: any) => {
          const aDesired = a.status?.desiredNumberScheduled ?? 0
          const bDesired = b.status?.desiredNumberScheduled ?? 0
          const aReady = a.status?.numberReady ?? 0
          const bReady = b.status?.numberReady ?? 0

          // Empty DaemonSets (0 desired) go to bottom
          if (aDesired === 0 && bDesired > 0) return 1
          if (aDesired > 0 && bDesired === 0) return -1

          // Then sort by health: unhealthy (ready < desired) first
          const aHealthy = aReady >= aDesired
          const bHealthy = bReady >= bDesired
          if (!aHealthy && bHealthy) return -1
          if (aHealthy && !bHealthy) return 1

          // Finally sort by name
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else if (kindLower === 'events') {
        // Events: most recently seen first, name tiebreaker for same-timestamp stability
        result = [...result].sort((a: any, b: any) => {
          const aTime = new Date(a.lastTimestamp || a.metadata?.creationTimestamp || 0).getTime()
          const bTime = new Date(b.lastTimestamp || b.metadata?.creationTimestamp || 0).getTime()
          if (bTime !== aTime) return bTime - aTime
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else if (['deployments', 'statefulsets', 'replicasets'].includes(kindLower)) {
        // Workloads: unhealthy first, scaled-to-zero at bottom
        result = [...result].sort((a: any, b: any) => {
          const aDesired = a.spec?.replicas ?? 0
          const bDesired = b.spec?.replicas ?? 0
          const aReady = a.status?.readyReplicas ?? 0
          const bReady = b.status?.readyReplicas ?? 0

          // Scaled-to-zero at bottom
          if (aDesired === 0 && bDesired > 0) return 1
          if (aDesired > 0 && bDesired === 0) return -1

          // Unhealthy (ready < desired) first
          const aHealthy = aReady >= aDesired
          const bHealthy = bReady >= bDesired
          if (!aHealthy && bHealthy) return -1
          if (aHealthy && !bHealthy) return 1

          // Finally sort by name
          return (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        })
      } else {
        // All other kinds: sort by name for stability across refreshes
        result = [...result].sort((a: any, b: any) =>
          (a.metadata?.name || '').localeCompare(b.metadata?.name || '')
        )
      }
    }

    return result
  }, [resources, deferredSearchTerm, regexMode, searchRegex, columnFilters, columnFilterExcludes, problemFilters, showInactiveReplicaSets, labelSelector, ownerKind, ownerName, selectedKind.name, sortColumn, sortDirection, getSortValue, extraColumnsByKey, podMatchesProblemFilter])

  // For nodes table: compute the majority minor version so outliers can be highlighted
  const majorityNodeMinorVersion = useMemo(() => {
    if (selectedKind.name.toLowerCase() !== 'nodes') return ''
    const counts = new Map<string, number>()
    for (const r of filteredResources) {
      const full = r.status?.nodeInfo?.kubeletVersion || ''
      const match = full.match(/^v?(\d+\.\d+)/)
      if (match) counts.set(match[1], (counts.get(match[1]) || 0) + 1)
    }
    let best = ''
    let bestCount = 0
    for (const [v, c] of counts) {
      if (c > bestCount) { best = v; bestCount = c }
    }
    return counts.size > 1 ? best : '' // empty string means no skew
  }, [filteredResources, selectedKind.name])

  // Keep refs in sync for keyboard shortcuts (shortcuts can't capture filteredResources directly)
  filteredResourceCountRef.current = filteredResources.length
  highlightedResourceRef.current = highlightedIndex >= 0 ? filteredResources[highlightedIndex] ?? null : null

  // Scroll to selected row when selection changes or data loads
  const lastScrolledResource = useRef<string | null>(null)
  useEffect(() => {
    if (!selectedResource || filteredResources.length === 0) return
    const resourceKey = `${selectedResource.kind}/${selectedResource.namespace}/${selectedResource.name}`
    if (lastScrolledResource.current === resourceKey) return

    const timer = setTimeout(() => {
      const idx = filteredResources.findIndex((r: any) =>
        r.metadata?.name === selectedResource.name &&
        r.metadata?.namespace === (selectedResource.namespace || '')
      )
      if (idx >= 0) {
        lastScrolledResource.current = resourceKey
        virtuosoRef.current?.scrollToIndex({ index: idx, align: 'center', behavior: 'smooth' })
      }
    }, 100)

    return () => clearTimeout(timer)
  }, [selectedResource, filteredResources])

  // Build flat kind list for [ / ] sidebar navigation
  useEffect(() => {
    const list: SelectedKindInfo[] = []
    const seen = new Set<string>()
    const addKind = (k: SelectedKindInfo) => {
      const key = `${k.group}/${k.name}`
      if (!seen.has(key)) { seen.add(key); list.push(k) }
    }
    for (const p of pinned) addKind({ name: p.name, kind: p.kind, group: p.group })
    if (categories) {
      for (const cat of categories) {
        for (const r of cat.resources) {
          addKind({ name: r.name, kind: r.kind, group: r.group })
        }
      }
    }
    flatKindListRef.current = list
  }, [pinned, categories])

  // Multi-select helpers
  const getResourceKey = useCallback((resource: any) => {
    return resource.metadata?.uid || `${resource.metadata?.namespace || ''}/${resource.metadata?.name || ''}`
  }, [])

  const toggleChecked = useCallback((resource: any) => {
    const key = getResourceKey(resource)
    setCheckedResources(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [getResourceKey])

  // Selection state is keyed by resource, but everything user-facing
  // (count, select-all, the delete payload) derives from checkedItems —
  // the checked ∩ currently-visible intersection. Rows checked and then
  // hidden by a filter are excluded, so the bar never promises more
  // deletions than the confirm dialog will perform.
  const checkedItems = useMemo(() => {
    if (checkedResources.size === 0) return []
    return filteredResources.filter(r => checkedResources.has(getResourceKey(r)))
  }, [filteredResources, checkedResources, getResourceKey])

  const checkedBulkItems = useMemo(() => {
    return checkedItems.map(r => ({
      kind: selectedKind.name,
      group: selectedKind.group,
      namespace: r.metadata?.namespace || '',
      name: r.metadata?.name || '',
    }))
  }, [checkedItems, selectedKind.name, selectedKind.group])

  const checkedItemDetails = useMemo(() => {
    return checkedItems.map(r => `${r.metadata?.namespace ? r.metadata.namespace + '/' : ''}${r.metadata?.name}`).join('\n')
  }, [checkedItems])

  const selectedKindName = selectedKind.name.toLowerCase()
  const canBulkRestartSelectedKind = onBulkRestart != null && BULK_RESTART_WORKLOAD_KINDS.has(selectedKindName)
  const canBulkScaleSelectedKind = onBulkScale != null && BULK_SCALE_WORKLOAD_KINDS.has(selectedKindName)
  const canBulkSelect = onBulkDelete != null || canBulkRestartSelectedKind || canBulkScaleSelectedKind
  const isBulkMutating = isBulkDeleting || isBulkRestarting || isBulkScaling

  const openBulkScaleDialog = useCallback(() => {
    const replicas = checkedItems[0]?.spec?.replicas
    setBulkScaleReplicas(typeof replicas === 'number' ? replicas : 0)
    setShowBulkScaleDialog(true)
  }, [checkedItems])

  const commonBulkScaleReplicas = useMemo(() => {
    if (checkedItems.length === 0) return null
    const first = checkedItems[0]?.spec?.replicas ?? 0
    return checkedItems.every(r => (r.spec?.replicas ?? 0) === first) ? first : null
  }, [checkedItems])

  const allVisibleChecked = filteredResources.length > 0 && checkedItems.length === filteredResources.length

  const toggleCheckAll = useCallback(() => {
    setCheckedResources(allVisibleChecked ? new Set() : new Set(filteredResources.map(getResourceKey)))
  }, [allVisibleChecked, filteredResources, getResourceKey])

  const isCheckboxMode = canBulkSelect && bulkMode

  // Stable row handlers so the memoized ResourceRowCells skips re-rendering
  // rows whose data didn't change on a refetch (React Query's structural
  // sharing keeps unchanged resources referentially identical).
  const handleRowClick = useCallback((resource: any, isSelected: boolean) => {
    if (compareMode) toggleComparePick(resource)
    else if (isCheckboxMode) toggleChecked(resource)
    else selectResource(resource, isSelected)
  }, [compareMode, isCheckboxMode, toggleComparePick, toggleChecked, selectResource])
  const handleRowMouseEnter = useCallback(() => setHighlightedIndex(-1), [])

  // Filter columns by visibility
  const columns = useMemo(() => {
    if (visibleColumns.size === 0) return allColumns.filter(c => c.defaultVisible !== false)
    return allColumns.filter(c => visibleColumns.has(c.key))
  }, [allColumns, visibleColumns])

  // Fixed-width columns can consume the table's flexible space and collapse
  // the required name column. Keep a real table minimum and let the container
  // scroll horizontally when the viewport is too narrow.
  const tableMinWidth = useMemo(() => {
    const compareColumnWidth = compareMode ? COMPARE_COLUMN_WIDTH : 0
    const baseMinWidth = columns.reduce((sum, col) => sum + (columnWidths[col.key] || getColumnMinWidth(col, extraColumnsByKey)), compareColumnWidth)
    const flexibleNameColumn = columns.find(col => col.key === 'name' && !columnWidths[col.key])

    if (!hasResizedColumns || !flexibleNameColumn) return baseMinWidth
    return baseMinWidth + getColumnMinWidth(flexibleNameColumn, extraColumnsByKey)
  }, [columns, columnWidths, compareMode, hasResizedColumns, extraColumnsByKey, headerFontEpoch])

  // Stable virtuoso components — memoized to avoid remounting the table on every render
  const virtuosoComponents = useMemo(() => ({
    Table: React.forwardRef<HTMLTableElement, React.TableHTMLAttributes<HTMLTableElement>>(function VirtuosoTable(props, ref) {
      return (
        <table
          {...props}
          ref={ref}
          className="w-full"
          style={{ ...props.style, tableLayout: 'fixed', minWidth: tableMinWidth }}
        >
          <colgroup>
            {/*
              Compare-mode adds a leading <th>/<td> per row but isn't part
              of `columns`. Under table-layout:fixed the <colgroup> must
              match the actual column count, otherwise the browser pads
              the missing entry by stealing width from a sized neighbour
              — typically blowing this narrow column out to ~200px.
            */}
            {compareMode && <col style={{ width: COMPARE_COLUMN_WIDTH }} />}
            {isCheckboxMode && <col style={{ width: '40px' }} />}
            {columns.map(col => (
              <col
                key={col.key}
                style={{
                  width: columnWidths[col.key]
                    ? `${columnWidths[col.key]}px`
                    : col.key === 'name' ? undefined : `${getColumnMinWidth(col, extraColumnsByKey)}px`,
                }}
              />
            ))}
            {hasResizedColumns && <col />}
          </colgroup>
          {props.children}
        </table>
      )
    }),
    TableRow: VirtuosoTableRow,
  }), [columns, columnWidths, hasResizedColumns, compareMode, tableMinWidth, isCheckboxMode, extraColumnsByKey, headerFontEpoch])

  // Calculate filter options with counts based on current resources (before filtering)
  const filterOptions = useMemo(() => {
    if (!resources || resources.length === 0) return null

    const kindLower = normalizeKindToPlural(selectedKind.name, selectedKind.group)
    const cellFilterKind = getCellFilterKind(selectedKind.name, selectedKind.group)
    // Iterate over allColumns (built-ins + injected extras) so an
    // ExtraColumn with getFilterValue gets a column-filter dropdown like
    // any built-in does. The previous formulation iterated KNOWN_COLUMNS
    // directly, which silently skipped extras whose keys weren't already
    // in the built-in set.
    const columns = allColumns

    // Auto-detect filterable columns
    const filterableColumns: Array<{
      key: string
      label: string
      values: Array<{ value: string; count: number }>
    }> = []

    for (const col of columns) {
      if (SKIP_FILTER_COLUMNS.has(col.key)) continue

      // Extra columns supply their own filter value extractor.
      // Skip the dropdown for extras that didn't supply one (no way to
      // build the unique-values list without it).
      const extra = extraColumnsByKey.get(col.key)
      if (extra && !extra.getFilterValue) continue

      // Count distinct values for this column
      const valueCounts: Record<string, number> = {}
      for (const r of resources) {
        const val = extra?.getFilterValue ? extra.getFilterValue(r) : getCellFilterValue(r, col.key, cellFilterKind)
        if (val) {
          valueCounts[val] = (valueCounts[val] || 0) + 1
        }
      }

      const distinctCount = Object.keys(valueCounts).length
      if (isColumnFilterableByDistinctCount(col.key, distinctCount)) {
        filterableColumns.push({
          key: col.key,
          label: col.label,
          values: Object.entries(valueCounts)
            .map(([value, count]) => ({ value, count }))
            .sort((a, b) => b.count - a.count),
        })
      }
    }

    // Pod-specific: compute problem counts (multi-select, different semantics)
    let problems: Array<{ value: string; count: number }> | undefined
    if (kindLower === 'pods') {
      const problemCounts: Record<string, number> = {}
      POD_PROBLEMS.forEach(p => problemCounts[p] = 0)

      for (const pod of resources) {
        const podProblems = getPodProblems(pod)
        const restarts = getPodRestarts(pod)
        for (const category of POD_PROBLEMS) {
          if (podMatchesProblemCategory(podProblems, restarts, category)) {
            problemCounts[category]++
          }
        }
      }

      const activeProblems = POD_PROBLEMS
        .map(p => ({ value: p, count: problemCounts[p] }))
        .filter(p => p.count > 0)
      if (activeProblems.length > 0) {
        problems = activeProblems
      }
    }

    // Workload-specific: compute problem counts
    if (WORKLOAD_KINDS.has(kindLower)) {
      const problemCounts: Record<string, number> = {}
      WORKLOAD_PROBLEMS.forEach(p => problemCounts[p] = 0)

      for (const resource of resources) {
        const workloadProblems = getWorkloadProblems(resource, kindLower)
        for (const category of WORKLOAD_PROBLEMS) {
          if (workloadMatchesProblemCategory(workloadProblems, category)) {
            problemCounts[category]++
          }
        }
      }

      const activeProblems = WORKLOAD_PROBLEMS
        .map(p => ({ value: p, count: problemCounts[p] }))
        .filter(p => p.count > 0)
      if (activeProblems.length > 0) {
        problems = activeProblems
      }
    }

    // Compute available labels for label filtering
    const labelCounts: Record<string, number> = {}
    for (const r of resources) {
      const labels = r.metadata?.labels || {}
      for (const [key, value] of Object.entries(labels)) {
        // Skip internal/noisy labels
        if (key.includes('pod-template-hash') || key.includes('controller-revision-hash')) continue
        const pair = `${key}=${value}`
        labelCounts[pair] = (labelCounts[pair] || 0) + 1
      }
    }
    const labelValues = Object.entries(labelCounts)
      .map(([pair, count]) => ({ value: pair, count }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 30) // cap at 30 most common labels


    if (filterableColumns.length === 0 && !problems && labelValues.length === 0) return null
    return { columns: filterableColumns, problems, labels: labelValues }
  }, [resources, selectedKind.name, selectedKind.group, allColumns])

  // Map filterable columns by key for O(1) lookup in header rendering
  const filterableColumnMap = useMemo(() => {
    const map = new Map<string, { key: string; label: string; values: Array<{ value: string; count: number }> }>()
    if (filterOptions) {
      for (const col of filterOptions.columns) map.set(col.key, col)
    }
    return map
  }, [filterOptions])

  // Compute inactive ReplicaSet count for toggle display
  const inactiveReplicaSetCount = useMemo(() => {
    if (selectedKind.name.toLowerCase() !== 'replicasets' || !resources) return 0
    return resources.filter((r: any) => !isReplicaSetActive(r)).length
  }, [resources, selectedKind.name])

  // Check if any filters are active
  const hasOwnerFilter = ownerKind !== '' && ownerName !== ''
  // Namespace contribution gated on a host-wired clearer: without it the
  // Clear filters button can't drop the namespace, so showing it would be
  // a no-op for that case.
  const hasAnyFilter =
    !!searchTerm ||
    regexMode ||
    !!labelSelector ||
    hasOwnerFilter ||
    problemFilters.length > 0 ||
    Object.values(columnFilters).some((vals) => vals.length > 0) ||
    showInactiveReplicaSets ||
    (!!onClearNamespaces && namespaces.length > 0)


  // Toggle problem filter
  const toggleProblemFilter = useCallback((problem: string) => {
    setProblemFilters(prev =>
      prev.includes(problem)
        ? prev.filter(p => p !== problem)
        : [...prev, problem]
    )
  }, [])

  // Toggle a label pair in the label selector (e.g., "app=nginx")
  const toggleLabelFilter = useCallback((pair: string) => {
    setLabelSelector(prev => {
      const existing = prev ? prev.split(',').filter(Boolean) : []
      const newLabels = existing.includes(pair)
        ? existing.filter(p => p !== pair)
        : [...existing, pair]
      const newSelector = newLabels.join(',')
      // Sync to URL — route through `navigate` so the onNavigate prop
      // can suppress the write.
      const params = new URLSearchParams(window.location.search)
      if (newSelector) {
        params.set('labels', newSelector)
      } else {
        params.delete('labels')
      }
      navigate({ pathname: window.location.pathname, search: params.toString() }, { replace: true })
      return newSelector
    })
  }, [navigate])

  // Parse active label pairs for display
  const activeLabelPairs = useMemo(() => {
    if (!labelSelector) return []
    return labelSelector.split(',').filter(Boolean)
  }, [labelSelector])

  const resourcesViewDataContextValue = useMemo<ResourcesViewData>(() => ({
    onNavigate,
    certExpiry,
    certExpiryError,
    auditBadges,
    onOpenLogs,
    onOpenWorkloadLogs,
  }), [onNavigate, certExpiry, certExpiryError, auditBadges, onOpenLogs, onOpenWorkloadLogs])

  return (
    <ResourcesViewDataContext.Provider value={resourcesViewDataContextValue}>
    <div className="flex h-full w-full">
      {/* Sidebar - Resource Types */}
      {!hideSidebar && (
        <ResourcesSidebar
          selectedKind={selectedKind}
          onSelectedKindChange={(kind) => {
            shouldPushHistory.current = true
            setSelectedKind(kind)
          }}
          onKindChange={onKindChange}
          apiResources={apiResourcesProp}
          resourceCounts={counts}
          resourceForbidden={Array.from(forbiddenKinds)}
          resourceUnavailable={sidebarResourceUnavailable}
          pinned={pinned}
          togglePin={togglePin}
          isPinned={isPinned}
          onKindNavigated={() => {
            // After selecting a kind via keyboard, move focus to the table search
            // so the user can immediately filter within the selected kind.
            setTimeout(() => searchInputRef.current?.focus(), 50)
          }}
        />
      )}

      {/* Main Content - Resource Table */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0 bg-theme-surface">
        {/* Toolbar */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-theme-border bg-theme-base shrink-0">
          <div className="flex-1 min-w-0">
            <div className="relative max-w-md">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
              <Input
                ref={searchInputRef}
                placeholder={regexMode ? 'Search by regex... (press /)' : 'Search... (press /)'}
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'ArrowDown') {
                    // Hand off to the table's keyboard navigation — blur the input
                    // so the registered ArrowDown/j/k shortcuts take over, and
                    // highlight the first row.
                    e.preventDefault()
                    searchInputRef.current?.blur()
                    setHighlightedIndex(0)
                  } else if (e.key === 'Enter' && filteredResourceCountRef.current > 0) {
                    // Select the first (or currently highlighted) resource
                    e.preventDefault()
                    searchInputRef.current?.blur()
                    if (highlightedIndex < 0) setHighlightedIndex(0)
                    // Defer to next frame so the highlight renders before we open
                    requestAnimationFrame(() => {
                      const res = highlightedResourceRef.current ?? filteredResources[0]
                      selectResource(res)
                    })
                  } else if (e.key === 'Escape') {
                    searchInputRef.current?.blur()
                  }
                }}
                className={clsx(
                  'w-full pl-10 py-2 bg-theme-elevated border rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2',
                  searchTerm ? 'pr-16' : 'pr-10',
                  searchRegex.error
                    ? 'border-red-500/60 focus:ring-red-500'
                    : 'border-theme-border-light focus:ring-skyhook-500'
                )}
              />
              <button
                type="button"
                onClick={() => setRegexMode((v) => !v)}
                aria-pressed={regexMode}
                aria-label={regexMode ? 'Disable regex search' : 'Enable regex search'}
                title={regexMode ? 'Regex search enabled — click to disable' : 'Enable regex search'}
                className={clsx(
                  'absolute top-1/2 -translate-y-1/2 flex items-center justify-center w-6 h-6 rounded transition-colors',
                  searchTerm ? 'right-9' : 'right-2',
                  regexMode
                    ? 'bg-skyhook-500/20 text-skyhook-400'
                    : 'text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover'
                )}
              >
                <Regex className="w-3.5 h-3.5" />
              </button>
              {searchTerm && (
                <button
                  type="button"
                  aria-label="Clear search"
                  onClick={() => {
                    setSearchTerm('')
                    searchInputRef.current?.focus()
                  }}
                  className="absolute right-2 top-1/2 -translate-y-1/2 flex items-center justify-center w-6 h-6 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
                >
                  <X className="w-3.5 h-3.5" />
                </button>
              )}
              {searchRegex.error && (
                <div
                  title={searchRegex.error}
                  className="absolute left-0 top-full mt-1 z-10 px-2 py-1 rounded bg-theme-elevated border border-red-500/40 text-[11px] text-red-400 shadow-theme-sm"
                >
                  Invalid regex pattern
                </div>
              )}
            </div>
          </div>

          {/* Problems dropdown (pods only) */}
          {filterOptions?.problems && filterOptions.problems.length > 0 && (
            <div className="relative" ref={problemsDropdownRef}>
              <button
                onClick={() => { setShowProblemsDropdown(!showProblemsDropdown); setShowLabelsDropdown(false) }}
                className={clsx(
                  'flex items-center gap-1.5 px-2.5 py-2 rounded-lg text-xs transition-colors',
                  problemFilters.length > 0
                    ? `${SEVERITY_BADGE.error} hover:bg-red-500/30`
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                )}
              >
                <AlertTriangle className="w-3.5 h-3.5" />
                <span>Problems</span>
                {problemFilters.length > 0 && (
                  <span className={clsx('badge-sm', SEVERITY_BADGE.error)}>
                    {problemFilters.length}
                  </span>
                )}
              </button>
              {showProblemsDropdown && (
                <div className="absolute right-0 top-full mt-1 min-w-48 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50">
                  {problemFilters.length > 0 && (
                    <div className="flex items-center justify-between px-3 py-1.5 border-b border-theme-border">
                      <span className="text-xs font-medium text-theme-text-secondary">Problems</span>
                      <button onClick={() => { setProblemFilters([]); setShowProblemsDropdown(false) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 -mr-1 rounded transition-colors">Clear</button>
                    </div>
                  )}
                  <div className="py-1">
                    {filterOptions.problems.map(({ value, count }) => (
                      <button
                        key={value}
                        onClick={() => toggleProblemFilter(value)}
                        className={clsx(
                          'w-full text-left px-3 py-1.5 text-xs flex items-center justify-between gap-2 transition-colors',
                          problemFilters.includes(value)
                            ? SEVERITY_BADGE.error
                            : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                        )}
                      >
                        <span className="truncate">{value}</span>
                        <span className="text-theme-text-disabled shrink-0">({count})</span>
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}

          {/* Labels dropdown */}
          {filterOptions?.labels && filterOptions.labels.length > 0 && (
            <div className="relative" ref={labelsDropdownRef}>
              <button
                onClick={() => { setShowLabelsDropdown(!showLabelsDropdown); setShowProblemsDropdown(false); setLabelSearch('') }}
                className={clsx(
                  'flex items-center gap-1.5 px-2.5 py-2 rounded-lg text-xs transition-colors',
                  activeLabelPairs.length > 0
                    ? `${SEVERITY_BADGE.success} hover:bg-emerald-500/30`
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                )}
              >
                <Tag className="w-3.5 h-3.5" />
                <span>Labels</span>
                {activeLabelPairs.length > 0 && (
                  <span className={clsx('badge-sm', SEVERITY_BADGE.success)}>
                    {activeLabelPairs.length}
                  </span>
                )}
              </button>
              {showLabelsDropdown && (() => {
                const labels = filterOptions.labels ?? []
                const filtered = labelSearch
                  ? labels.filter(l => l.value.toLowerCase().includes(labelSearch.toLowerCase()))
                  : labels
                return (
                  <div className="absolute right-0 top-full mt-1 min-w-64 max-w-80 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50">
                    <div className="flex items-center gap-2 p-2 border-b border-theme-border">
                      <div className="relative flex-1">
                        <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary" />
                        <Input
                          placeholder="Search labels..."
                          value={labelSearch}
                          onChange={(e) => setLabelSearch(e.target.value)}
                          autoFocus
                          className="w-full pl-7 pr-2 py-1.5 text-xs bg-theme-elevated border border-theme-border-light rounded text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-1 focus:ring-skyhook-500"
                        />
                      </div>
                      {activeLabelPairs.length > 0 && (
                        <button onClick={() => { setLabelSelector(''); setShowLabelsDropdown(false) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 rounded transition-colors shrink-0">Clear</button>
                      )}
                    </div>
                    <div className="py-1 max-h-64 overflow-y-auto">
                      {filtered.map(({ value, count }) => (
                        <button
                          key={value}
                          onClick={() => toggleLabelFilter(value)}
                          className={clsx(
                            'w-full text-left px-3 py-1.5 text-xs flex items-center justify-between gap-2 transition-colors',
                            activeLabelPairs.includes(value)
                              ? SEVERITY_BADGE.success
                              : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                          )}
                        >
                          <span className="truncate" title={value}>{value}</span>
                          <span className="text-theme-text-disabled shrink-0">({count})</span>
                        </button>
                      ))}
                      {filtered.length === 0 && (
                        <div className="px-3 py-2 text-xs text-theme-text-disabled">No matches</div>
                      )}
                    </div>
                  </div>
                )
              })()}
            </div>
          )}

          {/* ReplicaSet inactive toggle */}
          {selectedKind.name.toLowerCase() === 'replicasets' && inactiveReplicaSetCount > 0 && (
            <label className="flex items-center gap-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-secondary">
              <input
                type="checkbox"
                checked={showInactiveReplicaSets}
                onChange={(e) => setShowInactiveReplicaSets(e.target.checked)}
                className="w-3 h-3 rounded border-theme-border-light accent-skyhook-500"
              />
              Show inactive ({inactiveReplicaSetCount})
            </label>
          )}

          {/* Active filter badges — owner only (column filters shown on header, problems/labels on their buttons) */}
          {hasOwnerFilter && (
            <span className={clsx('flex items-center gap-1 px-2 py-1 text-xs rounded', SEVERITY_BADGE.info)}>
              {ownerKind}: {ownerName}
              <button
                onClick={() => {
                  setOwnerKind('')
                  setOwnerName('')
                  // Route through `navigate` so onNavigate-suppressing hosts
                  // can opt out of the URL write.
                  const params = new URLSearchParams(window.location.search)
                  params.delete('ownerKind')
                  params.delete('ownerName')
                  navigate({ pathname: window.location.pathname, search: params.toString() }, { replace: true })
                }}
                className="hover:text-theme-text-primary"
              >
                <X className="w-3 h-3" />
              </button>
            </span>
          )}

          {hasAnyFilter && (
            <Tooltip content={!!onClearNamespaces && namespaces.length > 0 ? 'Reset all filters and the active namespace' : 'Reset all filters'}>
              <button
                type="button"
                onClick={clearAllFilters}
                className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated transition-colors"
              >
                <RotateCcw className="w-3.5 h-3.5" />
                <span>Clear filters</span>
              </button>
            </Tooltip>
          )}

          {/* Column picker */}
          <div className="relative" ref={columnPickerRef}>
            <Tooltip content="Configure columns">
            <button
              onClick={() => setShowColumnPicker(prev => !prev)}
              className={clsx(
                'p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg',
                showColumnPicker && 'bg-theme-elevated text-theme-text-primary'
              )}
            >
              <Columns3 className="w-4 h-4" />
            </button>
            </Tooltip>
            {showColumnPicker && (
              <div className="absolute right-0 top-full mt-1 z-50 bg-theme-surface border border-theme-border rounded-lg shadow-lg py-1 min-w-[200px] max-h-[400px] flex flex-col">
                <div className="shrink-0 px-3 py-2 border-b border-theme-border flex items-center justify-between">
                  <span className="text-xs font-medium text-theme-text-secondary uppercase">Columns</span>
                  <button
                    onClick={resetColumnSettings}
                    className="text-xs text-theme-text-tertiary hover:text-theme-text-primary flex items-center gap-1"
                    title="Reset to defaults"
                  >
                    <RotateCcw className="w-3 h-3" />
                    Reset
                  </button>
                </div>
                {/* Column list scrolls; the add-column footer stays pinned so
                    its input isn't pushed below the fold on kinds with many columns. */}
                <div className="overflow-y-auto flex-1 min-h-0">
                {allColumns.map(col => {
                  const isCustom = customColumnKeySet.has(col.key)
                  return (
                    <div
                      key={col.key}
                      className="group flex items-center gap-2 px-3 py-1.5 hover:bg-theme-elevated"
                    >
                      <label className="flex items-center gap-2 flex-1 min-w-0 cursor-pointer">
                        <input
                          type="checkbox"
                          checked={visibleColumns.has(col.key)}
                          onChange={() => toggleColumnVisibility(col.key)}
                          disabled={col.key === 'name'}
                          className="rounded border-theme-border"
                        />
                        <span className={clsx(
                          'text-sm truncate',
                          col.key === 'name' ? 'text-theme-text-tertiary' : 'text-theme-text-primary'
                        )} title={col.tooltip || col.label}>
                          {col.label}
                        </span>
                      </label>
                      {isCustom && (
                        <button
                          type="button"
                          onClick={() => removeCustomColumn(col.key)}
                          className="shrink-0 p-0.5 text-theme-text-tertiary hover:text-red-500 opacity-0 group-hover:opacity-100 focus:opacity-100"
                          title="Remove column"
                        >
                          <X className="w-3.5 h-3.5" />
                        </button>
                      )}
                    </div>
                  )
                })}
                </div>
                {/* Add a label/annotation column */}
                <div className="shrink-0 border-t border-theme-border pt-2 px-3 pb-1">
                  <div className="flex items-center gap-1 mb-1.5">
                    {(['label', 'annotation'] as CustomColumnSource[]).map(src => (
                      <button
                        key={src}
                        type="button"
                        onClick={() => setCustomColumnDraft(d => ({ ...d, source: src }))}
                        className={clsx(
                          'px-2 py-0.5 text-xs rounded capitalize',
                          customColumnDraft.source === src
                            ? 'bg-skyhook-500/20 text-skyhook-400'
                            : 'text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated'
                        )}
                      >
                        {src}
                      </button>
                    ))}
                  </div>
                  <form
                    className="flex items-center gap-1"
                    onSubmit={e => {
                      e.preventDefault()
                      addCustomColumn(customColumnDraft)
                      setCustomColumnDraft(d => ({ ...d, path: '' }))
                    }}
                  >
                    <Input
                      list={customColKeysListId}
                      value={customColumnDraft.path}
                      onChange={e => setCustomColumnDraft(d => ({ ...d, path: e.target.value }))}
                      placeholder={`Add ${customColumnDraft.source} key…`}
                      className="flex-1 min-w-0 px-2 py-1 text-xs rounded bg-theme-base border border-theme-border text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                    />
                    <datalist id={customColKeysListId}>
                      {customColumnKeySuggestions[customColumnDraft.source].map(k => (
                        <option key={k} value={k} />
                      ))}
                    </datalist>
                    <button
                      type="submit"
                      disabled={!customColumnDraft.path.trim()}
                      className="shrink-0 p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-40 disabled:hover:bg-transparent"
                      title="Add column"
                    >
                      <Plus className="w-4 h-4" />
                    </button>
                  </form>
                </div>
              </div>
            )}
          </div>
          {onCreateResource && (
            <Tooltip content={`Create ${selectedKind.kind || 'resource'}`}>
              <button
                onClick={() => onCreateResource(selectedKind)}
                className="p-2 hover:bg-theme-elevated rounded-lg text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              >
                <Plus className="w-4 h-4" />
              </button>
            </Tooltip>
          )}
          {compareEnabled && (
            <Tooltip content={compareMode ? 'Exit compare mode' : 'Compare two resources side-by-side'}>
              <button
                onClick={() => {
                  if (compareMode) { exitCompareMode(); return }
                  exitBulkMode()
                  setCompareMode(true)
                }}
                aria-pressed={compareMode}
                className={clsx(
                  'p-2 rounded-lg transition-colors',
                  compareMode
                    ? 'bg-skyhook-500/15 text-skyhook-300 border border-skyhook-400/50'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated',
                )}
              >
                <GitCompare className="w-4 h-4" />
              </button>
            </Tooltip>
          )}
          {canBulkSelect && (
            <Tooltip content={bulkMode ? 'Exit bulk select mode' : 'Select multiple resources'}>
              <button
                onClick={() => {
                  if (bulkMode) { exitBulkMode(); return }
                  exitCompareMode()
                  setBulkMode(true)
                }}
                aria-pressed={bulkMode}
                className={clsx(
                  'p-2 rounded-lg transition-colors',
                  bulkMode
                    ? 'bg-skyhook-500/15 text-skyhook-300 border border-skyhook-400/50'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated',
                )}
              >
                <ListChecks className="w-4 h-4" />
              </button>
            </Tooltip>
          )}
          {/* Freshness/liveness status — trailing and divided off from the action
              buttons so it reads as a status, not another control. Resources has
              no PageHeader, so this toolbar is its home. */}
          <div className="mx-1 h-5 w-px bg-theme-border/60" />
          {/* The list is SSE-invalidated (near-real-time), so it reads
              "Auto-updating" with no refresh button — the stream keeps it
              current, so a manual refresh would only undercut the claim. */}
          <FreshnessControl mode="auto" connectionState={connectionState} />
        </div>

        {/* Bulk actions bar */}
        {isCheckboxMode && (
          <div className="flex items-center gap-3 px-4 py-2 bg-skyhook-500/10 border-b border-skyhook-400/20 shrink-0">
            <span className="text-sm font-medium text-theme-text-primary">
              {checkedItems.length} selected
            </span>
            {canBulkRestartSelectedKind && (
              <button
                type="button"
                onClick={() => setShowBulkRestartConfirm(true)}
                disabled={checkedItems.length === 0 || isBulkMutating}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand-muted disabled:opacity-50 disabled:pointer-events-none rounded-lg transition-colors"
              >
                <RefreshCw className={clsx('w-3.5 h-3.5', isBulkRestarting && 'animate-spin')} />
                {isBulkRestarting ? 'Restarting...' : 'Restart'}
              </button>
            )}
            {canBulkScaleSelectedKind && (
              <button
                type="button"
                onClick={openBulkScaleDialog}
                disabled={checkedItems.length === 0 || isBulkMutating}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium bg-theme-elevated hover:bg-theme-hover disabled:opacity-50 disabled:pointer-events-none text-theme-text-primary border border-theme-border rounded-lg transition-colors"
              >
                <Scale className="w-3.5 h-3.5" />
                {isBulkScaling ? 'Scaling...' : 'Scale'}
              </button>
            )}
            {onBulkDelete && (
              <button
                type="button"
                onClick={() => setShowBulkDeleteConfirm(true)}
                disabled={checkedItems.length === 0 || isBulkMutating}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium bg-red-600 hover:bg-red-700 disabled:opacity-50 disabled:pointer-events-none text-white rounded-lg transition-colors"
              >
                <Trash2 className="w-3.5 h-3.5" />
                Delete
              </button>
            )}
            <button
              type="button"
              onClick={exitBulkMode}
              className="px-3 py-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors"
            >
              Cancel
            </button>
          </div>
        )}

        {/* Table */}
        <div
          className="flex-1 overflow-auto relative"
          ref={tableContainerRef}
          onClick={(e) => {
            if (e.target === e.currentTarget && selectedResource) {
              onResourceClick?.(null)
            }
          }}
        >
          {isLoading ? (
            <PaneLoader className="absolute inset-0" />
          ) : isSelectedForbidden ? (
            // overflow-y-auto + min-h-full: centered when the panel is short
            // (collapsed), scrollable (no top clip) when the RBAC snippet is
            // expanded on a shorter pane.
            <div className="absolute inset-0 overflow-y-auto">
              <div className="min-h-full flex items-center justify-center">
                <RestrictedState
                  kindLabel={selectedKind.kind}
                  group={selectedKind.group}
                  resource={selectedKind.name}
                  reason={resourceReasons?.[selectedKindCountKey]}
                />
              </div>
            </div>
          ) : largeListGuard ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center text-theme-text-tertiary px-6 text-center">
              <AlertTriangle className="w-8 h-8 text-amber-400 mb-2" />
              <p className="text-theme-text-secondary font-medium">
                {largeListGuard.reason === 'count-unavailable'
                  ? `${largeListGuard.kind} count unavailable`
                  : `Too many ${largeListGuard.kind.toLowerCase()} to show`}
              </p>
              <p className="text-sm mt-1 max-w-xl">
                {largeListGuard.reason === 'count-unavailable'
                  ? 'Radar could not verify this list is small enough to load. Choose a namespace or smaller namespace set to try again.'
                  : `${largeListGuard.count?.toLocaleString()} ${largeListGuard.kind.toLowerCase()} are in the current scope. Choose a namespace or smaller namespace set to load this view.`}
              </p>
              <p className="text-xs mt-2 text-theme-text-disabled">
                Radar limits full table loads to {largeListGuard.limit.toLocaleString()} resources to keep the UI responsive.
              </p>
              <p className="text-xs mt-2 text-theme-text-disabled">
                Scope: {formatLargeListScope(largeListGuard.namespaces)}
              </p>
            </div>
          ) : filteredResources.length === 0 ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center text-theme-text-tertiary">
              <p>No {selectedKind.kind} found</p>
              {emptyKindNote(selectedKind.kind, selectedKind.group, resources?.length ?? 0) && (
                <p className="text-xs mt-2 max-w-md text-center text-theme-text-disabled">
                  {emptyKindNote(selectedKind.kind, selectedKind.group, resources?.length ?? 0)}
                </p>
              )}
              {searchTerm && (
                <button
                  onClick={() => {
                    setSearchTerm('')
                    setRegexMode(false)
                  }}
                  className="flex items-center gap-1.5 text-sm mt-2 px-3 py-1.5 rounded-md bg-theme-elevated hover:bg-theme-border text-theme-text-secondary hover:text-theme-text-primary transition-colors"
                >
                  No results for "{searchTerm}"
                  <X className="w-3.5 h-3.5" />
                </button>
              )}
              {namespaces.length > 0 && <p className="text-sm mt-1 text-theme-text-disabled">Searching in {namespaces.length === 1 ? `namespace: ${namespaces[0]}` : `${namespaces.length} namespaces`}</p>}
              {/* Show active filters as dismissible badges so user can clear them */}
              {(() => {
                const activeColEntries = Object.entries(columnFilters).filter(([, vals]) => vals.length > 0)
                if (activeColEntries.length === 0 && problemFilters.length === 0 && !labelSelector) return null
                return (
                  <div className="flex flex-wrap items-center gap-1.5 mt-3">
                    {activeColEntries.map(([key, vals]) => (
                      <button
                        key={key}
                        onClick={() => clearColumnFilter(key)}
                        className="flex items-center gap-1 px-2 py-1 text-xs selection selection-text rounded-md hover:selection-strong transition-colors"
                      >
                        <ListFilter className="w-3 h-3" />
                        <span>{key}: {columnFilterExcludes[key] ? 'not ' : ''}{vals.join(', ')}</span>
                        <X className="w-3 h-3" />
                      </button>
                    ))}
                    {problemFilters.length > 0 && (
                      <button
                        onClick={() => setProblemFilters([])}
                        className="flex items-center gap-1 px-2 py-1 text-xs bg-red-500/15 text-red-700 dark:text-red-300 rounded-md hover:bg-red-500/25 transition-colors"
                      >
                        <AlertTriangle className="w-3 h-3" />
                        <span>Problems: {problemFilters.join(', ')}</span>
                        <X className="w-3 h-3" />
                      </button>
                    )}
                    {labelSelector && (
                      <button
                        onClick={() => setLabelSelector('')}
                        className="flex items-center gap-1 px-2 py-1 text-xs bg-green-500/15 text-green-700 dark:text-green-300 rounded-md hover:bg-green-500/25 transition-colors"
                      >
                        <Tag className="w-3 h-3" />
                        <span>{labelSelector}</span>
                        <X className="w-3 h-3" />
                      </button>
                    )}
                  </div>
                )
              })()}
              {hasAnyFilter && (
                <button
                  type="button"
                  onClick={clearAllFilters}
                  className="flex items-center gap-1.5 mt-3 px-3 py-1.5 text-sm rounded-md bg-theme-elevated hover:bg-theme-border text-theme-text-secondary hover:text-theme-text-primary transition-colors"
                >
                  <RotateCcw className="w-3.5 h-3.5" />
                  Clear filters
                </button>
              )}
            </div>
          ) : (
            <MetricsContext.Provider value={metricsLookup}>
            <TableVirtuoso
              ref={virtuosoRef}
              data={filteredResources}
              increaseViewportBy={400}
              overscan={200}
              computeItemKey={(index, resource) => resource.metadata?.uid || `${resource.metadata?.namespace}-${resource.metadata?.name}-${index}`}
              components={virtuosoComponents}
              fixedHeaderContent={() => (
                <tr>
                  {compareMode && (
                    <th
                      // Inline px width — under `table-layout:fixed`,
                      // `w-9` is a hint the browser absorbs into leftover
                      // row width on an icon-only column.
                      style={COMPARE_COLUMN_STYLE}
                      className="px-2 py-3 text-xs font-medium uppercase tracking-wide bg-theme-base border-b border-r-subtle border-theme-border text-center text-skyhook-400"
                      title="Compare mode"
                    >
                      <GitCompare className="w-3.5 h-3.5 inline-block opacity-70" />
                    </th>
                  )}
                  {isCheckboxMode && (
                    <th className="bg-theme-surface border-b border-theme-border w-10 text-center px-0 py-3">
                      <input
                        type="checkbox"
                        checked={allVisibleChecked}
                        ref={(el) => { if (el) el.indeterminate = checkedItems.length > 0 && checkedItems.length < filteredResources.length }}
                        onChange={toggleCheckAll}
                        className="w-3.5 h-3.5 rounded border-theme-border accent-skyhook-500 cursor-pointer"
                        title={checkedItems.length > 0 ? 'Deselect all' : 'Select all'}
                      />
                    </th>
                  )}
                  {columns.map((col, colIdx) => {
                    // Built-in sortable keys, plus any extra/custom column that carries its own getSortValue.
                    const isSortable = isColumnSortable(col, extraColumnsByKey)
                    const isSorted = sortColumn === col.key
                    const isLastCol = colIdx === columns.length - 1
                    const filterCol = filterableColumnMap.get(col.key)
                    const activeFilterValues = columnFilters[col.key] || []
                    const hasActiveFilter = activeFilterValues.length > 0
                    const isFilterOpen = openColumnFilter === col.key
                    return (
                      <th
                        key={col.key}
                        className={clsx(
                          'text-left px-4 py-3 text-xs font-medium uppercase tracking-wide relative group/th',
                          'bg-theme-base border-b border-theme-border',
                          !isLastCol && 'border-r-subtle',
                          isSortable ? 'text-theme-text-secondary hover:text-theme-text-primary cursor-pointer select-none' : 'text-theme-text-secondary'
                        )}
                        onClick={isSortable ? () => handleSort(col.key) : undefined}
                      >
                        <div className="flex items-center gap-1 overflow-hidden">
                          {col.tooltip ? (
                            <Tooltip content={col.tooltip}>
                              <span className="border-b border-dotted border-theme-text-tertiary truncate">{col.label}</span>
                            </Tooltip>
                          ) : (
                            <HeaderLabel label={col.label} />
                          )}
                          {isSortable && (
                            <span className="text-theme-text-tertiary shrink-0">
                              {isSorted ? (
                                sortDirection === 'asc' ? (
                                  <ChevronUp className="w-3.5 h-3.5" />
                                ) : (
                                  <ChevronDown className="w-3.5 h-3.5" />
                                )
                              ) : (
                                <ArrowUpDown className="w-3 h-3 opacity-50" />
                              )}
                            </span>
                          )}
                          {filterCol && (
                            <span className="shrink-0 flex items-center gap-0">
                              <button
                                data-column-filter-trigger
                                onClick={(e) => {
                                  e.stopPropagation()
                                  if (isFilterOpen) {
                                    setOpenColumnFilter(null)
                                  } else {
                                    setOpenColumnFilter(col.key)
                                    setColumnFilterSearch('')
                                  }
                                }}
                                className={clsx(
                                  'rounded-l transition-colors flex items-center gap-0.5',
                                  hasActiveFilter
                                    ? 'px-1.5 py-0.5 -my-0.5 selection-strong selection-text hover:bg-skyhook-500/30'
                                    : isFilterOpen
                                      ? 'p-0.5 text-theme-text-primary'
                                      : 'p-0.5 text-theme-text-disabled opacity-40 group-hover/th:opacity-100 hover:text-theme-text-primary'
                                )}
                              >
                                <ListFilter className="w-3 h-3" />
                                {hasActiveFilter && (
                                  <span
                                    className="text-[10px] leading-none font-semibold"
                                    aria-label={`${activeFilterValues.length} ${columnFilterExcludes[col.key] ? 'excluded' : 'included'} value${activeFilterValues.length === 1 ? '' : 's'}`}
                                  >
                                    {columnFilterExcludes[col.key] ? `≠ ${activeFilterValues.length}` : activeFilterValues.length}
                                  </span>
                                )}
                              </button>
                              {hasActiveFilter && (
                                <button
                                  data-column-filter-trigger
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    clearColumnFilter(col.key)
                                    setOpenColumnFilter(null)
                                  }}
                                  className="rounded-r px-0.5 py-0.5 -my-0.5 selection-strong selection-text hover:bg-skyhook-500/30 transition-colors"
                                  title="Clear filter"
                                >
                                  <X className="w-3 h-3" />
                                </button>
                              )}
                            </span>
                          )}
                        </div>
                        {/* Column filter dropdown */}
                        {filterCol && isFilterOpen && (() => {
                          const values = filterCol.values
                          const filtered = columnFilterSearch
                            ? values.filter(v => v.value.toLowerCase().includes(columnFilterSearch.toLowerCase()))
                            : values
                          return (
                            <div
                              ref={columnFilterDropdownRef}
                              className={clsx(
                                'absolute top-full mt-1 min-w-48 max-w-64 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50',
                                isLastCol ? 'right-0' : 'left-0'
                              )}
                              onClick={(e) => e.stopPropagation()}
                            >
                              <div className="flex items-center gap-1 p-1.5 border-b border-theme-border" role="group" aria-label={`${col.label} filter mode`}>
                                <button
                                  onClick={() => setColumnFilterMode(col.key, false)}
                                  aria-pressed={!columnFilterExcludes[col.key]}
                                  className={clsx(
                                    'flex-1 px-2 py-1 text-xs rounded transition-colors',
                                    !columnFilterExcludes[col.key]
                                      ? 'selection-strong selection-text'
                                      : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                                  )}
                                  title="Show rows matching the selected values"
                                >
                                  Include
                                </button>
                                <button
                                  onClick={() => setColumnFilterMode(col.key, true)}
                                  aria-pressed={Boolean(columnFilterExcludes[col.key])}
                                  className={clsx(
                                    'flex-1 px-2 py-1 text-xs rounded transition-colors',
                                    columnFilterExcludes[col.key]
                                      ? 'selection-strong selection-text'
                                      : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                                  )}
                                  title="Show rows that do NOT match the selected values"
                                >
                                  Exclude
                                </button>
                              </div>
                              {values.length > 5 ? (
                                <div className="flex items-center gap-2 p-2 border-b border-theme-border">
                                  <div className="relative flex-1">
                                    <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary" />
                                    <Input
                                      placeholder="Search..."
                                      value={columnFilterSearch}
                                      onChange={(e) => setColumnFilterSearch(e.target.value)}
                                      autoFocus
                                      className="w-full pl-7 pr-2 py-1.5 text-xs bg-theme-elevated border border-theme-border-light rounded text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-1 focus:ring-skyhook-500"
                                    />
                                  </div>
                                  {activeFilterValues.length > 0 && (
                                    <button onClick={() => { clearColumnFilter(col.key); setOpenColumnFilter(null) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 rounded transition-colors shrink-0">Clear</button>
                                  )}
                                </div>
                              ) : activeFilterValues.length > 0 ? (
                                <div className="flex items-center justify-between px-3 py-1.5 border-b border-theme-border">
                                  <span className="text-xs font-medium text-theme-text-secondary">{col.label}</span>
                                  <button onClick={() => { clearColumnFilter(col.key); setOpenColumnFilter(null) }} className="text-xs text-theme-text-tertiary hover:text-theme-text-primary px-1 py-0.5 -mr-1 rounded transition-colors">Clear</button>
                                </div>
                              ) : null}
                              <div className="py-1 max-h-64 overflow-y-auto">
                                {filtered.map(({ value, count }) => {
                                  const isSelected = activeFilterValues.includes(value)
                                  return (
                                    <button
                                      key={value}
                                      onClick={() => toggleColumnFilterValue(col.key, value)}
                                      className={clsx(
                                        'w-full text-left px-3 py-1.5 text-xs flex items-center gap-2 transition-colors',
                                        isSelected
                                          ? 'selection-strong selection-text'
                                          : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                                      )}
                                    >
                                      <span className={clsx('w-3 h-3 shrink-0 rounded-sm border flex items-center justify-center', isSelected ? 'bg-skyhook-500 border-skyhook-500' : 'border-theme-border')}>
                                        {isSelected && <Check className="w-2 h-2 text-white" />}
                                      </span>
                                      <span className="truncate" title={value}>{value}</span>
                                      <span className="text-theme-text-disabled shrink-0 ml-auto">({count})</span>
                                    </button>
                                  )
                                })}
                                {filtered.length === 0 && (
                                  <div className="px-3 py-2 text-xs text-theme-text-disabled">No matches</div>
                                )}
                              </div>
                            </div>
                          )
                        })()}
                        {/* Resize handle with visible divider */}
                        <div
                          className="absolute right-0 top-0 bottom-0 w-3 cursor-col-resize flex items-center justify-center"
                          style={{ transform: 'translateX(50%)' , zIndex: 10 }}
                          onMouseDown={(e) => {
                            const th = e.currentTarget.parentElement!
                            handleResizeStart(e, col.key, th.getBoundingClientRect().width)
                          }}
                        >
                          <div className="w-px h-4 bg-theme-border group-hover/th:bg-theme-text-disabled transition-colors" />
                        </div>
                      </th>
                    )
                  })}
                  {hasResizedColumns && <th className="bg-theme-base border-b border-theme-border p-0" />}
                </tr>
              )}
              itemContent={(index, resource) => {
                const isSelected = selectedResource?.kind === selectedKind.name &&
                  selectedResource?.namespace === resource.metadata?.namespace &&
                  selectedResource?.name === resource.metadata?.name
                const isHighlighted = index === highlightedIndex
                const pickIdx = compareMode
                  ? pickIndex(comparePicks, {
                      namespace: resource.metadata?.namespace || '',
                      name: resource.metadata?.name || '',
                      clusterId: resolveRowCluster?.(resource)?.id,
                    })
                  : -1
                const resourceKey = getResourceKey(resource)
                return (
                  <ResourceRowCells
                    resource={resource}
                    kind={selectedKind.name}
                    group={selectedKind.group}
                    columns={columns}
                    extraColumnsByKey={extraColumnsByKey}
                    hasSpacerColumn={hasResizedColumns}
                    isSelected={isSelected}
                    isHighlighted={isHighlighted}
                    isChecked={checkedResources.has(resourceKey)}
                    showCheckbox={isCheckboxMode}
                    majorityNodeMinorVersion={majorityNodeMinorVersion}
                    onRowClick={handleRowClick}
                    onRowMouseEnter={handleRowMouseEnter}
                    compareMode={compareMode}
                    comparePickIndex={pickIdx}
                    rowHref={rowHrefFor?.(resource)}
                    onRowCheckToggle={toggleChecked}
                  />
                )
              }}
              className="h-full"
            />
            {/* Resize indicator line — full table height, shown only while dragging */}
            {resizeLineX !== null && (
              <div
                className="absolute top-0 bottom-0 w-0.5 bg-skyhook-500 pointer-events-none"
                style={{ left: resizeLineX, zIndex: 20 }}
              />
            )}
            </MetricsContext.Provider>
          )}
        </div>
        {compareMode && compareEnabled && (
          <CompareTray
            kind={selectedKind.name}
            picks={comparePicks}
            onRemove={(idx) => setComparePicks(prev => prev.filter((_, i) => i !== idx))}
            onCompare={handleCompareNavigate}
            onExit={exitCompareMode}
          />
        )}
      </div>
    </div>

    {/* Bulk delete confirmation */}
    <ConfirmDialog
      open={showBulkDeleteConfirm}
      onClose={() => { setShowBulkDeleteConfirm(false); setBulkForceDelete(false) }}
      onConfirm={() => {
        onBulkDelete?.(checkedBulkItems, {
          force: bulkForceDelete,
          onSuccess: () => {
            exitBulkMode()
            setShowBulkDeleteConfirm(false)
            setBulkForceDelete(false)
          },
        })
      }}
      title={`Delete ${checkedItems.length} ${selectedKind.kind}${checkedItems.length > 1 ? 's' : ''}?`}
      message={`You are about to delete ${checkedItems.length} resource${checkedItems.length > 1 ? 's' : ''}. This action cannot be undone.`}
      details={checkedItemDetails}
      confirmLabel={bulkForceDelete ? `Force Delete ${checkedItems.length} resource${checkedItems.length > 1 ? 's' : ''}` : `Delete ${checkedItems.length} resource${checkedItems.length > 1 ? 's' : ''}`}
      variant="danger"
      isLoading={isBulkDeleting}
      isClosable
    >
      <label className="flex items-center gap-2 text-sm text-theme-text-secondary">
        <input
          type="checkbox"
          checked={bulkForceDelete}
          onChange={(e) => setBulkForceDelete(e.target.checked)}
          className="w-4 h-4 rounded border-theme-border bg-theme-base text-red-600 focus:ring-red-500 focus:ring-offset-0"
        />
        <span>Force delete (strips finalizers and bypasses grace period)</span>
      </label>
    </ConfirmDialog>
    <ConfirmDialog
      open={showBulkRestartConfirm}
      onClose={() => setShowBulkRestartConfirm(false)}
      onConfirm={() => {
        onBulkRestart?.(checkedBulkItems, {
          onSuccess: () => {
            exitBulkMode()
            setShowBulkRestartConfirm(false)
          },
        })
      }}
      title={`Restart ${checkedItems.length} ${selectedKind.kind}${checkedItems.length > 1 ? 's' : ''}?`}
      message={`This will trigger a rolling restart for ${checkedItems.length} selected workload${checkedItems.length > 1 ? 's' : ''}.`}
      details={checkedItemDetails}
      confirmLabel={`Restart ${checkedItems.length} workload${checkedItems.length > 1 ? 's' : ''}`}
      variant="warning"
      isLoading={isBulkRestarting}
      isClosable
    />
    <ConfirmDialog
      open={showBulkScaleDialog}
      onClose={() => setShowBulkScaleDialog(false)}
      onConfirm={() => {
        onBulkScale?.(checkedBulkItems, bulkScaleReplicas, {
          onSuccess: () => {
            exitBulkMode()
            setShowBulkScaleDialog(false)
          },
        })
      }}
      title={`Scale ${checkedItems.length} ${selectedKind.kind}${checkedItems.length > 1 ? 's' : ''}?`}
      message={`Set every selected workload to exactly ${bulkScaleReplicas} replica${bulkScaleReplicas === 1 ? '' : 's'}.`}
      details={checkedItemDetails}
      confirmLabel={`Scale to ${bulkScaleReplicas}`}
      variant={bulkScaleReplicas === 0 ? 'danger' : 'warning'}
      isLoading={isBulkScaling}
      isClosable
    >
      <div className="space-y-3">
        <div className="flex items-center justify-center gap-3">
          <button
            type="button"
            onClick={() => setBulkScaleReplicas(Math.max(0, bulkScaleReplicas - 1))}
            disabled={bulkScaleReplicas <= 0}
            className="p-2 rounded-lg bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Minus className="w-5 h-5" />
          </button>
          <input
            type="number"
            min="0"
            max="10000"
            value={bulkScaleReplicas}
            onChange={(e) => setBulkScaleReplicas(Math.min(10000, Math.max(0, Number.parseInt(e.target.value, 10) || 0)))}
            className="w-24 text-center text-2xl font-semibold bg-theme-elevated border border-theme-border rounded-lg py-2 text-theme-text-primary focus:outline-none focus:border-skyhook-500"
            autoFocus
          />
          <button
            type="button"
            onClick={() => setBulkScaleReplicas(Math.min(10000, bulkScaleReplicas + 1))}
            disabled={bulkScaleReplicas >= 10000}
            className="p-2 rounded-lg bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-5 h-5" />
          </button>
        </div>
        <div className="text-xs text-theme-text-tertiary text-center">
          {commonBulkScaleReplicas === null ? 'Current replicas vary across the selected workloads.' : `Current: ${commonBulkScaleReplicas} replicas`}
        </div>
        <p className="text-xs text-theme-text-secondary text-center">
          All selected workloads will be set to the same replica count. Autoscalers may override it.
        </p>
      </div>
    </ConfirmDialog>
    </ResourcesViewDataContext.Provider>
  )
}

interface ResourceRowCellsProps {
  resource: any
  kind: string
  group?: string
  columns: Column[]
  extraColumnsByKey?: Map<string, ExtraColumn>
  hasSpacerColumn: boolean
  isSelected?: boolean
  isHighlighted?: boolean
  isChecked?: boolean
  showCheckbox?: boolean
  majorityNodeMinorVersion?: string
  // Row callbacks receive the resource so the parent can pass referentially
  // stable handlers — per-row closures would defeat React.memo and re-render
  // every visible row on each SSE-driven refetch.
  onRowClick?: (resource: any, isSelected: boolean) => void
  onRowMouseEnter?: () => void
  compareMode?: boolean
  /** -1 when not picked; 0 = pick A; 1 = pick B. */
  comparePickIndex?: number
  /** When provided, the name cell renders as `<a href>` and the other
   *  data cells drop their click handlers. The compare-mode chip column
   *  is unaffected (still toggles picks). */
  rowHref?: string
  onRowCheckToggle?: (resource: any) => void
}

function rowHighlightClass(
  compareMode: boolean | undefined,
  comparePickIndex: number,
  isSelected: boolean | undefined,
  isHighlighted: boolean | undefined,
  isChecked?: boolean,
): string {
  if (compareMode) {
    if (comparePickIndex === 0) return `${SIDE_TONES.a.rowBg} ${SIDE_TONES.a.rowBgHover}`
    if (comparePickIndex === 1) return `${SIDE_TONES.b.rowBg} ${SIDE_TONES.b.rowBgHover}`
    if (isHighlighted) return 'selection selection-ring'
    return 'group-hover/row:bg-theme-surface/50'
  }
  if (isSelected) return 'selection-strong group-hover/row:bg-skyhook-500/30'
  if (isChecked) return 'selection group-hover/row:bg-skyhook-500/20'
  if (isHighlighted) return 'selection selection-ring'
  return 'group-hover/row:bg-theme-surface/50'
}

const ResourceRowCells = React.memo(function ResourceRowCells({ resource, kind, group, columns, extraColumnsByKey, hasSpacerColumn, isSelected, isHighlighted, isChecked, showCheckbox, majorityNodeMinorVersion, onRowClick, onRowMouseEnter, compareMode, comparePickIndex = -1, rowHref, onRowCheckToggle }: ResourceRowCellsProps) {
  const rowHighlight = rowHighlightClass(compareMode, comparePickIndex, isSelected, isHighlighted, isChecked)
  const pickedSide = comparePickIndex === 0 ? 'a' : comparePickIndex === 1 ? 'b' : null
  const onClick = onRowClick ? () => onRowClick(resource, !!isSelected) : undefined
  const onMouseEnter = onRowMouseEnter
  // When the host supplies an anchor, drop per-cell onClick for the data
  // columns: the anchor is the only navigation surface. The compare-mode
  // chip column keeps its onClick so pick toggling still works.
  const cellsAreClickable = !rowHref
  return (
    <>
      {compareMode && (
        <td
          onClick={onClick}
          onMouseEnter={onMouseEnter}
          style={COMPARE_COLUMN_STYLE}
          className={clsx('px-2 py-3 border-b-subtle cursor-pointer text-center align-middle transition-colors', rowHighlight)}
        >
          {pickedSide ? (
            <span
              className={clsx(
                'inline-flex items-center justify-center w-5 h-5 rounded text-[10px] font-bold leading-none',
                SIDE_TONES[pickedSide].chipBg,
              )}
              aria-label={pickedSide === 'a' ? 'Pick A' : 'Pick B'}
            >
              {pickedSide === 'a' ? 'A' : 'B'}
            </span>
          ) : (
            <span
              className="inline-block w-4 h-4 rounded border border-theme-border-light group-hover/row:border-skyhook-400/60 transition-colors"
              aria-hidden
            />
          )}
        </td>
      )}
      {showCheckbox && (
        <td
          className={clsx('border-b-subtle text-center px-0 py-3 w-10 cursor-pointer transition-colors', rowHighlight)}
          onClick={(e) => { e.stopPropagation(); onRowCheckToggle?.(resource) }}
        >
          <input
            type="checkbox"
            checked={!!isChecked}
            onChange={() => {}}
            className="w-3.5 h-3.5 rounded border-theme-border accent-skyhook-500 cursor-pointer"
          />
        </td>
      )}
      {columns.map((col) => (
        <td
          key={col.key}
          onClick={cellsAreClickable ? onClick : undefined}
          onMouseEnter={onMouseEnter}
          className={clsx(
            'px-4 py-3 border-b-subtle transition-colors',
            cellsAreClickable && 'cursor-pointer',
            col.key !== 'status' && 'overflow-hidden truncate',
            rowHighlight,
          )}
        >
          <CellContent
            resource={resource}
            kind={kind}
            group={group}
            column={col.key}
            majorityNodeMinorVersion={majorityNodeMinorVersion}
            extraColumn={extraColumnsByKey?.get(col.key)}
            nameHref={col.key === 'name' ? rowHref : undefined}
          />
        </td>
      ))}
      {hasSpacerColumn && <td className="border-b-subtle p-0" />}
    </>
  )
})

const VirtuosoTableRow = React.forwardRef<HTMLTableRowElement, React.HTMLAttributes<HTMLTableRowElement>>(
  function VirtuosoTableRow(props, ref) {
    return <tr {...props} ref={ref} className="group/row" />
  }
)

function CopyNameButton({ name }: { name: string }) {
  const [copied, setCopied] = useState(false)
  if (!name) return null
  return (
    <button
      onClick={(e) => {
        e.stopPropagation()
        void copyText(name).then((didCopy) => {
          if (!didCopy) return
          setCopied(true)
          setTimeout(() => setCopied(false), 1500)
        })
      }}
      className="shrink-0 p-0.5 text-theme-text-tertiary hover:text-theme-text-primary opacity-0 group-hover/row:opacity-100 transition-opacity"
      title="Copy name"
    >
      {copied ? <Check className="w-3 h-3 text-green-400" /> : <Copy className="w-3 h-3" />}
    </button>
  )
}

interface CellContentProps {
  resource: any
  kind: string
  column: string
  group?: string
  majorityNodeMinorVersion?: string
  /** When provided, the parent has injected an ExtraColumn for this
   *  column key. Render via the extra's render() and short-circuit
   *  the built-in cell logic. */
  extraColumn?: ExtraColumn
  /** When set on the name column, the resource name renders as `<a href>`
   *  so ⌘-click / copy-link / hover-URL all work. */
  nameHref?: string
}

function CellContent({ resource, kind, column, group, majorityNodeMinorVersion, extraColumn, nameHref }: CellContentProps) {
  const { auditBadges } = useContext(ResourcesViewDataContext)
  // Parent-injected extra columns short-circuit the built-in switch.
  // Used by hosts that inject leading columns (e.g. a multi-cluster Cluster column).
  if (extraColumn) {
    return <>{extraColumn.render(resource)}</>
  }

  const meta = resource.metadata || {}

  // Common columns
  if (column === 'name') {
    const isTerminating = !!meta.deletionTimestamp
    const nameClass = clsx('text-sm font-medium truncate block', isTerminating ? 'text-theme-text-tertiary line-through' : 'text-theme-text-primary')
    const audit = auditBadges?.[`${meta.namespace || ''}/${meta.name}`]
    const auditTotal = audit ? audit.danger + audit.warning : 0
    return (
      <div className="flex items-center gap-1.5 min-w-0">
        <Tooltip content={meta.name}>
          {nameHref ? (
            <a
              href={nameHref}
              className={clsx(nameClass, 'hover:underline focus-visible:underline focus-visible:outline-none rounded-sm')}
            >
              {meta.name}
            </a>
          ) : (
            <span className={nameClass}>
              {meta.name}
            </span>
          )}
        </Tooltip>
        <CopyNameButton name={meta.name} />
        {auditTotal > 0 && audit && (
          <Tooltip content={audit.messages && audit.messages.length > 0
            ? <AuditBadgeTooltip messages={audit.messages} />
            : `${auditTotal} audit ${auditTotal === 1 ? 'finding' : 'findings'}${audit.danger > 0 ? ` · ${audit.danger} high` : ''}${audit.warning > 0 ? ` · ${audit.warning} medium` : ''}`}>
            <span className={clsx('shrink-0 inline-flex items-center gap-0.5 text-[10px] font-medium cursor-help', audit.danger > 0 ? SEVERITY_TEXT_CLASS.high : SEVERITY_TEXT_CLASS.medium)}>
              <AlertTriangle className="w-3 h-3" />
              {auditTotal}
            </span>
          </Tooltip>
        )}
        {isTerminating && (
          <Tooltip content="Resource is being deleted (has deletionTimestamp set). May be stuck due to finalizers.">
            <span className="shrink-0 flex items-center gap-1 px-1.5 py-0.5 text-[10px] font-medium bg-red-500/15 text-red-600 dark:text-red-400 rounded">
              <Trash2 className="w-3 h-3" />
              Terminating
            </span>
          </Tooltip>
        )}
      </div>
    )
  }
  if (column === 'namespace') {
    return (
      <Tooltip content={meta.namespace}>
        <span className="text-sm text-theme-text-secondary truncate block">{meta.namespace || '-'}</span>
      </Tooltip>
    )
  }
  if (column === 'age') {
    if (!meta.creationTimestamp) {
      return <span className="text-sm text-theme-text-secondary">-</span>
    }
    return (
      <Tooltip content={new Date(meta.creationTimestamp).toLocaleString()}>
        <span className="text-sm text-theme-text-secondary">{formatAge(meta.creationTimestamp)}</span>
      </Tooltip>
    )
  }

  // Kind-specific columns (normalize CRD singular names like 'ScaledObject' → 'scaledobjects')
  const kindLower = normalizeKindToPlural(kind, group)
  if (group && !hasCuratedColumns(kind, group)) {
    return <GenericCell resource={resource} column={column} />
  }

  // Kyverno cells are group-gated ahead of the switch so a non-matching CR
  // falls through to the switch's default (GenericCell) instead of being
  // described in Kyverno's vocabulary. These plurals are generic enough that
  // another vendor could ship them, and `policyexceptions` is served by BOTH
  // Kyverno API families with different spec shapes. The stakes are higher
  // here than in the drawer: an absent `validationActions` legitimately means
  // Deny for a real Kyverno policy, so an ungated foreign CR would render a
  // red "Deny" badge purely because it lacks a field it never had.
  if (KYVERNO_MODERN_PLURALS.has(kindLower) && isModernKyvernoPolicy(resource)) {
    return <KyvernoModernPolicyCell resource={resource} column={column} />
  }
  if (kindLower === 'policyexceptions' && isAnyKyvernoPolicyException(resource)) {
    return <KyvernoPolicyExceptionCell resource={resource} column={column} />
  }
  if (
    (kindLower === 'cleanuppolicies' || kindLower === 'clustercleanuppolicies') &&
    typeof resource?.apiVersion === 'string' &&
    resource.apiVersion.startsWith('kyverno.io/')
  ) {
    return <KyvernoCleanupPolicyCell resource={resource} column={column} />
  }

  switch (kindLower) {
    case 'pods':
      return <PodCell resource={resource} column={column} />
    case 'deployments':
    case 'statefulsets':
      return <WorkloadCell resource={resource} kind={kind} column={column} />
    case 'daemonsets':
      return <DaemonSetCell resource={resource} column={column} />
    case 'replicasets':
      return <ReplicaSetCell resource={resource} column={column} />
    case 'services':
      return <ServiceCell resource={resource} column={column} />
    case 'endpointslices':
      return <EndpointSliceCell resource={resource} column={column} />
    case 'ingresses':
      return <IngressCell resource={resource} column={column} />
    case 'configmaps':
      return <ConfigMapCell resource={resource} column={column} />
    case 'secrets':
      return <SecretCell resource={resource} column={column} />
    case 'jobs':
      return <JobCell resource={resource} column={column} />
    case 'cronjobs':
      return <CronJobCell resource={resource} column={column} />
    case 'hpas':
    case 'horizontalpodautoscalers':
      return <HPACell resource={resource} column={column} />
    case 'nodes':
      return <NodeCell resource={resource} column={column} majorityNodeMinorVersion={majorityNodeMinorVersion} />
    case 'persistentvolumeclaims':
      return <PVCCell resource={resource} column={column} />
    case 'rollouts':
      if (!resource.apiVersion?.startsWith('argoproj.io/')) {
        return <GenericCell resource={resource} column={column} />
      }
      return <RolloutCell resource={resource} column={column} />
    case 'analysisruns':
      return <AnalysisRunCell resource={resource} column={column} />
    case 'workflows':
      return <WorkflowCell resource={resource} column={column} />
    case 'cronworkflows':
      return <CronWorkflowCell resource={resource} column={column} />
    case 'certificates':
      return <CertificateCell resource={resource} column={column} />
    case 'persistentvolumes':
      return <PersistentVolumeCell resource={resource} column={column} />
    case 'storageclasses':
      return <StorageClassCell resource={resource} column={column} />
    case 'certificaterequests':
      return <CertificateRequestCell resource={resource} column={column} />
    case 'clusterissuers':
      return <ClusterIssuerCell resource={resource} column={column} />
    case 'issuers':
      return <IssuerCell resource={resource} column={column} />
    case 'orders':
      return <OrderCell resource={resource} column={column} />
    case 'challenges':
      return <ChallengeCell resource={resource} column={column} />
    case 'istiogateways':
      return <IstioGatewayCell resource={resource} column={column} />
    case 'gateways':
      // Disambiguate Gateway API vs Istio Gateway by apiVersion. Retained
      // alongside the case above because not every call site normalizes the
      // kind through GROUP_QUALIFIED_COLUMN_KEYS before dispatching.
      if (resource.apiVersion?.includes('networking.istio.io')) {
        return <IstioGatewayCell resource={resource} column={column} />
      }
      return <GatewayCell resource={resource} column={column} />
    case 'httproutes':
    case 'grpcroutes':
    case 'tcproutes':
    case 'tlsroutes':
      return <RouteCell resource={resource} column={column} />
    case 'gatewayclasses':
      return <GatewayClassCell resource={resource} column={column} />
    case 'sealedsecrets':
      return <SealedSecretCell resource={resource} column={column} />
    case 'workflowtemplates':
    case 'clusterworkflowtemplates':
      return <WorkflowTemplateCell resource={resource} column={column} />
    case 'networkpolicies':
      if (!isCoreNetworkPolicyKind(resource.kind ?? kind, resource.apiVersion, group)) {
        return <GenericCell resource={resource} column={column} />
      }
      return <NetworkPolicyCell resource={resource} column={column} />
    case 'calicoippools':
    case 'calicohostendpoints':
    case 'calicotiers':
      return <CalicoInfraCell resource={resource} column={column} />
    case 'caliconetworkpolicies':
    case 'calicoglobalnetworkpolicies':
    case 'calicostagednetworkpolicies':
    case 'calicostagedglobalnetworkpolicies':
    case 'calicostagedkubernetesnetworkpolicies':
      if (!isCalicoPolicyResource(resource)) return <GenericCell resource={resource} column={column} />
      return <CalicoPolicyCell resource={resource} column={column} />
    case 'poddisruptionbudgets':
      return <PDBCell resource={resource} column={column} />
    case 'serviceaccounts':
      return <ServiceAccountCell resource={resource} column={column} />
    case 'roles':
    case 'clusterroles':
      return <RoleCell resource={resource} column={column} />
    case 'rolebindings':
    case 'clusterrolebindings':
      return <RoleBindingCell resource={resource} column={column} />
    case 'ingressclasses':
      return <IngressClassCell resource={resource} column={column} />
    case 'leases':
      return <LeaseCell resource={resource} column={column} />
    case 'priorityclasses':
      return <PriorityClassCell resource={resource} column={column} />
    case 'runtimeclasses':
      return <RuntimeClassCell resource={resource} column={column} />
    case 'mutatingwebhookconfigurations':
    case 'validatingwebhookconfigurations':
      return <WebhookConfigCell resource={resource} column={column} />
    case 'events':
      return <EventCell resource={resource} column={column} />
    // FluxCD GitOps resources
    case 'gitrepositories':
      return <GitRepositoryCell resource={resource} column={column} />
    case 'ocirepositories':
      return <OCIRepositoryCell resource={resource} column={column} />
    case 'helmrepositories':
      return <HelmRepositoryCell resource={resource} column={column} />
    case 'kustomizations':
      return <KustomizationCell resource={resource} column={column} />
    case 'helmreleases':
      return <FluxHelmReleaseCell resource={resource} column={column} />
    case 'alerts':
      return <FluxAlertCell resource={resource} column={column} />
    // Karpenter
    case 'nodepools':
      return <NodePoolCell resource={resource} column={column} />
    case 'nodeclaims':
      return <NodeClaimCell resource={resource} column={column} />
    case 'ec2nodeclasses':
      return <EC2NodeClassCell resource={resource} column={column} />
    // Prometheus Operator
    case 'servicemonitors':
      return <ServiceMonitorCell resource={resource} column={column} />
    case 'prometheusrules':
      return <PrometheusRuleCell resource={resource} column={column} />
    case 'podmonitors':
      return <PodMonitorCell resource={resource} column={column} />
    // KEDA
    case 'scaledobjects':
      return <ScaledObjectCell resource={resource} column={column} />
    case 'scaledjobs':
      return <ScaledJobCell resource={resource} column={column} />
    case 'triggerauthentications':
      return <TriggerAuthenticationCell resource={resource} column={column} />
    case 'clustertriggerauthentications':
      return <ClusterTriggerAuthenticationCell resource={resource} column={column} />
    // DRA (resource.k8s.io)
    case 'resourceclaims':
      return <ResourceClaimCell resource={resource} column={column} />
    case 'resourceclaimtemplates':
      return <ResourceClaimTemplateCell resource={resource} column={column} />
    case 'deviceclasses':
      return <DeviceClassCell resource={resource} column={column} />
    case 'resourceslices':
      return <ResourceSliceCell resource={resource} column={column} />
    // NVIDIA GPU Operator (nvidiaclusterpolicies is the group-qualified key for nvidia.com clusterpolicies)
    case 'nvidiaclusterpolicies':
      return <NvidiaClusterPolicyCell resource={resource} column={column} />
    case 'nvidiadrivers':
      return <NvidiaDriverCell resource={resource} column={column} />
    // Kueue + Cluster Autoscaler
    case 'clusterqueues':
      return <ClusterQueueCell resource={resource} column={column} />
    case 'localqueues':
      return <LocalQueueCell resource={resource} column={column} />
    case 'workloads':
      return <KueueWorkloadCell resource={resource} column={column} />
    case 'resourceflavors':
      return <ResourceFlavorCell resource={resource} column={column} />
    case 'admissionchecks':
      return <AdmissionCheckCell resource={resource} column={column} />
    case 'provisioningrequests':
      return <ProvisioningRequestCell resource={resource} column={column} />
    // KubeRay
    case 'rayclusters':
      return <RayClusterCell resource={resource} column={column} />
    case 'rayjobs':
      return <RayJobCell resource={resource} column={column} />
    case 'rayservices':
      return <RayServiceCell resource={resource} column={column} />
    case 'raycronjobs':
      return <RayCronJobCell resource={resource} column={column} />
    // LeaderWorkerSet + JobSet
    case 'leaderworkersets':
      return <LeaderWorkerSetCell resource={resource} column={column} />
    case 'jobsets':
      return <JobSetCell resource={resource} column={column} />
    // KServe
    case 'inferenceservices':
      return <InferenceServiceCell resource={resource} column={column} />
    case 'servingruntimes':
    case 'clusterservingruntimes':
      return <ServingRuntimeCell resource={resource} column={column} />
    case 'inferencegraphs':
      return <InferenceGraphCell resource={resource} column={column} />
    case 'trainedmodels':
      return <TrainedModelCell resource={resource} column={column} />
    case 'llminferenceservices':
      return <LLMInferenceServiceCell resource={resource} column={column} />
    // Gateway API Inference Extension
    case 'inferencepools':
      return <InferencePoolCell resource={resource} column={column} />
    case 'inferenceobjectives':
      return <InferenceObjectiveCell resource={resource} column={column} />
    // Volcano (group-qualified keys disambiguate from batch Jobs and KAI)
    case 'volcanojobs':
      return <VolcanoJobCell resource={resource} column={column} />
    case 'volcanoqueues':
      return <VolcanoQueueCell resource={resource} column={column} />
    case 'volcanopodgroups':
      return <VolcanoPodGroupCell resource={resource} column={column} />
    case 'jobflows':
      return <JobFlowCell resource={resource} column={column} />
    case 'jobtemplates':
      return <JobTemplateCell resource={resource} column={column} />
    // KAI Scheduler
    case 'kaiqueues':
      return <KaiQueueCell resource={resource} column={column} />
    case 'kaipodgroups':
      return <KaiPodGroupCell resource={resource} column={column} />
    // KAITO
    case 'kaitoworkspaces':
      return <KaitoWorkspaceCell resource={resource} column={column} />
    case 'ragengines':
      return <RAGEngineCell resource={resource} column={column} />
    // NVIDIA NIM Operator
    case 'nimservices':
      return <NIMServiceCell resource={resource} column={column} />
    case 'nimcaches':
      return <NIMCacheCell resource={resource} column={column} />
    case 'nimpipelines':
      return <NIMPipelineCell resource={resource} column={column} />
    // AMD GPU Operator
    case 'deviceconfigs':
      return <AMDDeviceConfigCell resource={resource} column={column} />
    // Kubeflow training
    case 'pytorchjobs':
      return <PyTorchJobCell resource={resource} column={column} />
    case 'tfjobs':
      return <TFJobCell resource={resource} column={column} />
    case 'mpijobs':
      return <MPIJobCell resource={resource} column={column} />
    case 'trainjobs':
      return <TrainJobCell resource={resource} column={column} />
    // ArgoCD GitOps resources
    case 'applications':
      return <ArgoApplicationCell resource={resource} column={column} />
    case 'applicationsets':
      return <ArgoApplicationSetCell resource={resource} column={column} />
    case 'appprojects':
      return <ArgoAppProjectCell resource={resource} column={column} />
    // Kyverno / Policy Reports
    case 'policyreports':
      return <PolicyReportCell resource={resource} column={column} />
    case 'clusterpolicyreports':
      return <ClusterPolicyReportCell resource={resource} column={column} />
    case 'kyvernopolicies':
      return <KyvernoPolicyCell resource={resource} column={column} />
    case 'clusterpolicies':
      return <ClusterPolicyCell resource={resource} column={column} />
    // Trivy Operator
    case 'vulnerabilityreports':
      return <VulnerabilityReportCell resource={resource} column={column} />
    case 'configauditreports':
      return <ConfigAuditReportCell resource={resource} column={column} />
    case 'exposedsecretreports':
      return <ExposedSecretReportCell resource={resource} column={column} />
    case 'rbacassessmentreports':
    case 'clusterrbacassessmentreports':
    case 'infraassessmentreports':
    case 'clusterinfraassessmentreports':
      return <RbacAssessmentReportCell resource={resource} column={column} />
    case 'clustercompliancereports':
      return <ClusterComplianceReportCell resource={resource} column={column} />
    case 'sbomreports':
    case 'clustersbomreports':
      return <SbomReportCell resource={resource} column={column} />
    // External Secrets Operator
    case 'externalsecrets':
      return <ExternalSecretCell resource={resource} column={column} />
    case 'clusterexternalsecrets':
      return <ClusterExternalSecretCell resource={resource} column={column} />
    case 'secretstores':
      return <SecretStoreCell resource={resource} column={column} />
    case 'clustersecretstores':
      return <ClusterSecretStoreCell resource={resource} column={column} />
    // Velero
    // The cnpg* keys come from GROUP_QUALIFIED_COLUMN_KEYS; the unqualified
    // plurals below are reached when the caller had no group to resolve with,
    // and stay group-guarded so a foreign CRD is not read as CNPG.
    case 'updaterequests':
      return <KyvernoUpdateRequestCell resource={resource} column={column} />
    case 'ephemeralreports':
    case 'clusterephemeralreports':
      return <KyvernoEphemeralReportCell resource={resource} column={column} />
    case 'cnpgdatabases':
    case 'cnpgpublications':
    case 'cnpgsubscriptions':
      return <CNPGDeclarativeCell resource={resource} column={column} />
    case 'databases':
    case 'publications':
      if (isApiGroup(resource.apiVersion, CNPG_GROUP)) {
        return <CNPGDeclarativeCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    case 'cnpgimagecatalogs':
    case 'cnpgclusterimagecatalogs':
      return <CNPGImageCatalogCell resource={resource} column={column} />
    case 'imagecatalogs':
    case 'clusterimagecatalogs':
      if (isApiGroup(resource.apiVersion, CNPG_GROUP)) {
        return <CNPGImageCatalogCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    case 'barmanobjectstores':
      return <CNPGObjectStoreCell resource={resource} column={column} />
    case 'objectstores':
      // Group-guarded: another operator could serve an `objectstores` plural,
      // and it would have no recovery window to report.
      if (isApiGroup(resource.apiVersion, 'barmancloud.cnpg.io')) {
        return <CNPGObjectStoreCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    case 'cnpgbackups':
      return <CNPGBackupCell resource={resource} column={column} />
    case 'backups':
      // Reached only when the group is unknown to normalizeKindToPlural. Both
      // engines are matched positively so a third `backups` CRD renders generic
      // rather than inheriting whichever branch happened to be the fallback.
      if (isApiGroup(resource.apiVersion, CNPG_GROUP)) {
        return <CNPGBackupCell resource={resource} column={column} />
      }
      if (isApiGroup(resource.apiVersion, 'velero.io')) {
        return <BackupCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    case 'velerorestores':
      return <RestoreCell resource={resource} column={column} />
    case 'veleroschedules':
      return <ScheduleCell resource={resource} column={column} />
    case 'backupstoragelocations':
      if (!isVeleroResource(resource)) return <GenericCell resource={resource} column={column} />
      return <BackupStorageLocationCell resource={resource} column={column} />
    case 'volumesnapshotlocations':
      if (!isVeleroResource(resource)) return <GenericCell resource={resource} column={column} />
      return <VolumeSnapshotLocationCell resource={resource} column={column} />
    case 'backuprepositories':
      if (!isVeleroResource(resource)) return <GenericCell resource={resource} column={column} />
      return <BackupRepositoryCell resource={resource} column={column} />
    // CloudNativePG
    case 'cnpgclusters':
      return <CNPGClusterCell resource={resource} column={column} />
    case 'clusters':
      // Positive guard: `clusters` is one of the most collided CRD plurals
      // (CNPG, CAPI, KubeBlocks, Redis/Valkey operators). Anything else gets
      // the generic cell instead of a fabricated Postgres status.
      if (isApiGroup(resource.apiVersion, CNPG_GROUP)) {
        return <CNPGClusterCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    case 'scheduledbackups':
      if (isApiGroup(resource.apiVersion, CNPG_GROUP)) {
        return <CNPGScheduledBackupCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    case 'poolers':
      if (isApiGroup(resource.apiVersion, CNPG_GROUP)) {
        return <CNPGPoolerCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    // Istio Service Mesh
    case 'virtualservices':
      return <VirtualServiceCell resource={resource} column={column} />
    case 'destinationrules':
      return <DestinationRuleCell resource={resource} column={column} />
    case 'serviceentries':
      return <ServiceEntryCell resource={resource} column={column} />
    case 'peerauthentications':
      return <PeerAuthenticationCell resource={resource} column={column} />
    case 'authorizationpolicies':
      return <AuthorizationPolicyCell resource={resource} column={column} />
    // Knative Serving
    case 'knativeservices':
      return <KnativeServiceCell resource={resource} column={column} />
    case 'knativeconfigurations':
      return <KnativeConfigurationCell resource={resource} column={column} />
    case 'knativerevisions':
      return <KnativeRevisionCell resource={resource} column={column} />
    case 'knativeroutes':
      return <KnativeRouteCell resource={resource} column={column} />
    // Knative Eventing
    case 'brokers':
      return <BrokerCell resource={resource} column={column} />
    case 'triggers':
      return <TriggerCell resource={resource} column={column} />
    case 'eventtypes':
      return <EventTypeCell resource={resource} column={column} />
    // Knative Sources
    case 'pingsources':
      return <PingSourceCell resource={resource} column={column} />
    case 'apiserversources':
      return <ApiServerSourceCell resource={resource} column={column} />
    case 'containersources':
      return <ContainerSourceCell resource={resource} column={column} />
    case 'sinkbindings':
      return <SinkBindingCell resource={resource} column={column} />
    // Knative Messaging
    case 'channels':
      return <ChannelCell resource={resource} column={column} />
    case 'inmemorychannels':
      return <InMemoryChannelCell resource={resource} column={column} />
    case 'knativesubscriptions':
      return <SubscriptionCell resource={resource} column={column} />
    case 'subscriptions':
      // Shared plural, and not only by two: CNPG declares one, Knative declares
      // one, and a third operator's would be read as Knative's — Channel and
      // Subscriber blank on every row — unless both are matched positively.
      if (isApiGroup(resource.apiVersion, CNPG_GROUP)) {
        return <CNPGDeclarativeCell resource={resource} column={column} />
      }
      if (isApiGroup(resource.apiVersion, 'messaging.knative.dev')) {
        return <SubscriptionCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    // Knative Flows
    case 'sequences':
      return <SequenceCell resource={resource} column={column} />
    case 'parallels':
      return <ParallelCell resource={resource} column={column} />
    // Knative Networking & Serving
    case 'serverlessservices':
      return <ServerlessServiceCell resource={resource} column={column} />
    case 'domainmappings':
      return <DomainMappingCell resource={resource} column={column} />
    case 'knativeingresses':
      return <KnativeIngressCell resource={resource} column={column} />
    case 'knativecertificates':
      return <KnativeCertificateCell resource={resource} column={column} />
    // Traefik
    case 'ingressroutes':
    case 'ingressroutetcps':
    case 'ingressrouteudps':
      return <IngressRouteCell resource={resource} column={column} />
    case 'middlewares':
    case 'middlewaretcps':
      return <MiddlewareCell resource={resource} column={column} />
    case 'traefikservices':
      return <TraefikServiceCell resource={resource} column={column} />
    case 'serverstransports':
    case 'serverstransporttcps':
      return <ServersTransportCell resource={resource} column={column} />
    case 'tlsoptions':
      return <TLSOptionCell resource={resource} column={column} />
    // Contour
    case 'httpproxies':
      return <HTTPProxyCell resource={resource} column={column} />
    // Cluster API (CAPI)
    case 'capiclusters':
      return <CAPIClusterCell resource={resource} column={column} />
    case 'machines':
      return <CAPIMachineCell resource={resource} column={column} />
    case 'machinedeployments':
      return <CAPIMachineDeploymentCell resource={resource} column={column} />
    case 'machinesets':
      return <CAPIMachineSetCell resource={resource} column={column} />
    case 'machinepools':
      return <CAPIMachinePoolCell resource={resource} column={column} />
    case 'kubeadmcontrolplanes':
      return <CAPIKubeadmControlPlaneCell resource={resource} column={column} />
    case 'clusterclasses':
      return <CAPIClusterClassCell resource={resource} column={column} />
    case 'machinehealthchecks':
      return <CAPIMachineHealthCheckCell resource={resource} column={column} />
    case 'machinedrainrules':
    case 'kubeadmconfigs':
    case 'kubeadmconfigtemplates':
    case 'kubeadmcontrolplanetemplates':
      // These use default columns — no custom cell renderer needed
      return <GenericCell resource={resource} column={column} />
    // AWS CAPI Infrastructure Provider
    case 'awsmanagedcontrolplanes':
      return <AWSManagedControlPlaneCell resource={resource} column={column} />
    case 'awsmanagedmachinepools':
      return <AWSManagedMachinePoolCell resource={resource} column={column} />
    case 'awsmachines':
      return <AWSMachineCell resource={resource} column={column} />
    case 'awsmachinetemplates':
      return <AWSMachineTemplateCell resource={resource} column={column} />
    case 'awsmanagedclusters':
      return <AWSManagedClusterCell resource={resource} column={column} />
    // GCP CAPI Infrastructure
    case 'gcpmanagedcontrolplanes':
      return <GCPManagedControlPlaneCell resource={resource} column={column} />
    case 'gcpmanagedmachinepools':
      return <GCPManagedMachinePoolCell resource={resource} column={column} />
    case 'gcpmachines':
      return <GCPMachineCell resource={resource} column={column} />
    case 'gcpmachinetemplates':
      return <GCPMachineTemplateCell resource={resource} column={column} />
    case 'gcpmanagedclusters':
      return <GCPManagedClusterCell resource={resource} column={column} />
    // Azure CAPI Infrastructure
    case 'azuremanagedcontrolplanes':
      return <AzureManagedControlPlaneCell resource={resource} column={column} />
    case 'azuremanagedmachinepools':
      return <AzureManagedMachinePoolCell resource={resource} column={column} />
    case 'azuremachines':
      return <AzureMachineCell resource={resource} column={column} />
    case 'azuremachinetemplates':
      return <AzureMachineTemplateCell resource={resource} column={column} />
    case 'azuremanagedclusters':
      return <AzureManagedClusterCell resource={resource} column={column} />
    // Crossplane
    case 'providers':
      // Disambiguate from any future kind named "providers" by checking group
      if (resource.apiVersion?.startsWith('pkg.crossplane.io/')) {
        return <CrossplaneProviderCell resource={resource} column={column} />
      }
      return <GenericCell resource={resource} column={column} />
    case 'providerconfigs':
      return <CrossplaneProviderConfigCell resource={resource} column={column} />
    case 'compositions':
      return <CompositionCell resource={resource} column={column} />
    case 'compositeresourcedefinitions':
      return <XRDCell resource={resource} column={column} />
    default:
      // Crossplane Managed Resources: unbounded plurals (one CRD per provider
      // service), detected via group prefix + spec.providerConfigRef heuristic.
      if (isLikelyCrossplaneMRGroup(kind, group ?? '') && isManagedResource(resource)) {
        return <ManagedResourceCell resource={resource} column={column} />
      }
      // Composite Resources (XRs) — user-defined kinds with resourceRefs.
      if (isComposite(resource)) {
        return <CompositeResourceCell resource={resource} column={column} />
      }
      // Generic cell for CRDs and unknown resources
      return <GenericCell resource={resource} column={column} />
  }
}

// Generic cell renderer for CRDs and unknown resources
function GenericCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const derived = getGenericResourceStatus(resource)
      if (!derived) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <Tooltip content={derived.reason ?? derived.text}>
          <span className={clsx('badge', healthColors[derived.tone])}>{derived.text}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

// ============================================================================
// KIND-SPECIFIC CELL RENDERERS
// ============================================================================

function PodCell({ resource, column }: { resource: any; column: string }) {
  const metrics = useContext(MetricsContext)
  const { onNavigate: navigate } = useContext(ResourcesViewDataContext)

  switch (column) {
    case 'containers': {
      const squares = getContainerSquareStates(resource)
      const hasInit = squares.some(s => s.isInit)
      return (
        <div className="flex w-full items-center justify-center gap-1">
          {squares.map((sq, i) => {
            const showSeparator = hasInit && i > 0 && sq.isInit !== squares[i - 1].isInit
            const bgClass =
              sq.status === 'ready' ? 'bg-green-500' :
              sq.status === 'completed' ? 'bg-theme-text-tertiary/30 border border-theme-text-tertiary' :
              sq.status === 'running' ? 'bg-yellow-500' :
              sq.status === 'waiting' ? 'bg-red-500' :
              sq.status === 'terminated' ? 'bg-red-500' :
              'bg-theme-text-tertiary/30 border border-dashed border-theme-text-tertiary'
            const ringClass = sq.restarts > 0 ? 'ring-2 ring-orange-400' : ''
            const dotColor =
              sq.status === 'ready' ? 'bg-green-500' :
              sq.status === 'completed' ? 'bg-theme-text-tertiary' :
              sq.status === 'running' ? 'bg-yellow-500' :
              sq.status === 'waiting' || sq.status === 'terminated' ? 'bg-red-500' :
              'bg-theme-text-tertiary'
            // Relative time helper
            const timeAgo = (dateStr?: string) => {
              if (!dateStr) return null
              const ms = Date.now() - new Date(dateStr).getTime()
              if (isNaN(ms) || ms < 0) return null
              if (ms < 60000) return 'just now'
              return formatDuration(ms) + ' ago'
            }
            const runDuration = (start?: string, end?: string) => {
              if (!start || !end) return null
              const ms = new Date(end).getTime() - new Date(start).getTime()
              return ms > 0 ? formatDuration(ms, true) : null
            }
            const stateLabel =
              sq.status === 'ready' ? 'Running' :
              sq.status === 'completed' ? 'Completed' :
              sq.status === 'running' ? 'Running (not ready)' :
              sq.status === 'waiting' ? 'Waiting' :
              sq.status === 'terminated' ? 'Terminated' : 'Unknown'
            const uptime = sq.status === 'ready' || sq.status === 'running' ? timeAgo(sq.startedAt) : null
            const duration = (sq.status === 'completed' || sq.status === 'terminated') ? runDuration(sq.startedAt, sq.finishedAt) : null
            const lt = sq.lastTermination
            const restartRecencyMs = lt?.finishedAt ? Date.now() - new Date(lt.finishedAt).getTime() : null
            const restartColor = restartRecencyMs !== null && restartRecencyMs < 600000 ? 'text-red-400' : restartRecencyMs !== null && restartRecencyMs < 3600000 ? 'text-yellow-400' : 'text-orange-400'
            const tooltipContent = (
              <div className="whitespace-normal space-y-1">
                {/* Header: dot + name + state */}
                <div className="flex items-center gap-1.5">
                  <div className={clsx('w-2 h-2 rounded-full shrink-0', dotColor)} />
                  <span className="font-medium">{sq.isInit ? <span className="text-theme-text-tertiary font-normal">init · </span> : ''}{sq.name}</span>
                  <span className="text-theme-text-tertiary">·</span>
                  <span className="text-theme-text-secondary">{stateLabel}</span>
                </div>
                {/* Reason (when different from state label) */}
                {sq.reason && sq.reason !== stateLabel && (
                  <div className={clsx(
                    'font-medium',
                    (sq.status === 'waiting' || sq.status === 'terminated') ? 'text-red-400' : 'text-theme-text-secondary'
                  )}>{sq.reason}</div>
                )}
                {/* Message — truncated for tooltip */}
                {sq.message && (
                  <div className="text-theme-text-secondary text-[11px] leading-tight">
                    {sq.message.length > 120 ? sq.message.slice(0, 120) + '...' : sq.message}
                  </div>
                )}
                {/* Exit code + uptime/duration on same line */}
                {(sq.exitCode !== undefined && sq.status !== 'ready' && sq.status !== 'running') || uptime || duration ? (
                  <div className="text-theme-text-tertiary flex items-center gap-1.5">
                    {sq.exitCode !== undefined && sq.status !== 'ready' && sq.status !== 'running' && (
                      <span className={sq.exitCode !== 0 ? 'text-red-400' : ''}>exit {sq.exitCode}</span>
                    )}
                    {uptime && <span>up {uptime.replace(' ago', '')}</span>}
                    {duration && <span>ran {duration}</span>}
                  </div>
                ) : null}
                {/* Restarts + last crash info */}
                {sq.restarts > 0 && (
                  <div className={clsx('border-t border-theme-border/50 pt-1 space-y-0.5', restartColor)}>
                    <div className="flex items-center gap-1.5">
                      <span>{pluralize(sq.restarts, 'restart')}</span>
                      {lt?.finishedAt && <span className="text-theme-text-tertiary">· last {timeAgo(lt.finishedAt)}</span>}
                    </div>
                    {lt?.reason && (
                      <div className="text-theme-text-tertiary">
                        {lt.reason}{lt.exitCode !== undefined && lt.exitCode !== 0 ? ` (exit ${lt.exitCode})` : ''}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
            return (
              <React.Fragment key={i}>
                {showSeparator && <div className="w-px h-3 bg-theme-text-tertiary/40 mx-0.5" />}
                <Tooltip content={tooltipContent} className="whitespace-normal max-w-xs">
                  <div className={clsx('w-2.5 h-2.5 rounded-sm', bgClass, ringClass)} />
                </Tooltip>
              </React.Fragment>
            )
          })}
        </div>
      )
    }
    case 'status': {
      const status = getPodStatus(resource)
      const problems = getPodProblems(resource)
      return (
        <div className="flex items-center gap-2 min-w-0">
          <span className={clsx('badge truncate', status.color)} title={status.text}>
            {status.text}
          </span>
          {problems.length > 1 && (
            <Tooltip content={
              <div className="space-y-0.5">
                {problems.map((p, i) => (
                  <div key={i} className="flex items-center gap-1.5">
                    <span className={clsx('w-1.5 h-1.5 rounded-full shrink-0', SEVERITY_DOT_COLOR[p.severity])} />
                    <span>{p.message}</span>
                  </div>
                ))}
              </div>
            }>
              <span className="text-red-400">
                <AlertTriangle className="w-3.5 h-3.5" />
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'restarts': {
      const restarts = getPodRestarts(resource)
      return (
        <span className={clsx(
          'text-sm',
          restarts > 5 ? 'text-red-400 font-medium' : restarts > 0 ? 'text-yellow-400' : 'text-theme-text-secondary'
        )}>
          {restarts}
        </span>
      )
    }
    case 'node': {
      const nodeVal = resource.spec?.nodeName || '-'
      if (nodeVal === '-') {
        return <span className="text-sm text-theme-text-tertiary">-</span>
      }
      return (
        <Tooltip content={`Filter pods on ${nodeVal}`}>
          <button
            onClick={(e) => {
              e.stopPropagation()
              // Merge node filter into existing column filters via URL,
              // preserving any exclude operators already set on other columns.
              const params = new URLSearchParams(window.location.search)
              const existing = parseColumnFilters(params.get('filters'))
              const existingExcludes = parseColumnFilterExcludes(params.get('filters'))
              existing['node'] = [nodeVal]
              // Clicking a node means "show pods on this node", so force the
              // node column back to include mode even if it was previously
              // set to exclude — otherwise the shortcut would hide those pods.
              delete existingExcludes['node']
              params.set('filters', serializeColumnFilters(existing, existingExcludes))
              navigate?.(`/resources/pods?${params.toString()}`)
            }}
            className="text-sm text-blue-400 hover:text-blue-300 hover:underline truncate block text-left"
          >
            {nodeVal}
          </button>
        </Tooltip>
      )
    }
    case 'podIP': {
      const ip = resource.status?.podIP || '-'
      return <span className="text-sm text-theme-text-secondary font-mono">{ip}</span>
    }
    case 'cpu':
    case 'memory': {
      const kind = column as 'cpu' | 'memory'
      const isCPU = kind === 'cpu'
      const key = `${resource.metadata?.namespace}/${resource.metadata?.name}`
      const m = metrics.pods.get(key)
      if (!m) return <span className="text-sm text-theme-text-tertiary">-</span>
      const label = isCPU ? 'CPU' : 'Memory'
      const format = isCPU ? formatCPU : formatMemoryShort

      // Multi-container pods carry a per-container breakdown; single-container
      // pods fall back to the (union-summed) pod-level fields as one synthetic
      // container so the rest of the logic is uniform.
      const list: ContainerResourceMetrics[] = (m.containers && m.containers.length > 0)
        ? m.containers
        : [{
            name: resource.spec?.containers?.[0]?.name ?? resource.metadata?.name ?? '',
            cpu: m.cpu, cpuRequest: m.cpuRequest, cpuLimit: m.cpuLimit,
            memory: m.memory, memoryRequest: m.memoryRequest, memoryLimit: m.memoryLimit,
          }]

      const { mode, totalUsage, denom, markerPct, unlimitedCount } = podAggregate(list, kind)
      if (totalUsage === 0) return <span className="text-sm text-theme-text-tertiary">-</span>

      // Pod-vs-node context: how full the pod's node is (the risk signal) and
      // how much of it this pod takes. Only when node metrics are loaded.
      const nodeName = resource.spec?.nodeName as string | undefined
      const node = nodeName ? metrics.nodes.get(nodeName) : undefined
      const nodeAlloc = node ? (isCPU ? node.cpuAllocatable : node.memoryAllocatable) : 0
      const nodeUsage = node ? (isCPU ? node.cpu : node.memory) : 0
      // Zero usage means metrics-server has no sample for the node (down, or not
      // yet scraped) — the same "no data" NodeCell renders blank — so skip the
      // line rather than assert a misleading "0% used" that reads as safe.
      const nodeCtx = node && nodeAlloc > 0 && nodeUsage > 0
        ? {
            name: nodeName!,
            usedPct: (nodeUsage / nodeAlloc) * 100,
            podSharePct: (totalUsage / nodeAlloc) * 100,
          }
        : undefined

      const tip = buildContainerResourceTooltip(label, list, kind, format, nodeCtx)

      // Every container is limited → aggregate bar against the summed limit; a
      // real pod-level ceiling, so it may go red.
      if (mode === 'limit') {
        const pct = denom > 0 ? (totalUsage / denom) * 100 : 0
        return (
          <ResourceBar
            used={format(totalUsage)}
            total={format(denom)}
            percent={pct}
            colorScheme={getBulletBarScheme(pct, markerPct)}
            markerPercent={markerPct}
            tooltip={tip}
          />
        )
      }

      // No limits but some requests → neutral bar against the summed request
      // (no ceiling → never red), no marker.
      if (mode === 'request') {
        const pct = denom > 0 ? (totalUsage / denom) * 100 : 0
        return (
          <ResourceBar
            used={format(totalUsage)}
            total={format(denom)}
            percent={pct}
            colorScheme="quiet"
            tooltip={tip}
          />
        )
      }

      // partial (some limited, some not — the sum is not a real ceiling) or none
      // (nothing set) → plain usage number plus a faint tag.
      const tag = mode === 'partial' ? `${unlimitedCount} unbounded` : 'no limit'
      return (
        <Tooltip content={tip} delay={200} position="top" wrapperClassName="w-full min-w-0">
          <span className="inline-flex items-center gap-1.5 min-w-0">
            <span className="text-sm text-theme-text-secondary font-mono">{format(totalUsage)}</span>
            <span className="text-[10px] text-theme-text-tertiary shrink-0">{tag}</span>
          </span>
        </Tooltip>
      )
    }
    case 'gpu': {
      const count = getPodGpuCount(resource)
      if (!count) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary font-mono">{count}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WorkloadStatusCell({ resource, kind }: { resource: any; kind: string }) {
  const { activity, status } = getWorkloadDisplayStatus(resource, kind)
  const rolloutVisible = isRolloutActivityVisible(activity)
  const label = rolloutVisible ? status.text : workloadStatusLabel(status)
  const detail = rolloutVisible && activity.detail ? `${activity.detail} · ${status.text}` : status.text
  return <Tooltip content={detail}><span className={clsx('badge', status.color)}>{label}</span></Tooltip>
}

function WorkloadCell({ resource, column, kind }: { resource: any; kind: string; column: string }) {
  const status = resource.status || {}
  const spec = resource.spec || {}

  switch (column) {
    case 'status':
      return <WorkloadStatusCell resource={resource} kind={kind} />
    case 'ready': {
      const desired = spec.replicas ?? 1
      const ready = status.readyReplicas || 0
      const allReady = ready >= desired && desired > 0
      const problems = getWorkloadProblems(resource, kind)
      return (
        <div className="flex items-center gap-1.5">
          <span className={clsx(
            'text-sm font-medium',
            desired === 0 ? 'text-theme-text-secondary' : allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
          )}>
            {Math.min(ready, desired)}/{desired}
          </span>
          {problems.length > 0 && (
            <Tooltip content={
              <div className="space-y-0.5">
                {problems.map((p, i) => (
                  <div key={i} className="flex items-center gap-1.5">
                    <span className={clsx('w-1.5 h-1.5 rounded-full shrink-0', SEVERITY_DOT_COLOR[p.severity])} />
                    <span>{p.message}</span>
                  </div>
                ))}
              </div>
            }>
              <span className="text-red-400">
                <AlertTriangle className="w-3.5 h-3.5" />
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'upToDate':
      return <span className="text-sm text-theme-text-secondary">{status.updatedReplicas || 0}</span>
    case 'available':
      return <span className="text-sm text-theme-text-secondary">{status.availableReplicas || 0}</span>
    case 'images': {
      const images = getWorkloadImages(resource)
      if (images.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = images.length === 1 ? truncate(images[0], 40) : `${truncate(images[0], 30)} +${images.length - 1}`
      return (
        <Tooltip content={images.join('\n')}>
          <span className="text-sm text-theme-text-secondary truncate">
            {display}
          </span>
        </Tooltip>
      )
    }
    case 'conditions': {
      const { conditions, hasIssues } = getWorkloadConditions(resource)
      if (conditions.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = conditions.join(', ')
      return (
        <Tooltip content={display}>
          <span
            className={clsx(
              'text-sm truncate block',
              hasIssues ? 'text-yellow-400' : 'text-green-400'
            )}
          >
            {display}
          </span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function DaemonSetCell({ resource, column }: { resource: any; column: string }) {
  const status = resource.status || {}

  switch (column) {
    case 'status':
      return <WorkloadStatusCell resource={resource} kind="daemonsets" />
    case 'desired':
      return <span className="text-sm text-theme-text-secondary">{status.desiredNumberScheduled || 0}</span>
    case 'ready': {
      const desired = status.desiredNumberScheduled || 0
      const ready = status.numberReady || 0
      const allReady = ready >= desired && desired > 0
      const problems = getWorkloadProblems(resource, 'daemonsets')
      return (
        <div className="flex items-center gap-1.5">
          <span className={clsx(
            'text-sm font-medium',
            allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
          )}>
            {Math.min(ready, desired)}
          </span>
          {problems.length > 0 && (
            <Tooltip content={
              <div className="space-y-0.5">
                {problems.map((p, i) => (
                  <div key={i} className="flex items-center gap-1.5">
                    <span className={clsx('w-1.5 h-1.5 rounded-full shrink-0', SEVERITY_DOT_COLOR[p.severity])} />
                    <span>{p.message}</span>
                  </div>
                ))}
              </div>
            }>
              <span className="text-red-400">
                <AlertTriangle className="w-3.5 h-3.5" />
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'upToDate':
      return <span className="text-sm text-theme-text-secondary">{status.updatedNumberScheduled || 0}</span>
    case 'available':
      return <span className="text-sm text-theme-text-secondary">{status.numberAvailable || 0}</span>
    case 'images': {
      const images = getWorkloadImages(resource)
      if (images.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = images.length === 1 ? truncate(images[0], 40) : `${truncate(images[0], 30)} +${images.length - 1}`
      return (
        <Tooltip content={images.join('\n')}>
          <span className="text-sm text-theme-text-secondary truncate">
            {display}
          </span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ReplicaSetCell({ resource, column }: { resource: any; column: string }) {
  const status = resource.status || {}
  const spec = resource.spec || {}

  switch (column) {
    case 'ready': {
      const desired = spec.replicas ?? 1
      const ready = status.readyReplicas || 0
      const allReady = ready === desired && desired > 0
      return (
        <span className={clsx(
          'text-sm font-medium',
          desired === 0 ? 'text-theme-text-secondary' : allReady ? 'text-green-400' : ready > 0 ? 'text-yellow-400' : 'text-red-400'
        )}>
          {ready}/{desired}
        </span>
      )
    }
    case 'owner': {
      const owner = getReplicaSetOwner(resource)
      return <span className="text-sm text-theme-text-secondary truncate">{owner || '-'}</span>
    }
    case 'status': {
      const isActive = isReplicaSetActive(resource)
      return (
        <span className={clsx(
          'badge',
          isActive ? 'status-neutral' : 'status-unknown'
        )}>
          {isActive ? 'Active' : 'Old'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ServiceCell({ resource, column }: { resource: any; column: string }) {
  const { auditBadges } = useContext(ResourcesViewDataContext)
  switch (column) {
    case 'type': {
      const status = getServiceStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'selector': {
      const selector = getServiceSelector(resource)
      return (
        <Tooltip content={selector}>
          <span className="text-sm text-theme-text-secondary truncate">
            {selector}
          </span>
        </Tooltip>
      )
    }
    case 'endpoints': {
      // getServiceEndpointsStatus can't see live pods, so it optimistically
      // reports "Active" for any service with a selector. The audit's
      // serviceNoMatchingPods check DOES resolve the selector against live pods —
      // the only badge-worthy finding a Service can carry — so when it fired,
      // trust it over the guess instead of showing a false-green "Active".
      const meta = resource.metadata || {}
      const flagged = auditBadges?.[`${meta.namespace || ''}/${meta.name}`]
      if (flagged && flagged.danger + flagged.warning > 0) {
        return (
          <span className={clsx('badge', flagged.danger > 0 ? 'status-alert' : 'status-degraded')}>
            No endpoints
          </span>
        )
      }
      const { status, color } = getServiceEndpointsStatus(resource)
      return (
        <span className={clsx('badge', color)}>
          {status}
        </span>
      )
    }
    case 'clusterIP':
      return <span className="text-sm text-theme-text-secondary font-mono">{resource.spec?.clusterIP || '-'}</span>
    case 'externalIP': {
      const external = getServiceExternalIP(resource)
      if (!external) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <Tooltip content={external}>
          <div className="flex items-center gap-1">
            <Globe className="w-3.5 h-3.5 text-violet-400" />
            <span className="text-sm text-violet-400 truncate">{external}</span>
          </div>
        </Tooltip>
      )
    }
    case 'ports': {
      const ports = getServicePorts(resource)
      return <span className="text-sm text-theme-text-secondary">{ports}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function getEndpointSliceReadyCount(resource: any): number {
  return (resource.endpoints || []).filter((endpoint: any) => endpoint?.conditions?.ready !== false).length
}

function getEndpointSliceAddressCount(resource: any): number {
  return (resource.endpoints || []).reduce((total: number, endpoint: any) => total + (endpoint.addresses?.length || 0), 0)
}

function getEndpointSlicePorts(resource: any): string {
  const ports = resource.ports || []
  if (ports.length === 0) return '-'
  return ports.map((port: any) => {
    const name = port.name || 'unnamed'
    const protocol = port.protocol || 'TCP'
    return `${name}:${port.port ?? '-'}${protocol !== 'TCP' ? `/${protocol}` : ''}`
  }).join(', ')
}

function EndpointSliceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'service': {
      const service = resource.metadata?.labels?.['kubernetes.io/service-name']
      return <span className="text-sm text-theme-text-secondary truncate">{service || '-'}</span>
    }
    case 'addressType':
      return <span className="text-sm text-theme-text-secondary">{resource.addressType || '-'}</span>
    case 'endpoints': {
      const total = resource.endpoints?.length || 0
      const ready = getEndpointSliceReadyCount(resource)
      const color = total === 0 ? SEVERITY_BADGE.neutral :
        ready === total ? SEVERITY_BADGE.success :
        ready > 0 ? SEVERITY_BADGE.warning :
        SEVERITY_BADGE.error
      return <span className={clsx('badge', color)}>{ready}/{total}</span>
    }
    case 'addresses':
      return <span className="text-sm text-theme-text-secondary">{getEndpointSliceAddressCount(resource)}</span>
    case 'ports': {
      const ports = getEndpointSlicePorts(resource)
      return (
        <Tooltip content={ports}>
          <span className="text-sm text-theme-text-secondary truncate">{ports}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function IngressCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'class': {
      const ingressClass = getIngressClass(resource)
      return <span className="text-sm text-theme-text-secondary">{ingressClass || '-'}</span>
    }
    case 'hosts': {
      const hosts = getIngressHosts(resource)
      return (
        <Tooltip content={hosts}>
          <span className="text-sm text-theme-text-secondary truncate">{hosts}</span>
        </Tooltip>
      )
    }
    case 'rules': {
      const rules = getIngressRules(resource)
      return (
        <Tooltip content={rules}>
          <span className="text-sm text-theme-text-secondary truncate">{rules}</span>
        </Tooltip>
      )
    }
    case 'tls': {
      const hasTLS = hasIngressTLS(resource)
      return hasTLS ? (
        <Tooltip content="TLS Enabled">
          <span>
            <Shield className="w-4 h-4 text-green-400" />
          </span>
        </Tooltip>
      ) : (
        <span className="text-sm text-theme-text-tertiary">-</span>
      )
    }
    case 'address': {
      const address = getIngressAddress(resource)
      return <span className="text-sm text-theme-text-secondary truncate">{address || 'Pending'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ConfigMapCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'keys': {
      const { count, preview } = getConfigMapKeys(resource)
      return (
        <div className="flex items-center gap-2">
          <span className="text-sm text-theme-text-secondary">{count}</span>
          {count > 0 && (
            <Tooltip content={preview}>
              <span className="text-xs text-theme-text-tertiary truncate">
                ({preview})
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'size': {
      const size = getConfigMapSize(resource)
      return <span className="text-sm text-theme-text-secondary">{size}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function SecretCell({ resource, column }: { resource: any; column: string }) {
  const { certExpiry, certExpiryError } = useContext(ResourcesViewDataContext)
  switch (column) {
    case 'type': {
      const { type, color } = getSecretType(resource)
      return (
        <span className={clsx('badge', color)}>
          {type}
        </span>
      )
    }
    case 'keys': {
      const count = getSecretKeyCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    case 'expires': {
      if (certExpiryError) {
        return <span className="text-sm text-theme-text-tertiary" title="Failed to load certificate expiry">!</span>
      }
      const meta = resource.metadata || {}
      const key = `${meta.namespace}/${meta.name}`
      const expiry = certExpiry?.[key]
      if (!expiry) {
        return <span className="text-sm text-theme-text-tertiary">-</span>
      }
      const color = expiry.expired || expiry.daysLeft < 7
        ? 'text-red-400'
        : expiry.daysLeft < 30
          ? 'text-yellow-400'
          : 'text-green-400'
      const text = expiry.expired
        ? `Expired ${Math.abs(expiry.daysLeft)}d ago`
        : `${expiry.daysLeft}d`
      return <span className={clsx('text-sm font-medium', color)}>{text}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function JobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getJobStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'completions': {
      const { succeeded, total } = getJobCompletions(resource)
      const allDone = succeeded === total
      return (
        <span className={clsx(
          'text-sm font-medium',
          allDone ? 'text-green-400' : succeeded > 0 ? 'text-yellow-400' : 'text-theme-text-secondary'
        )}>
          {succeeded}/{total}
        </span>
      )
    }
    case 'duration': {
      const duration = getJobDuration(resource)
      return <span className="text-sm text-theme-text-secondary">{duration || '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function CronJobCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'schedule': {
      const { cron, readable } = getCronJobSchedule(resource)
      return (
        <div className="flex flex-col">
          <span className="text-sm text-theme-text-secondary font-mono">{cron}</span>
          <span className="text-xs text-theme-text-tertiary">{readable}</span>
        </div>
      )
    }
    case 'status': {
      const status = getCronJobStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'lastRun': {
      const lastRun = getCronJobLastRun(resource)
      return <span className="text-sm text-theme-text-secondary">{lastRun || 'Never'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function HPACell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'target': {
      const target = getHPATarget(resource)
      return <span className="text-sm text-theme-text-secondary truncate">{target}</span>
    }
    case 'replicas': {
      const { current, min, max } = getHPAReplicas(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          <span className="text-theme-text-primary font-medium">{current}</span>
          <span className="text-theme-text-tertiary"> ({min}-{max})</span>
        </span>
      )
    }
    case 'metrics': {
      const { cpu, memory, custom } = getHPAMetrics(resource)
      const parts: string[] = []
      if (cpu !== undefined) parts.push(`CPU: ${cpu}%`)
      if (memory !== undefined) parts.push(`Mem: ${memory}%`)
      if (custom > 0) parts.push(`+${custom} custom`)
      return <span className="text-sm text-theme-text-secondary">{parts.join(', ') || '-'}</span>
    }
    case 'status': {
      const status = getHPAStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

// Format helpers for resource bars
function formatCPU(nanocores: number): string {
  const m = Math.round(nanocores / 1e6)
  return `${m}m`
}

function formatMemoryShort(bytes: number): string {
  const gib = bytes / (1024 * 1024 * 1024)
  if (gib >= 1) return `${gib.toFixed(1)}Gi`
  const mib = bytes / (1024 * 1024)
  return `${Math.round(mib)}Mi`
}

// Bullet graph color logic for pod resource bars:
// green when usage < request, yellow when above request, red when > 85% of limit
function getBulletBarScheme(_usagePct: number, _markerPct: number | undefined): 'utilization' {
  return 'utilization'
}

// Max per-container rows shown in the compact cell tooltip before collapsing
// the tail into a "+N more" line.
const CONTAINER_TOOLTIP_CAP = 3

// Per-container resource reading. The yardstick is the container's own limit
// when set, otherwise its request. A limit means an enforced ceiling (usage can
// be throttled / OOM-killed → the bar may go red); a request-only container has
// no ceiling, so usage above request is normal and the bar stays neutral.
type Yardstick = 'limit' | 'request' | 'none'

export function readContainer(c: ContainerResourceMetrics, kind: 'cpu' | 'memory') {
  const isCPU = kind === 'cpu'
  const usage = isCPU ? c.cpu : c.memory
  const limit = isCPU ? c.cpuLimit : c.memoryLimit
  const request = isCPU ? c.cpuRequest : c.memoryRequest
  let yardstick: Yardstick = 'none'
  let denom = 0
  if (limit > 0) {
    yardstick = 'limit'
    denom = limit
  } else if (request > 0) {
    yardstick = 'request'
    denom = request
  }
  const pct = denom > 0 ? (usage / denom) * 100 : -1
  return { usage, limit, request, yardstick, denom, pct }
}

interface PodAggregate {
  // limit: every container is limited → a real ceiling exists (bar vs summed
  // limit). partial: some containers limited, some not → the sum is not a true
  // ceiling, so plain usage + an "unbounded" tag. request: no limits but some
  // requests → neutral bar vs summed request. none: nothing set → plain usage.
  mode: 'limit' | 'partial' | 'request' | 'none'
  totalUsage: number
  /** summed limit (limit mode) or summed request (request mode); 0 otherwise. */
  denom: number
  /** request marker as % of summed limit; limit mode only. */
  markerPct?: number
  /** number of containers with no limit set. */
  unlimitedCount: number
}

// podAggregate reduces a pod's containers to the aggregate the compact cell
// headline renders — total usage measured against a pod-level yardstick. It
// keeps the dominant consumer visible instead of demoting it behind a small
// limited container, and stays honest about partial limits (which don't form a
// real ceiling).
export function podAggregate(list: ContainerResourceMetrics[], kind: 'cpu' | 'memory'): PodAggregate {
  const isCPU = kind === 'cpu'
  let totalUsage = 0
  let limitedCount = 0
  let requestedCount = 0
  let unlimitedCount = 0
  let summedLimit = 0
  let summedRequest = 0
  for (const c of list) {
    const usage = isCPU ? c.cpu : c.memory
    const limit = isCPU ? c.cpuLimit : c.memoryLimit
    const request = isCPU ? c.cpuRequest : c.memoryRequest
    totalUsage += usage
    if (limit > 0) {
      limitedCount++
      summedLimit += limit
    } else {
      unlimitedCount++
    }
    if (request > 0) {
      requestedCount++
      summedRequest += request
    }
  }
  const allLimited = list.length > 0 && limitedCount === list.length
  if (allLimited) {
    return {
      mode: 'limit',
      totalUsage,
      denom: summedLimit,
      markerPct: summedRequest > 0 ? (summedRequest / summedLimit) * 100 : undefined,
      unlimitedCount,
    }
  }
  if (limitedCount > 0) {
    return { mode: 'partial', totalUsage, denom: 0, unlimitedCount }
  }
  if (requestedCount > 0) {
    return { mode: 'request', totalUsage, denom: summedRequest, unlimitedCount }
  }
  return { mode: 'none', totalUsage, denom: 0, unlimitedCount }
}

// buildContainerResourceTooltip renders one row per container — usage against
// its OWN yardstick (limit if set, otherwise request) and percentage — sorted
// by pct descending, intermixing limited and request-only. Containers with
// neither render the words "no limit". Capped at CONTAINER_TOOLTIP_CAP rows,
// with a final "+N more" line pointing at the pod.
// NodeContext answers "is this pod at risk from its node?" — how full the node
// is (the risk signal) and how much of the node this pod takes (attribution).
interface NodeContext {
  name: string
  usedPct: number
  podSharePct: number
}

// A node this full puts every pod on it at risk (eviction / OOM / throttling),
// regardless of the pod's own limit headroom — so the line goes amber here.
const NODE_PRESSURE_PCT = 85

function formatSharePct(pct: number): string {
  if (pct > 0 && pct < 1) return '<1%'
  return `${Math.round(pct)}%`
}

function buildContainerResourceTooltip(
  label: 'CPU' | 'Memory',
  containers: ContainerResourceMetrics[],
  kind: 'cpu' | 'memory',
  formatFn: (n: number) => string,
  nodeCtx?: NodeContext,
) {
  const rows = [...containers].sort((a, b) => {
    const diff = readContainer(b, kind).pct - readContainer(a, kind).pct
    return diff !== 0 ? diff : a.name.localeCompare(b.name)
  })
  const shown = rows.slice(0, CONTAINER_TOOLTIP_CAP)
  const remaining = rows.length - shown.length

  return (
    <div className="whitespace-normal w-72 flex flex-col gap-2 py-0.5">
      <div className="text-[11px] text-theme-text-tertiary uppercase tracking-wide">{label} by container</div>
      <div className="flex flex-col gap-2">
        {shown.map((c) => {
          const r = readContainer(c, kind)
          // Container name on its own line, the numbers below it. Limited
          // containers also show the request (the tooltip is the detail
          // surface and shouldn't drop it); request-only/unset show their
          // single yardstick or "no limit".
          const detail = r.yardstick === 'limit'
            ? `${formatFn(r.usage)} · ${Math.round(r.pct)}% · ${formatFn(r.denom)} limit${r.request > 0 ? ` · req ${formatFn(r.request)}` : ''}`
            : r.yardstick === 'request'
              ? `${formatFn(r.usage)} · ${Math.round(r.pct)}% · ${formatFn(r.denom)} request`
              : `${formatFn(r.usage)} · no limit`
          return (
            <div key={c.name} className="flex flex-col leading-snug">
              <span className="text-[11px] text-theme-text-tertiary truncate">{c.name}</span>
              <span className="text-xs font-mono text-theme-text-primary">{detail}</span>
            </div>
          )
        })}
      </div>
      {remaining > 0 && (
        <div className="text-[11px] text-theme-text-tertiary border-t border-theme-border/50 pt-1">
          +{remaining} more → open pod
        </div>
      )}
      {nodeCtx && (
        <div className="flex flex-col leading-snug border-t border-theme-border/50 pt-1">
          <span className="text-[11px] text-theme-text-tertiary truncate">Node {nodeCtx.name}</span>
          <span
            className={clsx(
              'text-[11px]',
              nodeCtx.usedPct >= NODE_PRESSURE_PCT
                ? 'text-amber-500 font-medium'
                : 'text-theme-text-secondary',
            )}
          >
            {formatSharePct(nodeCtx.usedPct)} used · this pod {formatSharePct(nodeCtx.podSharePct)}
          </span>
        </div>
      )}
    </div>
  )
}

function NodeCell({ resource, column, majorityNodeMinorVersion }: { resource: any; column: string; majorityNodeMinorVersion?: string }) {
  const metrics = useContext(MetricsContext)

  switch (column) {
    case 'status': {
      const status = getNodeStatus(resource)
      const { problems } = getNodeConditions(resource)
      return (
        <div className="flex items-center gap-2">
          <span className={clsx('badge', status.color)}>
            {status.text}
          </span>
          {problems.length > 0 && (
            <Tooltip content={problems.join(', ')}>
              <span className="text-red-400">
                <AlertTriangle className="w-3.5 h-3.5" />
              </span>
            </Tooltip>
          )}
        </div>
      )
    }
    case 'roles': {
      const roles = getNodeRoles(resource)
      return <span className="text-sm text-theme-text-secondary">{roles}</span>
    }
    case 'conditions': {
      const { problems, healthy } = getNodeConditions(resource)
      if (healthy) {
        return <span className="text-sm text-green-400">Healthy</span>
      }
      return (
        <Tooltip content={problems.join(', ')}>
          <span className="text-sm text-yellow-400 truncate">
            {problems.join(', ')}
          </span>
        </Tooltip>
      )
    }
    case 'taints': {
      const { text, count } = getNodeTaints(resource)
      return (
        <span className={clsx('text-sm', count > 0 ? 'text-yellow-400' : 'text-theme-text-secondary')}>
          {text}
        </span>
      )
    }
    case 'version': {
      const version = getNodeVersion(resource)
      const isSkewed = majorityNodeMinorVersion && version && !version.startsWith(`v${majorityNodeMinorVersion}`)
      return (
        <span className={clsx('text-sm', isSkewed ? 'text-yellow-400 font-medium' : 'text-theme-text-secondary')}>
          {version}
        </span>
      )
    }
    case 'cpu': {
      const m = metrics.nodes.get(resource.metadata?.name)
      if (!m || m.cpu === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const pct = m.cpuAllocatable > 0 ? (m.cpu / m.cpuAllocatable) * 100 : 0
      return <ResourceBar used={formatCPU(m.cpu)} total={formatCPU(m.cpuAllocatable)} percent={pct} />
    }
    case 'memory': {
      const m = metrics.nodes.get(resource.metadata?.name)
      if (!m || m.memory === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const pct = m.memoryAllocatable > 0 ? (m.memory / m.memoryAllocatable) * 100 : 0
      return <ResourceBar used={formatMemoryShort(m.memory)} total={formatMemoryShort(m.memoryAllocatable)} percent={pct} />
    }
    case 'pods': {
      const m = metrics.nodes.get(resource.metadata?.name)
      const allocatable = resource.status?.allocatable?.pods
      const podCount = m?.podCount ?? 0
      if (!allocatable) return <span className="text-sm text-theme-text-tertiary font-mono">{podCount || '-'}</span>
      // Allocatable pods is a Quantity, so a 1000-pod node reports "1k" —
      // a raw-integer read would see 1 and peg the bar at 21600%.
      const max = parseQuantityToNumber(allocatable)
      if (!Number.isFinite(max) || max <= 0) return <span className="text-sm text-theme-text-tertiary font-mono">{podCount || '-'}</span>
      const pct = (podCount / max) * 100
      return <ResourceBar used={String(podCount)} total={String(max)} percent={pct} colorScheme="count" />
    }
    case 'gpu': {
      const count = getNodeGpuCount(resource)
      if (!count) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary font-mono">{count}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function PVCCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPVCStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'capacity': {
      const capacity = getPVCCapacity(resource)
      return <span className="text-sm text-theme-text-secondary">{capacity}</span>
    }
    case 'storageClass':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.storageClassName || '-'}</span>
    case 'accessModes': {
      const modes = getPVCAccessModes(resource)
      return <span className="text-sm text-theme-text-secondary">{modes}</span>
    }
    case 'volume':
      return (
        <Tooltip content={resource.spec?.volumeName}>
          <span className="text-sm text-theme-text-secondary truncate block">{resource.spec?.volumeName || '-'}</span>
        </Tooltip>
      )
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RolloutCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <WorkloadStatusCell resource={resource} kind="rollouts" />
    case 'ready': {
      const ready = getRolloutReady(resource)
      const parts = ready.split('/')
      const allReady = parts.length === 2 && parts[0] === parts[1] && parts[0] !== '0'
      return (
        <span className={clsx('text-sm font-medium', allReady ? 'text-green-400' : 'text-yellow-400')}>
          {ready}
        </span>
      )
    }
    case 'strategy': {
      const strategy = getRolloutStrategy(resource)
      return <span className="text-sm text-theme-text-secondary">{strategy}</span>
    }
    case 'step': {
      const step = getRolloutStep(resource)
      return <span className="text-sm text-theme-text-secondary">{step || '-'}</span>
    }
    case 'images': {
      const images = getWorkloadImages(resource)
      if (images.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const display = images.length === 1 ? truncate(images[0], 40) : `${truncate(images[0], 30)} +${images.length - 1}`
      return (
        <Tooltip content={images.join('\n')}>
          <span className="text-sm text-theme-text-secondary truncate">{display}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function AnalysisRunCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getAnalysisRunStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'trigger': {
      const trigger = resource.metadata?.labels?.['rollout-type']
      const step = resource.metadata?.labels?.['step-index']
      if (!trigger) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <span className="text-sm text-theme-text-secondary">
          {trigger}
          {step ? ` (step ${step})` : ''}
        </span>
      )
    }
    case 'metrics': {
      const results: any[] = resource.status?.metricResults || []
      if (results.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const { total, passing, notPassing } = summarizeAnalysisMetrics(resource)
      return (
        <Tooltip
          content={results
            .map((m) => `${m.name}: ${m.phase || 'Unknown'}${m.dryRun ? ' (dry-run)' : ''}`)
            .join('\n')}
        >
          <span className={clsx('text-sm', notPassing > 0 ? 'text-amber-400' : 'text-theme-text-secondary')}>
            {notPassing > 0 ? `${notPassing}/${total} not passing` : `${passing}/${total} passing`}
          </span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WorkflowCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getWorkflowStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'duration': {
      const duration = getWorkflowDuration(resource)
      return <span className="text-sm text-theme-text-secondary">{duration || '-'}</span>
    }
    case 'progress': {
      const progress = getWorkflowProgress(resource)
      return <span className="text-sm text-theme-text-secondary">{progress || '-'}</span>
    }
    case 'template': {
      const template = getWorkflowTemplate(resource)
      return (
        <Tooltip content={template}>
          <span className="text-sm text-theme-text-secondary truncate block">{template || '-'}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function CronWorkflowCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getCronWorkflowStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'schedule':
      return <span className="font-mono text-xs text-theme-text-secondary">{getCronWorkflowSchedule(resource)}</span>
    case 'lastRun':
      return <span className="text-sm text-theme-text-secondary">{getCronWorkflowLastRun(resource)}</span>
    case 'template': {
      const template = getCronWorkflowTemplate(resource)
      return (
        <Tooltip content={template}>
          <span className="block truncate text-sm text-theme-text-secondary">{template}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}


function PersistentVolumeCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPVStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'capacity':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.capacity?.storage || '-'}</span>
    case 'accessModes': {
      const modes = getPVAccessModes(resource)
      return <span className="text-sm text-theme-text-secondary">{modes}</span>
    }
    case 'reclaimPolicy': {
      const policy = resource.spec?.persistentVolumeReclaimPolicy || '-'
      return (
        <span className={clsx('text-sm', policy === 'Delete' ? 'text-red-400' : policy === 'Retain' ? 'text-green-400' : 'text-theme-text-secondary')}>
          {policy}
        </span>
      )
    }
    case 'storageClass':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.storageClassName || '-'}</span>
    case 'claim': {
      const claim = getPVClaim(resource)
      return (
        <Tooltip content={claim}>
          <span className="text-sm text-theme-text-secondary truncate block">{claim}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function StorageClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'provisioner':
      return (
        <Tooltip content={getStorageClassProvisioner(resource)}>
          <span className="text-sm text-theme-text-secondary truncate block">{getStorageClassProvisioner(resource)}</span>
        </Tooltip>
      )
    case 'reclaimPolicy': {
      const policy = getStorageClassReclaimPolicy(resource)
      return (
        <span className={clsx('text-sm', policy === 'Delete' ? 'text-red-400' : policy === 'Retain' ? 'text-green-400' : 'text-theme-text-secondary')}>
          {policy}
        </span>
      )
    }
    case 'bindingMode':
      return <span className="text-sm text-theme-text-secondary">{getStorageClassBindingMode(resource)}</span>
    case 'expansion': {
      const expansion = getStorageClassExpansion(resource)
      return (
        <span className={clsx('text-sm', expansion === 'Yes' ? 'text-green-400' : 'text-theme-text-secondary')}>
          {expansion}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}



function GatewayCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getGatewayStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'class':
      return <span className="text-sm text-theme-text-secondary">{getGatewayClass(resource)}</span>
    case 'listeners': {
      const listeners = getGatewayListeners(resource)
      return (
        <Tooltip content={listeners}>
          <span className="text-sm text-theme-text-secondary truncate block">{listeners}</span>
        </Tooltip>
      )
    }
    case 'routes':
      return <span className="text-sm text-theme-text-secondary">{getGatewayAttachedRoutes(resource)}</span>
    case 'addresses': {
      const addrs = getGatewayAddresses(resource)
      return (
        <Tooltip content={addrs}>
          <span className="text-sm text-theme-text-secondary truncate block">{addrs}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function GatewayClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getGatewayClassStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'controller': {
      const controller = getGatewayClassController(resource)
      return (
        <Tooltip content={controller}>
          <span className="text-sm text-theme-text-secondary truncate block">{controller}</span>
        </Tooltip>
      )
    }
    case 'description': {
      const desc = getGatewayClassDescription(resource)
      return (
        <Tooltip content={desc}>
          <span className="text-sm text-theme-text-secondary truncate block">{desc}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

// Shared cell renderer for all Gateway API route types (HTTPRoute, GRPCRoute, TCPRoute, TLSRoute)
function RouteCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getRouteStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'hostnames': {
      const hostnames = getRouteHostnames(resource)
      return (
        <Tooltip content={hostnames}>
          <span className="text-sm text-theme-text-secondary truncate block">{hostnames}</span>
        </Tooltip>
      )
    }
    case 'parents':
      return <span className="text-sm text-theme-text-secondary">{getRouteParents(resource)}</span>
    case 'backends': {
      const backends = getRouteBackends(resource)
      return (
        <Tooltip content={backends}>
          <span className="text-sm text-theme-text-secondary truncate block">{backends}</span>
        </Tooltip>
      )
    }
    case 'rules':
      return <span className="text-sm text-theme-text-secondary">{getRouteRulesCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function SealedSecretCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getSealedSecretStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'keys':
      return <span className="text-sm text-theme-text-secondary">{getSealedSecretKeyCount(resource)}</span>
    case 'type':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.template?.type || 'Opaque'}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WorkflowTemplateCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'entrypoint':
      return <span className="text-sm text-theme-text-secondary">{getWorkflowTemplateEntrypoint(resource)}</span>
    case 'templates':
      return <span className="text-sm text-theme-text-secondary">{getWorkflowTemplateCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function NetworkPolicyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'policyTypes':
      return <span className="text-sm text-theme-text-secondary">{getNetworkPolicyTypes(resource)}</span>
    case 'selector': {
      const selector = getNetworkPolicySelector(resource)
      return (
        <Tooltip content={selector}>
          <span className="text-sm text-theme-text-secondary truncate block">{selector}</span>
        </Tooltip>
      )
    }
    case 'rules': {
      const { ingress, egress } = getNetworkPolicyRuleCount(resource)
      return <span className="text-sm text-theme-text-secondary">{ingress}i / {egress}e</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function PDBCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getPDBStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'budget':
      return <span className="text-sm text-theme-text-secondary">{getPDBBudget(resource)}</span>
    case 'healthy': {
      const healthy = getPDBHealthy(resource)
      return <span className="text-sm text-theme-text-secondary">{healthy}</span>
    }
    case 'allowed': {
      const allowed = getPDBAllowed(resource)
      return (
        <span className={clsx('text-sm font-medium', allowed > 0 ? 'text-green-400' : 'text-red-400')}>
          {allowed}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function ServiceAccountCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'automount': {
      const automount = getServiceAccountAutomount(resource)
      return <span className={clsx('text-sm', automount === 'No' ? 'text-green-400' : 'text-yellow-400')}>{automount}</span>
    }
    case 'secrets':
      return <span className="text-sm text-theme-text-secondary">{getServiceAccountSecretCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RoleCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'rules':
      return <span className="text-sm text-theme-text-secondary">{getRoleRuleCount(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RoleBindingCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'role': {
      const ref = resource.roleRef
      if (!ref?.name) return <span className="text-sm text-theme-text-tertiary">-</span>
      const kindColor = SEVERITY_BADGE.info
      return (
        <span className="text-sm text-theme-text-secondary inline-flex items-center gap-1.5">
          <span className={clsx('badge-sm', kindColor)}>{ref.kind === 'ClusterRole' ? 'CR' : 'R'}</span>
          {ref.name}
        </span>
      )
    }
    case 'subjects': {
      const subjects: any[] = resource.subjects || []
      if (subjects.length === 0) return <span className="text-sm text-theme-text-tertiary">0</span>
      const preview = subjects.slice(0, 2).map((s: any) => {
        const prefix = s.kind === 'ServiceAccount' ? 'sa:' : s.kind === 'Group' ? 'grp:' : ''
        return prefix + s.name
      }).join(', ')
      const more = subjects.length > 2 ? ` +${subjects.length - 2}` : ''
      return <span className="text-sm text-theme-text-secondary" title={subjects.map((s: any) => `${s.kind}:${s.name}`).join(', ')}>{preview}{more}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function IngressClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'controller':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.controller || '-'}</span>
    case 'default': {
      const isDefault = resource.metadata?.annotations?.['ingressclass.kubernetes.io/is-default-class'] === 'true'
      return <span className={clsx('text-sm', isDefault ? 'text-green-400' : 'text-theme-text-tertiary')}>{isDefault ? 'Yes' : 'No'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function LeaseCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'holder':
      return <span className="text-sm text-theme-text-secondary">{resource.spec?.holderIdentity || '-'}</span>
    case 'renewTime': {
      const renewTime = resource.spec?.renewTime
      if (!renewTime) return <span className="text-sm text-theme-text-tertiary">-</span>
      const elapsed = (Date.now() - new Date(renewTime).getTime()) / 1000
      const duration = resource.spec?.leaseDurationSeconds || 0
      const isStale = duration > 0 && elapsed > duration
      const diff = Date.now() - new Date(renewTime).getTime()
      const seconds = Math.floor(diff / 1000)
      const label = seconds < 60 ? `${seconds}s ago` : seconds < 3600 ? `${Math.floor(seconds / 60)}m ago` : seconds < 86400 ? `${Math.floor(seconds / 3600)}h ago` : `${Math.floor(seconds / 86400)}d ago`
      return <span className={clsx('text-sm', isStale ? 'text-red-400' : 'text-green-400')} title={new Date(renewTime).toLocaleString()}>{label}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function PriorityClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'value':
      return <span className="text-sm text-theme-text-secondary">{resource.value ?? '-'}</span>
    case 'globalDefault':
      return <span className={clsx('text-sm', resource.globalDefault ? 'text-green-400' : 'text-theme-text-tertiary')}>{resource.globalDefault ? 'Yes' : 'No'}</span>
    case 'preemptionPolicy':
      return <span className="text-sm text-theme-text-secondary">{resource.preemptionPolicy || 'PreemptLowerPriority'}</span>
    case 'description':
      return resource.description
        ? <span className="text-sm text-theme-text-secondary truncate" title={resource.description}>{resource.description}</span>
        : <span className="text-sm text-theme-text-tertiary">-</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function RuntimeClassCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'handler':
      return <span className="text-sm text-theme-text-secondary">{resource.handler || '-'}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function WebhookConfigCell({ resource, column }: { resource: any; column: string }) {
  const webhooks = resource.webhooks || []
  switch (column) {
    case 'webhooks':
      return <span className="text-sm text-theme-text-secondary">{webhooks.length}</span>
    case 'failurePolicy': {
      const hasFail = webhooks.some((w: any) => w.failurePolicy === 'Fail')
      return hasFail
        ? <span className={clsx('badge', SEVERITY_BADGE.error)}>Fail</span>
        : <span className="text-sm text-theme-text-secondary">Ignore</span>
    }
    case 'target': {
      if (webhooks.length === 0) return <span className="text-sm text-theme-text-tertiary">-</span>
      const first = webhooks[0]
      const svc = first.clientConfig?.service
      const target = svc ? `${svc.namespace}/${svc.name}` : first.clientConfig?.url || '-'
      const more = webhooks.length > 1 ? ` +${webhooks.length - 1}` : ''
      return <span className="text-sm text-theme-text-secondary truncate" title={target}>{target}{more && <span className="text-theme-text-tertiary">{more}</span>}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

function EventCell({ resource, column }: { resource: any; column: string }) {
  const eventType = resource.type || 'Normal'
  const isWarning = eventType === 'Warning'

  switch (column) {
    case 'type':
      return (
        <span className={clsx(
          'badge',
          EVENT_TYPE_COLORS[eventType] || SEVERITY_BADGE.info
        )}>
          {eventType}
        </span>
      )
    case 'reason':
      return (
        <span className={clsx(
          'text-sm font-medium',
          isWarning ? 'text-amber-400' : 'text-theme-text-secondary'
        )}>
          {resource.reason || '-'}
        </span>
      )
    case 'message': {
      const message = resource.message || ''
      return (
        <Tooltip content={message}>
          <span className="text-sm text-theme-text-secondary truncate block max-w-64">
            {message || '-'}
          </span>
        </Tooltip>
      )
    }
    case 'object': {
      const obj = resource.involvedObject
      if (!obj) return <span className="text-sm text-theme-text-tertiary">-</span>
      const objRef = `${obj.kind}/${obj.name}`
      return (
        <Tooltip content={`${obj.kind}: ${obj.namespace ? obj.namespace + '/' : ''}${obj.name}`}>
          <span className="text-sm text-theme-text-secondary truncate block">
            {objRef}
          </span>
        </Tooltip>
      )
    }
    case 'count': {
      const count = resource.count || 1
      return (
        <span className={clsx(
          'text-sm',
          count > 1 ? 'text-amber-400 font-medium' : 'text-theme-text-secondary'
        )}>
          {count}
        </span>
      )
    }
    case 'lastSeen': {
      const lastTimestamp = resource.lastTimestamp || resource.metadata?.creationTimestamp
      if (!lastTimestamp) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary">{formatAge(lastTimestamp)}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
