import { useCallback, useEffect, useMemo } from 'react'
import type { MouseEvent, ReactNode } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import {
  AlertTriangle,
  Anchor,
  ArrowLeft,
  ArrowLeftRight,
  CheckCircle2,
  Code,
  FileText,
  GitCompare,
  Link2,
  Package,
  Settings,
} from 'lucide-react'
import { PaneLoader } from '@skyhook-io/k8s-ui'
import {
  useCloudRole,
  useHelmHooksDiff,
  useHelmManifestDiff,
  useHelmNotesDiff,
  useHelmRelease,
  useHelmResourceDiff,
  useHelmValuesDiff,
} from '../../api/client'
import type { HelmHook, HelmRevision, HooksDiff, ResourceDiff } from '../../types'
import { getHelmStatusColor, getKindBadgeColor, SEVERITY_BADGE } from '../../utils/badge-colors'
import { formatDate } from './helm-utils'
import { DiffLine, hasDiffBodyChange } from './ManifestDiffViewer'
import { RoleGatedPanel } from './RoleGatedPanel'

type DiffTone = 'success' | 'warning' | 'error' | 'info' | 'neutral'

interface DiffStats {
  changed: boolean
  additions: number
  removals: number
  hunks: number
}

interface ParsedReleaseParam {
  namespace: string
  name: string
}

