import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent, ReactNode } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import {
  AlertTriangle,
  Anchor,
  ArrowLeft,
  ArrowLeftRight,
  Check,
  CheckCircle2,
  ChevronDown,
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
import { Tooltip } from '../ui/Tooltip'

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
    let cancelled = false
    let attempts = 0
    let timeout: number | undefined
    const scroll = () => {
      if (cancelled) return
      document.getElementById(sectionId)?.scrollIntoView({ block: 'start' })
      attempts += 1
      if (attempts < 8) timeout = window.setTimeout(scroll, 75)
    }
    timeout = window.setTimeout(scroll, 0)
    return () => {
      cancelled = true
      if (timeout) window.clearTimeout(timeout)
    }
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
            <Tooltip content="Swap revisions" position="bottom">
              <button
                type="button"
                onClick={swapRevisions}
                disabled={!pairReady}
                aria-label="Swap revisions"
                className="rounded-md border border-theme-border bg-theme-elevated p-1.5 text-theme-text-secondary shadow-theme-sm transition-colors hover:bg-theme-hover hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-50"
              >
                <ArrowLeftRight className="h-4 w-4" />
              </button>
            </Tooltip>
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
          <div className={clsx('grid w-full grid-cols-1 gap-4 px-4 py-4', pairReady && 'xl:grid-cols-[220px_minmax(0,1fr)]')}>
            {pairReady && (
              <nav className="hidden xl:block">
                <div className="sticky top-4 rounded-xl border border-theme-border bg-theme-surface p-2 shadow-theme-sm">
                  <div className="px-2 pb-2 text-[11px] font-medium uppercase text-theme-text-tertiary">Compare</div>
                  {[
                    ['summary', 'Summary'],
                    ['manifest', 'Manifest'],
                    ['resources', 'Resources'],
                    ['values', 'Values'],
                    ['hooks', 'Hooks'],
                    ['notes', 'Notes'],
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
            )}

            <main className="min-w-0 space-y-4">
              {!pairReady ? (
                <div className="card-inner-lg flex items-start gap-3">
                  <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-amber-500" />
                  <div>
                    <div className="text-sm font-medium text-theme-text-primary">Pick two different revisions to compare</div>
                    <div className="mt-1 text-sm text-theme-text-secondary">Use the revision selectors above to choose a source and target revision.</div>
                  </div>
                </div>
              ) : (
                <>
                  <section id="summary" className="scroll-mt-4 space-y-4">
                    <CompareSummary
                      left={left}
                      right={right}
                      revision1={revision1}
                      revision2={revision2}
                      manifestDiff={manifestDiff.data?.diff}
                      manifestLoading={manifestDiff.isLoading}
                      manifestError={manifestDiff.error}
                      valuesDiff={valuesDiff.data?.diff}
                      valuesLoading={valuesDiff.isLoading}
                      valuesError={valuesDiff.error}
                      notesDiff={notesDiff.data?.diff}
                      notesLoading={notesDiff.isLoading}
                      notesError={notesDiff.error}
                      hooksDiff={hooksDiff.data}
                      hooksLoading={hooksDiff.isLoading}
                      hooksError={hooksDiff.error}
                      resourceDiff={resourceDiff.data}
                      resourceLoading={resourceDiff.isLoading}
                      resourceError={resourceDiff.error}
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

                  <ResourceInventoryDiffSection
                    diff={resourceDiff.data}
                    isLoading={resourceDiff.isLoading}
                    error={resourceDiff.error}
                    left={left}
                    right={right}
                    revision1={revision1}
                    revision2={revision2}
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

                </>
              )}
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
  const [open, setOpen] = useState(false)
  const [activeIndex, setActiveIndex] = useState(0)
  const rootRef = useRef<HTMLDivElement>(null)
  const listboxId = useId()
  const selected = revisions.find((revision) => revision.revision === value)
  const selectedIndex = Math.max(0, revisions.findIndex((revision) => revision.revision === value))

  useEffect(() => {
    if (!open) return
    const close = () => setOpen(false)
    const onDown = (event: globalThis.MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) close()
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') close()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', close)
    }
  }, [open])

  useEffect(() => {
    if (open) setActiveIndex(selectedIndex)
  }, [open, selectedIndex])

  useEffect(() => {
    if (!open) return
    const revision = revisions[activeIndex]
    if (!revision) return
    document.getElementById(`${listboxId}-${revision.revision}`)?.scrollIntoView({ block: 'nearest' })
  }, [activeIndex, listboxId, open, revisions])

  const selectRevision = (revision: number) => {
    setOpen(false)
    if (revision !== value) onChange(revision)
  }

  const moveActive = (delta: number) => {
    if (revisions.length === 0) return
    setActiveIndex((current) => {
      const next = current + delta
      if (next < 0) return revisions.length - 1
      if (next >= revisions.length) return 0
      return next
    })
  }

  const onKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (revisions.length === 0) return
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (!open) {
        setOpen(true)
        setActiveIndex(selectedIndex)
      } else {
        moveActive(1)
      }
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (!open) {
        setOpen(true)
        setActiveIndex(selectedIndex)
      } else {
        moveActive(-1)
      }
    } else if (event.key === 'Enter' && open) {
      event.preventDefault()
      selectRevision(revisions[activeIndex]?.revision || value)
    }
  }

  return (
    <div ref={rootRef} className="relative flex items-center gap-2 text-xs text-theme-text-tertiary">
      <span>{label}</span>
      <button
        type="button"
        onClick={() => setOpen((next) => !next)}
        onKeyDown={onKeyDown}
        disabled={revisions.length === 0}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listboxId}
        aria-activedescendant={open ? `${listboxId}-${revisions[activeIndex]?.revision || value}` : undefined}
        aria-label={`${label} revision`}
        className={clsx(
          'inline-flex max-w-[34rem] items-center gap-2 rounded-md border border-theme-border bg-theme-elevated px-2.5 py-1.5 text-left text-sm font-medium text-theme-text-primary shadow-theme-sm transition-colors',
          revisions.length === 0 ? 'cursor-not-allowed opacity-60' : 'hover:bg-theme-hover',
        )}
      >
        <span className="min-w-0 truncate">{selected ? formatRevisionOption(selected) : 'No revisions'}</span>
        <ChevronDown className={clsx('h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform', open && 'rotate-180')} />
      </button>
      {open && (
        <div
          id={listboxId}
          role="listbox"
          className="absolute right-0 top-full z-50 mt-1 max-h-80 w-[min(36rem,calc(100vw-2rem))] overflow-y-auto rounded-lg border border-theme-border bg-theme-surface p-1 shadow-theme-lg"
        >
          {revisions.map((revision, index) => {
            const selectedRevision = revision.revision === value
            const activeRevision = index === activeIndex
            return (
              <button
                key={revision.revision}
                id={`${listboxId}-${revision.revision}`}
                type="button"
                role="option"
                aria-selected={selectedRevision}
                onMouseEnter={() => setActiveIndex(index)}
                onClick={() => selectRevision(revision.revision)}
                className={clsx(
                  'flex w-full min-w-0 items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm transition-colors',
                  selectedRevision ? 'selection selection-ring' : activeRevision ? 'bg-theme-hover' : 'hover:bg-theme-hover',
                )}
              >
                <span className="w-14 shrink-0 font-medium text-theme-text-primary">rev {revision.revision}</span>
                <span className={clsx('badge-sm shrink-0', getHelmStatusColor(revision.status))}>{revision.status}</span>
                <span className="min-w-0 flex-1 truncate text-theme-text-secondary">{revision.chart}</span>
                {selectedRevision && <Check className="h-4 w-4 shrink-0 text-accent" />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function formatRevisionOption(revision: HelmRevision): string {
  return `rev ${revision.revision} - ${revision.status} - ${revision.chart}`
}

function CompareSummary({
  left,
  right,
  revision1,
  revision2,
  manifestDiff,
  manifestLoading,
  manifestError,
  valuesDiff,
  valuesLoading,
  valuesError,
  notesDiff,
  notesLoading,
  notesError,
  hooksDiff,
  hooksLoading,
  hooksError,
  resourceDiff,
  resourceLoading,
  resourceError,
}: {
  left?: HelmRevision
  right?: HelmRevision
  revision1: number
  revision2: number
  manifestDiff?: string
  manifestLoading: boolean
  manifestError: unknown
  valuesDiff?: string
  valuesLoading: boolean
  valuesError: unknown
  notesDiff?: string
  notesLoading: boolean
  notesError: unknown
  hooksDiff?: HooksDiff
  hooksLoading: boolean
  hooksError: unknown
  resourceDiff?: ResourceDiff
  resourceLoading: boolean
  resourceError: unknown
}) {
  const manifestStats = diffStats(manifestDiff || '')
  const valuesStats = diffStats(valuesDiff || '')
  const notesStats = diffStats(notesDiff || '')
  const hookChanged = hooksDiff ? hooksDiff.added.length + hooksDiff.removed.length + hooksDiff.modified.length : 0
  const resourceChanged = resourceDiff ? resourceDiff.added.length + resourceDiff.removed.length + resourceDiff.modified.length : 0
  const resourceParseErrors = resourceDiff?.parseErrorCount || 0

  return (
    <div className="card-inner-lg">
      <div className="mb-4">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <GitCompare className="h-4 w-4 text-theme-text-secondary" />
            <h2 className="text-base font-semibold text-theme-text-primary">Revision {revision1} -&gt; {revision2}</h2>
          </div>
          <p className="mt-1 text-sm text-theme-text-secondary">
            Start with the rendered manifest diff below, then use resources, values, hooks, and notes as supporting evidence.
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium uppercase text-theme-text-tertiary">Evidence</span>
        <SignalPill
          label="Manifest"
          loading={manifestLoading}
          error={manifestError}
          tone={manifestStats.changed ? 'info' : 'neutral'}
          value={manifestStats.changed ? `${manifestStats.additions} add / ${manifestStats.removals} remove` : 'same'}
          sectionId="manifest"
        />
        <SignalPill
          label="Resources"
          loading={resourceLoading}
          error={resourceError}
          tone={resourceParseErrors > 0 ? 'warning' : resourceChanged > 0 ? 'warning' : 'neutral'}
          value={resourceDiff ? (resourceParseErrors > 0 ? `partial, ${resourceChanged} changed` : resourceChanged > 0 ? `${resourceChanged} changed` : 'same') : 'same'}
          sectionId="resources"
        />
        <SignalPill
          label="Values"
          loading={valuesLoading}
          error={valuesError}
          tone={valuesStats.changed ? 'info' : 'neutral'}
          value={valuesStats.changed ? `${valuesStats.additions} add / ${valuesStats.removals} remove` : 'same'}
          sectionId="values"
        />
        <SignalPill
          label="Hooks"
          loading={hooksLoading}
          error={hooksError}
          tone={hookChanged > 0 ? 'warning' : 'neutral'}
          value={hooksDiff ? `${hookChanged} changed` : 'same'}
          sectionId="hooks"
        />
        <SignalPill
          label="Notes"
          loading={notesLoading}
          error={notesError}
          tone={notesStats.changed ? 'info' : 'neutral'}
          value={notesStats.changed ? `${notesStats.additions} add / ${notesStats.removals} remove` : 'same'}
          sectionId="notes"
        />
      </div>

      <div className="mt-4">
        <div className="mb-2 text-xs font-medium uppercase text-theme-text-tertiary">Release metadata</div>
        <MetadataDiffTable
          revision1={revision1}
          revision2={revision2}
          rows={[
            { label: 'Chart', left: left?.chart, right: right?.chart },
            { label: 'Status', left: left?.status, right: right?.status, status: true },
            { label: 'App version', left: left?.appVersion, right: right?.appVersion },
            {
              label: 'Updated',
              left: left?.updated ? formatDate(left.updated) : undefined,
              right: right?.updated ? formatDate(right.updated) : undefined,
            },
          ]}
        />
      </div>
    </div>
  )
}

interface MetadataDiffRow {
  label: string
  left?: string
  right?: string
  status?: boolean
}

function MetadataDiffTable({ revision1, revision2, rows }: { revision1: number; revision2: number; rows: MetadataDiffRow[] }) {
  return (
    <div className="overflow-hidden rounded-lg border border-theme-border bg-theme-base/50">
      <div className="hidden grid-cols-[7.5rem_minmax(0,1fr)_1.5rem_minmax(0,1fr)_5rem] items-center gap-3 border-b border-theme-border bg-theme-surface/70 px-3 py-2 text-xs font-medium text-theme-text-tertiary md:grid">
        <span>Field</span>
        <span>Rev {revision1}</span>
        <span />
        <span>Rev {revision2}</span>
        <span className="text-right">Diff</span>
      </div>
      {rows.map((row) => <MetadataDiffTableRow key={row.label} row={row} />)}
    </div>
  )
}

function MetadataDiffTableRow({ row }: { row: MetadataDiffRow }) {
  const changed = (row.left || '') !== (row.right || '')
  return (
    <div
      className={clsx(
        'grid grid-cols-1 gap-1 border-t border-theme-border px-3 py-2.5 text-sm first:border-t-0 md:grid-cols-[7.5rem_minmax(0,1fr)_1.5rem_minmax(0,1fr)_5rem] md:items-center md:gap-3',
        changed && 'bg-accent-muted/30',
      )}
    >
      <div className="font-medium text-theme-text-tertiary md:text-theme-text-secondary">{row.label}</div>
      <MetadataValue value={row.left} status={row.status} muted />
      <div className="hidden text-center text-theme-text-tertiary md:block">-&gt;</div>
      <MetadataValue value={row.right} status={row.status} />
      <div className="pt-1 md:pt-0 md:text-right">
        <span className={clsx('badge-sm', changed ? SEVERITY_BADGE.info : SEVERITY_BADGE.neutral)}>
          {changed ? 'changed' : 'same'}
        </span>
      </div>
    </div>
  )
}

function MetadataValue({ value, status = false, muted = false }: { value?: string; status?: boolean; muted?: boolean }) {
  if (status && value) {
    return <span className={clsx('badge-sm w-fit', getHelmStatusColor(value))}>{value}</span>
  }
  return (
    <div className={clsx('min-w-0 whitespace-normal break-words', muted ? 'text-theme-text-secondary' : 'text-theme-text-primary')}>
      {value || '-'}
    </div>
  )
}

function SignalPill({
  label,
  value,
  tone,
  loading,
  error,
  sectionId,
}: {
  label: string
  value: string
  tone: DiffTone
  loading: boolean
  error: unknown
  sectionId: string
}) {
  const displayTone: DiffTone = error ? 'warning' : loading ? 'neutral' : tone
  const displayValue = error ? 'failed' : loading ? 'loading' : value
  return (
    <a
      href={`#${sectionId}`}
      onClick={(event) => scrollCompareSection(event, sectionId)}
      className="inline-flex items-center gap-2 rounded-md border border-theme-border bg-theme-surface px-2.5 py-1.5 text-sm transition-colors hover:bg-theme-elevated"
    >
      <span className="font-medium text-theme-text-secondary">{label}</span>
      <span className={clsx('badge-sm', SEVERITY_BADGE[displayTone])}>{displayValue}</span>
    </a>
  )
}

function scrollCompareSection(event: ReactMouseEvent<HTMLAnchorElement>, sectionId: string) {
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
              {hook.manifestChanged && <span className={clsx('badge-sm', SEVERITY_BADGE.info)}>manifest changed</span>}
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
  revision1,
  revision2,
}: {
  diff?: ResourceDiff
  isLoading: boolean
  error: unknown
  left?: HelmRevision
  right?: HelmRevision
  revision1: number
  revision2: number
}) {
  const changed = diff ? diff.added.length + diff.removed.length + diff.modified.length : 0
  const identityOverlap = diff ? diff.modified.length + diff.unchanged.length : 0
  const chartChanged = Boolean(left && right && left.chart !== right.chart)
  const lowPairingConfidence = Boolean(diff && chartChanged && changed > 0 && identityOverlap === 0)
  const parseWarning = Boolean(diff?.parseErrorCount)

  return (
    <section id="resources" className="card-inner-lg scroll-mt-4">
      <SectionHeader
        icon={Link2}
        title="Rendered resources"
        description="Compact index of rendered Kubernetes objects. Use the manifest diff for exact YAML."
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
            {parseWarning && (
              <span className={clsx('badge-sm', SEVERITY_BADGE.warning)}>
                {diff.parseErrorCount} unparsed
              </span>
            )}
          </div>
        )}
      </SectionHeader>

      {isLoading ? (
        <PaneLoader label="Comparing rendered resources..." className="h-32" />
      ) : error ? (
        <ErrorState error={error} />
      ) : !diff ? (
        <EmptyDiffState label="No rendered resource changes found." />
      ) : (
        <div className="mt-3 space-y-3">
          {parseWarning && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-theme-text-secondary">
              <div className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
                <div>
                  <div className="font-medium text-theme-text-primary">Rendered resource list may be incomplete.</div>
                  <div className="mt-1">
                    Radar could not parse {diff.parseErrorCount} rendered manifest document{diff.parseErrorCount === 1 ? '' : 's'} for
                    resource grouping. Use the rendered manifest diff above as the source of truth.
                  </div>
                </div>
              </div>
            </div>
          )}
          {lowPairingConfidence && (
            <LowPairingConfidenceNotice added={diff.added} removed={diff.removed} />
          )}
          {changed === 0 ? (
            <EmptyDiffState label={parseWarning ? 'No rendered resource changes found in parsed documents.' : 'No rendered resource changes found.'} />
          ) : lowPairingConfidence ? (
            <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
              <ResourceChangeList title="Added" tone="success" resources={diff.added} initialLimit={8} />
              <ResourceChangeList title="Removed" tone="error" resources={diff.removed} initialLimit={8} />
            </div>
          ) : (
            <>
              <ModifiedResourceTable changes={diff.modified} revision1={revision1} revision2={revision2} />
              <ResourceChangeList title="Added" tone="success" resources={diff.added} />
              <ResourceChangeList title="Removed" tone="error" resources={diff.removed} />
            </>
          )}
        </div>
      )}
    </section>
  )
}

function ModifiedResourceTable({
  changes,
  revision1,
  revision2,
}: {
  changes: ResourceDiff['modified']
  revision1: number
  revision2: number
}) {
  if (changes.length === 0) return null
  return (
    <div>
      <div className="mb-2 flex items-center gap-2 text-sm font-medium text-theme-text-primary">
        <span className={clsx('badge-sm', SEVERITY_BADGE.info)}>{changes.length}</span>
        Modified in place
      </div>
      <div className="space-y-3">
        {changes.map((change) => (
          <ModifiedResourceRows key={resourceKey(change)} change={change} revision1={revision1} revision2={revision2} />
        ))}
      </div>
    </div>
  )
}

function ModifiedResourceRows({
  change,
  revision1,
  revision2,
}: {
  change: ResourceDiff['modified'][number]
  revision1: number
  revision2: number
}) {
  const explicitFields = change.fields.filter((field) => {
    const oldValue = formatDiffValue(field.oldValue, field.path)
    const newValue = formatDiffValue(field.newValue, field.path)
    return !isGenericContentChange(field, oldValue, newValue)
  })
  const visibleFields = explicitFields.slice(0, 8)
  const hiddenCount = Math.max(0, explicitFields.length - visibleFields.length) + Math.max(0, change.fieldCount - change.fields.length)

  return (
    <div className="overflow-hidden rounded-lg border border-theme-border bg-theme-base/50">
      <div className="flex min-w-0 flex-wrap items-center gap-2 border-b border-theme-border px-3 py-2">
        <span className={clsx('badge-sm shrink-0', getKindBadgeColor(change.kind))}>{change.kind}</span>
        <ResourceName resource={change} />
      </div>
      {visibleFields.length === 0 ? (
        <div className="px-3 py-2 text-xs text-theme-text-secondary">
          {change.summary && change.summary !== 'resource changed'
            ? change.summary
            : 'Rendered manifest changed; field-level summary unavailable.'}
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[720px] border-collapse text-left text-xs">
            <thead className="bg-theme-surface text-theme-text-tertiary">
              <tr>
                <th scope="col" className="w-[42%] px-3 py-2 font-medium">
                  Field
                </th>
                <th scope="col" className="w-[29%] px-3 py-2 font-medium">
                  Rev {revision1}
                </th>
                <th scope="col" className="w-[29%] px-3 py-2 font-medium">
                  Rev {revision2}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-theme-border">
              {visibleFields.map((field, index) => (
                <ModifiedFieldRow key={`${field.path}-${index}`} field={field} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {hiddenCount > 0 && (
        <div className="border-t border-theme-border px-3 py-2 text-xs text-theme-text-tertiary">
          +{hiddenCount} more changed field{hiddenCount === 1 ? '' : 's'} in the manifest diff
        </div>
      )}
    </div>
  )
}

function ModifiedFieldRow({ field }: { field: ResourceDiff['modified'][number]['fields'][number] }) {
  const oldValue = formatDiffValue(field.oldValue, field.path)
  const newValue = formatDiffValue(field.newValue, field.path)
  const oldTooltip = formatFullDiffValue(field.oldValue, field.path)
  const newTooltip = formatFullDiffValue(field.newValue, field.path)
  return (
    <tr className="align-top">
      <td className="px-3 py-2">
        <Tooltip content={field.path} wrapperClassName="min-w-0 max-w-full">
          <code className="break-words font-mono text-theme-text-secondary">{formatPathLabel(field.path)}</code>
        </Tooltip>
      </td>
      <td className="px-3 py-2 text-theme-text-secondary">
        <Tooltip content={oldTooltip} wrapperClassName="min-w-0 max-w-full">
          <span className="block break-words">{oldValue}</span>
        </Tooltip>
      </td>
      <td className="px-3 py-2 font-medium text-theme-text-primary">
        <Tooltip content={newTooltip} wrapperClassName="min-w-0 max-w-full">
          <span className="block break-words">{newValue}</span>
        </Tooltip>
      </td>
    </tr>
  )
}

function LowPairingConfidenceNotice({
  added,
  removed,
}: {
  added: ResourceDiff['added']
  removed: ResourceDiff['removed']
}) {
  return (
    <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-theme-text-secondary">
      <div className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
        <div className="min-w-0">
          <div className="font-medium text-theme-text-primary">Resource identities do not overlap.</div>
          <div className="mt-1">
            The chart changed and Radar cannot pair old and new resources by name. Use the manifest diff above for the cause; the rows below are
            identity context.
          </div>
          <div className="mt-2 grid grid-cols-1 gap-2 text-xs md:grid-cols-2">
            <div>
              <span className="font-medium text-theme-text-primary">Added:</span>{' '}
              <span>{summarizeResourceKinds(added)}</span>
            </div>
            <div>
              <span className="font-medium text-theme-text-primary">Removed:</span>{' '}
              <span>{summarizeResourceKinds(removed)}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function ResourceChangeList({
  title,
  tone,
  resources,
  initialLimit = 12,
}: {
  title: string
  tone: DiffTone
  resources: ResourceDiff['added']
  initialLimit?: number
}) {
  const [expanded, setExpanded] = useState(false)
  if (resources.length === 0) return null
  const visible = expanded ? resources : resources.slice(0, initialLimit)
  return (
    <div>
      <div className="mb-2 flex items-center gap-2 text-sm font-medium text-theme-text-primary">
        <span className={clsx('badge-sm', SEVERITY_BADGE[tone])}>{resources.length}</span>
        {title}
      </div>
      <div className="grid grid-cols-1 gap-1.5 2xl:grid-cols-2">
        {visible.map((resource) => (
          <ResourceChangeRow key={resourceKey(resource)} resource={resource} />
        ))}
      </div>
      {resources.length > initialLimit && (
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="mt-2 rounded-md px-2 py-1 text-xs font-medium text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary"
        >
          {expanded ? 'Show fewer' : `Show ${resources.length - visible.length} more`}
        </button>
      )}
    </div>
  )
}

function ResourceChangeRow({ resource }: { resource: ResourceDiff['added'][number] }) {
  return (
    <div className="flex min-w-0 items-start gap-2 rounded-md border border-theme-border bg-theme-base/50 px-2 py-1.5">
      <span className={clsx('badge-sm shrink-0', getKindBadgeColor(resource.kind))}>{resource.kind}</span>
      <ResourceName resource={resource} />
    </div>
  )
}

function ResourceName({ resource }: { resource: ResourceDiff['added'][number] }) {
  return (
    <div className="min-w-0 flex-1">
      <div className="break-words text-sm font-medium leading-snug text-theme-text-primary">{resource.name}</div>
      {resource.namespace && <div className="mt-0.5 text-xs text-theme-text-tertiary">{resource.namespace}</div>}
    </div>
  )
}

function isGenericContentChange(
  field: ResourceDiff['modified'][number]['fields'][number],
  oldValue: string,
  newValue: string,
): boolean {
  return field.path === 'resource' && oldValue === 'changed' && newValue === 'changed'
}

function summarizeResourceKinds(resources: ResourceDiff['added']): string {
  if (resources.length === 0) return 'none'
  const counts = new Map<string, number>()
  for (const resource of resources) {
    counts.set(resource.kind, (counts.get(resource.kind) || 0) + 1)
  }
  return [...counts.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([kind, count]) => `${count} ${kind}`)
    .join(', ')
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

function formatFullDiffValue(value: unknown, path?: string): string {
  if (value === null || value === undefined) return 'none'
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  const structured = formatStructuredDiffValue(value, path)
  if (structured) return structured
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
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