export function HelmCompareRoute() {
  const navigate = useNavigate()
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  const { canAtLeast } = useCloudRole()
  const canViewSensitive = canAtLeast('member')

  const releaseRef = parseReleaseParam(searchParams.get('release'))
  const storageNamespace = searchParams.get('releaseStorage') || undefined
  const helmNamespace = storageNamespace || releaseRef?.namespace || ''
  const releaseName = releaseRef?.name || ''
  const revision1Param = parsePositiveInt(searchParams.get('revision1'))
  const revision2Param = parsePositiveInt(searchParams.get('revision2'))

  const releaseQuery = useHelmRelease(helmNamespace, releaseName)
  const release = releaseQuery.data
  const revisions = useMemo(
    () => [...(release?.history || [])].sort((a, b) => a.revision - b.revision),
    [release?.history],
  )
  const defaultRightRevision = revision2Param || release?.revision || revisions.at(-1)?.revision || 0
  const defaultLeftRevision = revision1Param || previousRevision(revisions, defaultRightRevision) || revisions.at(0)?.revision || 0
  const revision1 = defaultLeftRevision
  const revision2 = defaultRightRevision
  const pairReady = Boolean(helmNamespace && releaseName && revision1 > 0 && revision2 > 0 && revision1 !== revision2)
  const diffEnabled = canViewSensitive && pairReady

  const left = revisions.find((r) => r.revision === revision1)
  const right = revisions.find((r) => r.revision === revision2)

  const manifestDiff = useHelmManifestDiff(helmNamespace, releaseName, revision1, revision2, diffEnabled)
  const valuesDiff = useHelmValuesDiff(helmNamespace, releaseName, revision1, revision2, false, diffEnabled)
  const notesDiff = useHelmNotesDiff(helmNamespace, releaseName, revision1, revision2, diffEnabled)
  const hooksDiff = useHelmHooksDiff(helmNamespace, releaseName, revision1, revision2, diffEnabled)
  const resourceDiff = useHelmResourceDiff(helmNamespace, releaseName, revision1, revision2, diffEnabled)

  const updateRevision = useCallback(
    (key: 'revision1' | 'revision2', value: number) => {
      const params = new URLSearchParams(searchParams)
      params.set(key, String(value))
      setSearchParams(params, { replace: true })
    },
    [searchParams, setSearchParams],
  )

  const swapRevisions = useCallback(() => {
    if (!revision1 || !revision2) return
    const params = new URLSearchParams(searchParams)
    params.set('revision1', String(revision2))
    params.set('revision2', String(revision1))
    setSearchParams(params, { replace: true })
  }, [revision1, revision2, searchParams, setSearchParams])

  const backToRelease = useCallback(() => {
    const params = new URLSearchParams()
    const globalNamespaces = searchParams.get('namespaces')
    if (globalNamespaces) params.set('namespaces', globalNamespaces)
    if (releaseRef) params.set('release', `${releaseRef.namespace}/${releaseRef.name}`)
    if (storageNamespace) params.set('releaseStorage', storageNamespace)
    navigate({ pathname: '/helm', search: params.toString() })
  }, [navigate, releaseRef, searchParams, storageNamespace])

  useEffect(() => {
    if (!location.hash) return
    const sectionId = decodeURIComponent(location.hash.slice(1))
    const timeout = window.setTimeout(() => {
      document.getElementById(sectionId)?.scrollIntoView({ block: 'start' })
    }, 0)
    return () => window.clearTimeout(timeout)
  }, [location.hash, releaseName, revision1, revision2])

  if (!releaseRef) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 bg-theme-base p-8 text-center">
        <div className="text-sm font-medium text-theme-text-primary">This Helm compare link is missing a release.</div>
        <button onClick={() => navigate('/helm')} className="btn-brand rounded-lg px-3 py-1.5 text-xs font-medium">
          Back to Helm
        </button>
      </div>
    )
  }

  if (releaseQuery.isLoading && !release) {
    return <PaneLoader label="Loading release..." className="h-full bg-theme-base" />
  }

  if (releaseQuery.error && !release) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 bg-theme-base p-8 text-center">
        <AlertTriangle className="h-8 w-8 text-red-500" />
        <div className="text-sm font-medium text-theme-text-primary">Release not found</div>
        <div className="max-w-lg text-xs text-theme-text-secondary">
          {releaseQuery.error instanceof Error ? releaseQuery.error.message : 'Radar could not load this Helm release.'}
        </div>
        <button onClick={backToRelease} className="rounded-lg border border-theme-border bg-theme-elevated px-3 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover">
          Back to release
        </button>
      </div>
    )
  }

  return (
    <div className="flex h-full min-w-0 flex-col bg-theme-base">
      <header className="shrink-0 border-b border-theme-border bg-theme-surface/95 px-4 py-3">
        <div className="flex w-full flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <button
              type="button"
              onClick={backToRelease}
              className="mb-2 inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              Back to release
            </button>
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <Package className="h-5 w-5 shrink-0 text-purple-400" />
              <h1 className="truncate text-lg font-semibold text-theme-text-primary">{releaseName}</h1>
              {release && (
                <span className={clsx('badge', getHelmStatusColor(release.status))}>{release.status}</span>
              )}
              {storageNamespace && storageNamespace !== releaseRef.namespace && (
                <span className={clsx('badge', SEVERITY_BADGE.neutral)}>stored in {storageNamespace}</span>
              )}
            </div>
            <div className="mt-1 text-sm text-theme-text-tertiary">{releaseRef.namespace}</div>
          </div>

          <div className="flex flex-wrap items-center gap-2 rounded-lg border border-theme-border bg-theme-base/70 p-2">
            <RevisionSelect
              label="From"
              value={revision1}
              revisions={revisions}
              onChange={(rev) => updateRevision('revision1', rev)}
            />
            <button
              type="button"
              onClick={swapRevisions}
              disabled={!pairReady}
              className="rounded-md border border-theme-border bg-theme-elevated p-1.5 text-theme-text-secondary shadow-theme-sm transition-colors hover:bg-theme-hover hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-50"
              title="Swap revisions"
            >
              <ArrowLeftRight className="h-4 w-4" />
            </button>
            <RevisionSelect
              label="To"
              value={revision2}
              revisions={revisions}
              onChange={(rev) => updateRevision('revision2', rev)}
            />
          </div>
        </div>
      </header>

      <RoleGatedPanel min="member" feature="release revision comparison">
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="grid w-full grid-cols-1 gap-4 px-4 py-4 xl:grid-cols-[220px_minmax(0,1fr)]">
            <nav className="hidden xl:block">
              <div className="sticky top-4 rounded-xl border border-theme-border bg-theme-surface p-2 shadow-theme-sm">
                <div className="px-2 pb-2 text-[11px] font-medium uppercase text-theme-text-tertiary">Compare</div>
                {[
                  ['summary', 'Summary'],
                  ['manifest', 'Manifest'],
                  ['values', 'Values'],
                  ['hooks', 'Hooks'],
                  ['notes', 'Notes'],
                  ['resources', 'Resources'],
                ].map(([id, label]) => (
                  <a
                    key={id}
                    href={`#${id}`}
                    onClick={(event) => scrollCompareSection(event, id)}
                    className="block rounded-md px-2 py-1.5 text-xs text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary"
                  >
                    {label}
                  </a>
                ))}
              </div>
            </nav>

            <main className="min-w-0 space-y-4">
              {!pairReady && (
                <div className="card-inner-lg flex items-start gap-3">
                  <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-amber-500" />
                  <div>
                    <div className="text-sm font-medium text-theme-text-primary">Pick two different revisions to compare</div>
                    <div className="mt-1 text-sm text-theme-text-secondary">Use the revision selectors above to choose a source and target revision.</div>
                  </div>
                </div>
              )}

              <section id="summary" className="scroll-mt-4 space-y-4">
                <CompareSummary
                  left={left}
                  right={right}
                  revision1={revision1}
                  revision2={revision2}
                  manifestDiff={manifestDiff.data?.diff}
                  manifestLoading={manifestDiff.isLoading}
                  valuesDiff={valuesDiff.data?.diff}
                  valuesLoading={valuesDiff.isLoading}
                  notesDiff={notesDiff.data?.diff}
                  notesLoading={notesDiff.isLoading}
                  hooksDiff={hooksDiff.data}
                  hooksLoading={hooksDiff.isLoading}
                  resourceDiff={resourceDiff.data}
                  resourceLoading={resourceDiff.isLoading}
                />
              </section>

              <DiffSection
                id="manifest"
                icon={Code}
                title="Rendered manifest diff"
                description="Rendered Kubernetes YAML is the ground truth for what Helm would apply between these revisions."
                diff={manifestDiff.data?.diff || ''}
                isLoading={manifestDiff.isLoading}
                error={manifestDiff.error}
                emptyLabel="No rendered manifest changes found."
              />

              <DiffSection
                id="values"
                icon={Settings}
                title="User-supplied values diff"
                description="Only values explicitly supplied to the release are compared here; computed chart defaults can still affect the rendered manifest."
                diff={valuesDiff.data?.diff || ''}
                isLoading={valuesDiff.isLoading}
                error={valuesDiff.error}
                emptyLabel="No user-supplied value changes found."
              />

              <HooksDiffSection diff={hooksDiff.data} isLoading={hooksDiff.isLoading} error={hooksDiff.error} />

              <DiffSection
                id="notes"
                icon={FileText}
                title="Release notes diff"
                description="NOTES.txt output can reveal chart-level instructions that changed without changing live Kubernetes objects."
                diff={notesDiff.data?.diff || ''}
                isLoading={notesDiff.isLoading}
                error={notesDiff.error}
                emptyLabel="No release notes changes found."
              />

              <ResourceInventoryDiffSection
                diff={resourceDiff.data}
                isLoading={resourceDiff.isLoading}
                error={resourceDiff.error}
                left={left}
                right={right}
              />
            </main>
          </div>
        </div>
      </RoleGatedPanel>
    </div>
  )
}

function RevisionSelect({
  label,
  value,
  revisions,
  onChange,
}: {
  label: string
  value: number
  revisions: HelmRevision[]
  onChange: (revision: number) => void
}) {
  return (
    <label className="flex items-center gap-2 text-xs text-theme-text-tertiary">
      <span>{label}</span>
      <select
        value={value || ''}
        onChange={(event) => onChange(Number(event.target.value))}
        className="rounded-md border border-theme-border bg-theme-elevated px-2 py-1.5 text-sm font-medium text-theme-text-primary"
      >
        {revisions.map((revision) => (
          <option key={revision.revision} value={revision.revision}>
            rev {revision.revision} - {revision.status} - {revision.chart}
          </option>
        ))}
      </select>
    </label>
  )
}

function CompareSummary({
  left,
  right,
  revision1,
  revision2,
  manifestDiff,
  manifestLoading,
  valuesDiff,
  valuesLoading,
  notesDiff,
  notesLoading,
  hooksDiff,
  hooksLoading,
  resourceDiff,
  resourceLoading,
}: {
  left?: HelmRevision
  right?: HelmRevision
  revision1: number
  revision2: number
  manifestDiff?: string
  manifestLoading: boolean
  valuesDiff?: string
  valuesLoading: boolean
  notesDiff?: string
  notesLoading: boolean
  hooksDiff?: HooksDiff
  hooksLoading: boolean
  resourceDiff?: ResourceDiff
  resourceLoading: boolean
}) {
  const manifestStats = diffStats(manifestDiff || '')
  const valuesStats = diffStats(valuesDiff || '')
  const notesStats = diffStats(notesDiff || '')
  const hookChanged = hooksDiff ? hooksDiff.added.length + hooksDiff.removed.length + hooksDiff.modified.length : 0
  const resourceChanged = resourceDiff ? resourceDiff.added.length + resourceDiff.removed.length + resourceDiff.modified.length : 0
  const chartChanged = Boolean(left && right && left.chart !== right.chart)
  const statusChanged = Boolean(left && right && left.status !== right.status)
  const descriptionChanged = Boolean(left && right && (left.description || '') !== (right.description || ''))

  return (
    <div className="card-inner-lg">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <GitCompare className="h-4 w-4 text-theme-text-secondary" />
            <h2 className="text-base font-semibold text-theme-text-primary">Revision {revision1} -&gt; {revision2}</h2>
          </div>
          <p className="mt-1 text-sm text-theme-text-secondary">
            Start with the rendered manifest diff below, then use values, hooks, notes, and inventory as supporting evidence.
          </p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {chartChanged && <span className={clsx('badge-sm', SEVERITY_BADGE.info)}>chart changed</span>}
          {statusChanged && <span className={clsx('badge-sm', SEVERITY_BADGE.warning)}>status changed</span>}
          {descriptionChanged && <span className={clsx('badge-sm', SEVERITY_BADGE.neutral)}>description changed</span>}
        </div>
      </div>

      <div className="grid grid-cols-1 gap-2 md:grid-cols-2 min-[1180px]:grid-cols-4">
        <MetadataDelta label="Chart" left={left?.chart} right={right?.chart} />
        <MetadataDelta label="Status" left={left?.status} right={right?.status} status />
        <MetadataDelta label="App version" left={left?.appVersion} right={right?.appVersion} />
        <MetadataDelta
          label="Updated"
          left={left?.updated ? formatDate(left.updated) : undefined}
          right={right?.updated ? formatDate(right.updated) : undefined}
        />
      </div>

      <div className="mt-3 grid grid-cols-1 gap-2 md:grid-cols-2 min-[1180px]:grid-cols-5">
        <SignalCard
          label="Manifest"
          loading={manifestLoading}
          tone={manifestStats.changed ? 'info' : 'neutral'}
          value={manifestStats.changed ? `${manifestStats.additions} add / ${manifestStats.removals} remove` : 'same'}
          sectionId="manifest"
        />
        <SignalCard
          label="Values"
          loading={valuesLoading}
          tone={valuesStats.changed ? 'info' : 'neutral'}
          value={valuesStats.changed ? `${valuesStats.additions} add / ${valuesStats.removals} remove` : 'same'}
          sectionId="values"
        />
        <SignalCard
          label="Hooks"
          loading={hooksLoading}
          tone={hookChanged > 0 ? 'warning' : 'neutral'}
          value={hooksDiff ? `${hookChanged} changed` : 'same'}
          sectionId="hooks"
        />
        <SignalCard
          label="Notes"
          loading={notesLoading}
          tone={notesStats.changed ? 'info' : 'neutral'}
          value={notesStats.changed ? `${notesStats.additions} add / ${notesStats.removals} remove` : 'same'}
          sectionId="notes"
        />
        <SignalCard
          label="Inventory"
          loading={resourceLoading}
          tone={resourceChanged > 0 ? 'warning' : 'neutral'}
          value={resourceDiff ? `${resourceChanged} changed` : 'same'}
          sectionId="resources"
        />
      </div>
    </div>
  )
}

function MetadataDelta({ label, left, right, status = false }: { label: string; left?: string; right?: string; status?: boolean }) {
  const changed = (left || '') !== (right || '')
  return (
    <div className="min-w-0 rounded-lg border border-theme-border bg-theme-base/50 p-2">
      <div className="mb-1 flex items-center justify-between gap-2">
        <div className="text-xs text-theme-text-tertiary">{label}</div>
        <span className={clsx('badge-sm', changed ? SEVERITY_BADGE.info : SEVERITY_BADGE.neutral)}>
          {changed ? 'changed' : 'same'}
        </span>
      </div>
      <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-1.5 text-xs">
        <span className={clsx(status && left ? ['badge-sm', getHelmStatusColor(left)] : ['min-w-0 truncate text-theme-text-secondary'])} title={left || '-'}>
          {left || '-'}
        </span>
        <span className="text-theme-text-tertiary">-&gt;</span>
        <span className={clsx(status && right ? ['badge-sm', getHelmStatusColor(right)] : ['min-w-0 truncate text-theme-text-primary'])} title={right || '-'}>
          {right || '-'}
        </span>
      </div>
    </div>
  )
}

function SignalCard({
  label,
  value,
  tone,
  loading,
  sectionId,
}: {
  label: string
  value: string
  tone: DiffTone
  loading: boolean
  sectionId: string
}) {
  return (
    <a
      href={`#${sectionId}`}
      onClick={(event) => scrollCompareSection(event, sectionId)}
      className="rounded-lg border border-theme-border bg-theme-surface p-3 transition-colors hover:bg-theme-elevated"
    >
      <div className="text-xs text-theme-text-tertiary">{label}</div>
      <div className="mt-2 flex items-center justify-between gap-2">
        <span className={clsx('badge-sm', loading ? SEVERITY_BADGE.neutral : SEVERITY_BADGE[tone])}>
          {loading ? 'loading' : value}
        </span>
      </div>
    </a>
  )
}

function scrollCompareSection(event: MouseEvent<HTMLAnchorElement>, sectionId: string) {
  event.preventDefault()
  document.getElementById(sectionId)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  const nextUrl = `${window.location.pathname}${window.location.search}#${encodeURIComponent(sectionId)}`
  window.history.replaceState(null, '', nextUrl)
}

function DiffSection({
  id,
  icon: Icon,
  title,
  description,
  diff,
  isLoading,
  error,
  emptyLabel,
}: {
  id: string
  icon: typeof Code
  title: string
  description: string
  diff: string
  isLoading: boolean
  error: unknown
  emptyLabel: string
}) {
  const stats = diffStats(diff)
  return (
    <section id={id} className="card-inner-lg scroll-mt-4">
      <SectionHeader icon={Icon} title={title} description={description}>
        {!isLoading && !error && (
          <div className="flex flex-wrap gap-1.5">
            <span className={clsx('badge-sm', stats.changed ? SEVERITY_BADGE.info : SEVERITY_BADGE.neutral)}>
              {stats.changed ? `${stats.hunks} hunks` : 'same'}
            </span>
            {stats.changed && (
              <>
                <span className={clsx('badge-sm', SEVERITY_BADGE.success)}>+{stats.additions}</span>
                <span className={clsx('badge-sm', SEVERITY_BADGE.error)}>-{stats.removals}</span>
              </>
            )}
          </div>
        )}
      </SectionHeader>

      {isLoading ? (
        <PaneLoader label="Computing diff..." className="h-36" />
      ) : error ? (
        <ErrorState error={error} />
      ) : !stats.changed ? (
        <EmptyDiffState label={emptyLabel} />
      ) : (
        <div className="mt-3 max-h-[620px] overflow-auto rounded-lg border border-theme-border bg-theme-base/60 font-mono text-xs">
          <div className="min-w-max p-3">
            {diff.split('\n').map((line, index) => (
              <DiffLine key={index} line={line} />
            ))}
          </div>
        </div>
      )}
    </section>
  )
}

function HooksDiffSection({ diff, isLoading, error }: { diff?: HooksDiff; isLoading: boolean; error: unknown }) {
  const changed = diff ? diff.added.length + diff.removed.length + diff.modified.length : 0
  return (
    <section id="hooks" className="card-inner-lg scroll-mt-4">
      <SectionHeader
        icon={Anchor}
        title="Hooks diff"
        description="Helm hooks can fail before or after normal resources, so changed hooks are called out separately from the rendered manifest."
      >
        {!isLoading && !error && diff && (
          <div className="flex flex-wrap gap-1.5">
            <span className={clsx('badge-sm', diff.modified.length ? SEVERITY_BADGE.info : SEVERITY_BADGE.neutral)}>
              {diff.modified.length} modified
            </span>
            <span className={clsx('badge-sm', diff.added.length ? SEVERITY_BADGE.success : SEVERITY_BADGE.neutral)}>
              {diff.added.length} added
            </span>
            <span className={clsx('badge-sm', diff.removed.length ? SEVERITY_BADGE.error : SEVERITY_BADGE.neutral)}>
              {diff.removed.length} removed
            </span>
          </div>
        )}
      </SectionHeader>

      {isLoading ? (
        <PaneLoader label="Comparing hooks..." className="h-32" />
      ) : error ? (
        <ErrorState error={error} />
      ) : !diff || changed === 0 ? (
        <EmptyDiffState label="No hook changes found." />
      ) : (
        <div className="mt-3 space-y-3">
          <HookGroup title="Modified hooks" tone="info" hooks={diff.modified} />
          <HookGroup title="Added hooks" tone="success" hooks={diff.added} />
          <HookGroup title="Removed hooks" tone="error" hooks={diff.removed} />
        </div>
      )}
    </section>
  )
}

function HookGroup({ title, tone, hooks }: { title: string; tone: DiffTone; hooks: HelmHook[] }) {
  if (hooks.length === 0) return null
  return (
    <div>
      <div className="mb-2 flex items-center gap-2 text-sm font-medium text-theme-text-primary">
        <span className={clsx('badge-sm', SEVERITY_BADGE[tone])}>{hooks.length}</span>
        {title}
      </div>
      <div className="grid grid-cols-1 gap-2 lg:grid-cols-2">
        {hooks.map((hook) => (
          <div key={hookKey(hook)} className="rounded-lg border border-theme-border bg-theme-base/50 p-3">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <span className={clsx('badge-sm', getKindBadgeColor(hook.kind))}>{hook.kind}</span>
              <span className="min-w-0 truncate text-sm font-medium text-theme-text-primary">{hook.name}</span>
              {hook.status && <span className={clsx('badge-sm', getHelmStatusColor(hook.status))}>{hook.status}</span>}
            </div>
            <div className="mt-2 flex flex-wrap gap-1.5 text-xs text-theme-text-tertiary">
              {hook.namespace && <span>{hook.namespace}</span>}
              {hook.events.map((event) => <span key={event} className="badge-sm bg-theme-elevated text-theme-text-secondary">{event}</span>)}
              {hook.weight !== 0 && <span>weight {hook.weight}</span>}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

function ResourceInventoryDiffSection({
  diff,
  isLoading,
  error,
  left,
  right,
}: {
  diff?: ResourceDiff
  isLoading: boolean
  error: unknown
  left?: HelmRevision
  right?: HelmRevision
}) {
  const changed = diff ? diff.added.length + diff.removed.length + diff.modified.length : 0
  const identityOverlap = diff ? diff.modified.length + diff.unchanged.length : 0
  const chartChanged = Boolean(left && right && left.chart !== right.chart)
  const lowPairingConfidence = Boolean(diff && chartChanged && changed > 0 && identityOverlap === 0)

  return (
    <section id="resources" className="card-inner-lg scroll-mt-4">
      <SectionHeader
        icon={Link2}
        title="Rendered resource inventory"
        description="Inventory shows object identities added, removed, or changed in place. It is supporting evidence, not a replacement for the manifest diff."
      >
        {!isLoading && !error && diff && (
          <div className="flex flex-wrap gap-1.5">
            <span className={clsx('badge-sm', diff.modified.length ? SEVERITY_BADGE.info : SEVERITY_BADGE.neutral)}>
              {diff.modified.length} modified
            </span>
            <span className={clsx('badge-sm', diff.added.length ? SEVERITY_BADGE.success : SEVERITY_BADGE.neutral)}>
              {diff.added.length} added
            </span>
            <span className={clsx('badge-sm', diff.removed.length ? SEVERITY_BADGE.error : SEVERITY_BADGE.neutral)}>
              {diff.removed.length} removed
            </span>
          </div>
        )}
      </SectionHeader>

      {isLoading ? (
        <PaneLoader label="Comparing resource inventory..." className="h-32" />
      ) : error ? (
        <ErrorState error={error} />
      ) : !diff || changed === 0 ? (
        <EmptyDiffState label="No rendered resource identity changes found." />
      ) : (
        <div className="mt-3 space-y-3">
          {lowPairingConfidence && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-theme-text-secondary">
              <div className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
                <div>
                  <div className="font-medium text-theme-text-primary">Resource identities do not overlap.</div>
                  <div className="mt-1">
                    The chart changed and Radar cannot pair old and new resources by name. Treat these added/removed counts as inventory context; use the rendered manifest diff above to see the actual cause.
                  </div>
                </div>
              </div>
            </div>
          )}
          <ModifiedResourceGroup changes={diff.modified} />
          <ResourceGroup title="Added resources" tone="success" resources={diff.added} />
          <ResourceGroup title="Removed resources" tone="error" resources={diff.removed} />
          <ResourceGroup title="Unchanged resources" tone="neutral" resources={diff.unchanged} collapsed />
        </div>
      )}
    </section>
  )
}

function ModifiedResourceGroup({ changes }: { changes: ResourceDiff['modified'] }) {
  if (changes.length === 0) return null
  return (
    <div>
      <div className="mb-2 flex items-center gap-2 text-sm font-medium text-theme-text-primary">
        <span className={clsx('badge-sm', SEVERITY_BADGE.info)}>{changes.length}</span>
        Modified in place
      </div>
      <div className="space-y-2">
        {changes.map((change) => (
          <div key={resourceKey(change)} className="rounded-lg border border-theme-border bg-theme-base/50 p-3">
            <ResourceChip resource={change} />
            <div className="mt-2 space-y-1">
              {change.fields.slice(0, 6).map((field, index) => (
                <div key={`${field.path}-${index}`} className="grid grid-cols-1 gap-1 rounded-md bg-theme-surface/70 px-2 py-1.5 text-xs lg:grid-cols-[260px_minmax(0,1fr)]">
                  <code className="truncate font-mono text-theme-text-tertiary" title={field.path}>{formatPathLabel(field.path)}</code>
                  <div className="min-w-0 truncate text-theme-text-secondary" title={`${formatDiffValue(field.oldValue, field.path)} -> ${formatDiffValue(field.newValue, field.path)}`}>
                    {formatDiffValue(field.oldValue, field.path)} <span className="text-theme-text-tertiary">-&gt;</span> <span className="text-theme-text-primary">{formatDiffValue(field.newValue, field.path)}</span>
                  </div>
                </div>
              ))}
              {change.fieldCount > 6 && (
                <div className="text-xs text-theme-text-tertiary">+{change.fieldCount - 6} more changed fields in the manifest diff</div>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

function ResourceGroup({
  title,
  tone,
  resources,
  collapsed = false,
}: {
  title: string
  tone: DiffTone
  resources: ResourceDiff['added']
  collapsed?: boolean
}) {
  if (resources.length === 0) return null
  const visible = collapsed ? resources.slice(0, 16) : resources
  return (
    <div>
      <div className="mb-2 flex items-center gap-2 text-sm font-medium text-theme-text-primary">
        <span className={clsx('badge-sm', SEVERITY_BADGE[tone])}>{resources.length}</span>
        {title}
      </div>
      <div className="grid grid-cols-1 gap-2 lg:grid-cols-2">
        {visible.map((resource) => <ResourceChip key={resourceKey(resource)} resource={resource} />)}
      </div>
      {visible.length < resources.length && (
        <div className="mt-2 text-xs text-theme-text-tertiary">+{resources.length - visible.length} more resources</div>
      )}
    </div>
  )
}

function ResourceChip({ resource }: { resource: ResourceDiff['added'][number] }) {
  return (
    <div className="min-w-0 rounded-lg border border-theme-border bg-theme-base/50 px-3 py-2">
      <div className="flex min-w-0 items-center gap-2">
        <span className={clsx('badge-sm shrink-0', getKindBadgeColor(resource.kind))}>{resource.kind}</span>
        <span className="min-w-0 truncate text-sm font-medium text-theme-text-primary">
          {resource.name}
        </span>
      </div>
      {resource.namespace && (
        <div className="mt-1 truncate text-xs text-theme-text-tertiary">{resource.namespace}</div>
      )}
    </div>
  )
}

function SectionHeader({
  icon: Icon,
  title,
  description,
  children,
}: {
  icon: typeof Code
  title: string
  description: string
  children?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <Icon className="h-4 w-4 text-theme-text-secondary" />
          <h3 className="text-base font-semibold text-theme-text-primary">{title}</h3>
        </div>
        <p className="mt-1 text-sm text-theme-text-secondary">{description}</p>
      </div>
      {children}
    </div>
  )
}

function EmptyDiffState({ label }: { label: string }) {
  return (
    <div className="mt-3 flex items-center gap-2 rounded-lg border border-theme-border bg-theme-base/50 px-3 py-3 text-sm text-theme-text-secondary">
      <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400" />
      {label}
    </div>
  )
}

function ErrorState({ error }: { error: unknown }) {
  return (
    <div className="mt-3 flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-3 text-sm text-theme-text-secondary">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-600 dark:text-red-400" />
      <div>{error instanceof Error ? error.message : 'Failed to load this comparison surface.'}</div>
    </div>
  )
}

function parseReleaseParam(value: string | null): ParsedReleaseParam | null {
  if (!value) return null
  const separator = value.indexOf('/')
  if (separator <= 0 || separator === value.length - 1) return null
  return {
    namespace: value.slice(0, separator),
    name: value.slice(separator + 1),
  }
}

function parsePositiveInt(value: string | null): number | undefined {
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function previousRevision(revisions: HelmRevision[], revision: number): number | undefined {
  const lower = revisions.filter((item) => item.revision < revision)
  return lower.at(-1)?.revision
}

function diffStats(diff: string): DiffStats {
  let additions = 0
  let removals = 0
  let hunks = 0
  for (const line of diff.split('\n')) {
    if (line.startsWith('@@')) hunks += 1
    else if (line.startsWith('+') && !line.startsWith('+++')) additions += 1
    else if (line.startsWith('-') && !line.startsWith('---')) removals += 1
  }
  return { changed: hasDiffBodyChange(diff), additions, removals, hunks }
}

function resourceKey(resource: ResourceDiff['added'][number]): string {
  return `${resource.apiVersion || ''}/${resource.kind}/${resource.namespace || ''}/${resource.name}`
}

function hookKey(hook: HelmHook): string {
  return `${hook.namespace || ''}/${hook.kind}/${hook.name}/${hook.events.join(',')}`
}

function formatPathLabel(path: string): string {
  return path
    .replace(/\[\*\]/g, '')
    .replace(/\[([^\]]+)\]/g, ' $1')
    .replace(/\./g, ' / ')
}

function formatDiffValue(value: unknown, path?: string): string {
  if (value === null || value === undefined) return 'none'
  if (typeof value === 'string') return truncate(value)
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  const structured = formatStructuredDiffValue(value, path)
  if (structured) return truncate(structured)
  try {
    return truncate(JSON.stringify(value))
  } catch {
    return truncate(String(value))
  }
}

function formatStructuredDiffValue(value: unknown, path?: string): string | undefined {
  if (!isRecord(value)) return undefined
  if (path?.includes('Probe')) return formatProbeValue(value)
  return undefined
}

function formatProbeValue(value: Record<string, unknown>): string | undefined {
  const handler = formatProbeHandler(value)
  if (!handler) return undefined
  const details: string[] = []
  const periodSeconds = numberField(value, 'periodSeconds')
  const timeoutSeconds = numberField(value, 'timeoutSeconds')
  const failureThreshold = numberField(value, 'failureThreshold')
  if (periodSeconds && periodSeconds > 0) details.push(`period ${periodSeconds}s`)
  if (timeoutSeconds && timeoutSeconds > 0) details.push(`timeout ${timeoutSeconds}s`)
  if (failureThreshold && failureThreshold > 0) details.push(`failure threshold ${failureThreshold}`)
  return details.length ? `${handler} (${details.join(', ')})` : handler
}

function formatProbeHandler(value: Record<string, unknown>): string | undefined {
  const normalizedHandler = stringField(value, 'handler')
  if (normalizedHandler) return formatNormalizedProbeHandler(normalizedHandler)
  if (isRecord(value.httpGet)) {
    const method = stringField(value.httpGet, 'scheme') || 'HTTP'
    const path = stringField(value.httpGet, 'path') || '/'
    const port = value.httpGet.port
    return `${method} GET ${path}${port !== undefined ? ` on ${String(port)}` : ''}`
  }
  if (isRecord(value.tcpSocket)) {
    const port = value.tcpSocket.port
    return `TCP socket${port !== undefined ? ` on ${String(port)}` : ''}`
  }
  if (isRecord(value.grpc)) {
    const port = value.grpc.port
    const service = stringField(value.grpc, 'service')
    return `gRPC${service ? ` ${service}` : ''}${port !== undefined ? ` on ${String(port)}` : ''}`
  }
  if (isRecord(value.exec) && Array.isArray(value.exec.command)) {
    return `exec ${value.exec.command.map(String).join(' ')}`
  }
  return undefined
}

function formatNormalizedProbeHandler(handler: string): string {
  if (handler.startsWith('httpGet:')) {
    const rest = handler.slice('httpGet:'.length)
    const schemeSeparator = rest.indexOf(':')
    const scheme = schemeSeparator >= 0 ? rest.slice(0, schemeSeparator) : ''
    const target = schemeSeparator >= 0 ? rest.slice(schemeSeparator + 1) : rest
    const slashIndex = target.indexOf('/')
    const port = slashIndex >= 0 ? target.slice(0, slashIndex) : target
    const path = slashIndex >= 0 ? target.slice(slashIndex) : '/'
    return `${scheme || 'HTTP'} GET ${path}${port ? ` on ${port}` : ''}`
  }
  if (handler.startsWith('tcpSocket:')) {
    const port = handler.slice('tcpSocket:'.length)
    return `TCP socket${port ? ` on ${port}` : ''}`
  }
  if (handler.startsWith('grpc:')) {
    const target = handler.slice('grpc:'.length)
    const [port, service] = target.split('/', 2)
    return `gRPC${service ? ` ${service}` : ''}${port ? ` on ${port}` : ''}`
  }
  return handler
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringField(value: Record<string, unknown>, field: string): string | undefined {
  const raw = value[field]
  return typeof raw === 'string' ? raw : undefined
}

function numberField(value: Record<string, unknown>, field: string): number | undefined {
  const raw = value[field]
  return typeof raw === 'number' ? raw : undefined
}

function truncate(value: string): string {
  return value.length > 96 ? `${value.slice(0, 93)}...` : value
}
