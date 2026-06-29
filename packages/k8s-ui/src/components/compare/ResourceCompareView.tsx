import { useMemo, useRef, useState } from 'react'
import { clsx } from 'clsx'
import { ArrowLeftRight, GitCompare, Rows, FileText, FileCode2, X, Pencil, AlertTriangle, Sparkles } from 'lucide-react'
import { YamlDiffEditor } from '../ui/YamlEditor'
import { PaneLoader } from '../ui/PaneLoader'
import { Tooltip } from '../ui/Tooltip'
import { pluralToKind } from '../../utils/navigation'
import { toComparableYaml } from './normalize'
import { SIDE_TONES, type CompareSide } from './types'

export type { CompareSide }

export interface CompareResourceRef {
  kind: string
  namespace: string
  name: string
  group?: string
}

export interface CompareSideError {
  side: CompareSide
  message: string
}

export interface ResourceCompareViewProps {
  a: CompareResourceRef
  b: CompareResourceRef
  aData: unknown
  bData: unknown
  /** Per-side fetch errors. Each side renders independently — a working side stays useful. */
  errors?: CompareSideError[]
  /** Caller-supplied theme passthrough for the Monaco editor. */
  editorTheme?: 'vs-dark' | 'vs'
  onSwap: () => void
  onClose: () => void
  /** Optional — when provided, the resource pill shows a pencil button to re-pick. */
  onChangeA?: () => void
  onChangeB?: () => void
  /**
   * Optional small label rendered after the resource name in each pill.
   * Used by cross-cluster compare to surface the cluster identity for
   * each side; single-cluster compare leaves these undefined.
   */
  aSubtitle?: string
  bSubtitle?: string
  /**
   * When true, the diff editor area drops its padding + rounded border.
   * Use when the host renders compare as a full-bleed page (the diff IS
   * the page). Default false preserves the inset-card look that works
   * inside OSS Radar's narrower compare route.
   */
  bleed?: boolean
}

function ResourcePill({
  resource,
  side,
  error,
  onChange,
  subtitle,
}: {
  resource: CompareResourceRef
  side: CompareSide
  error?: string
  onChange?: () => void
  subtitle?: string
}) {
  const tones = SIDE_TONES[side]
  const errTone = 'border-red-400/50 bg-red-500/10'
  const label = side === 'a' ? 'A' : 'B'
  const full = resource.namespace ? `${resource.namespace}/${resource.name}` : resource.name
  return (
    <div
      className={clsx(
        'group flex items-center gap-2 pl-1.5 pr-2 py-1 rounded-lg border text-xs font-mono min-w-0 max-w-[18rem]',
        error ? errTone : clsx(tones.containerBorder, tones.containerBg),
      )}
      title={error ? `${full} — ${error}` : full}
    >
      <span className={clsx('inline-flex items-center justify-center w-4 h-4 rounded text-[10px] font-bold leading-none shrink-0', error ? 'bg-red-400/90 text-red-950' : tones.chipBg)}>
        {label}
      </span>
      {error && <AlertTriangle className="w-3 h-3 text-red-400 shrink-0" aria-hidden />}
      <span className={clsx('truncate min-w-0', error ? 'text-red-300' : 'text-theme-text-primary')}>
        {resource.namespace && <span className="opacity-60">{resource.namespace}/</span>}
        {resource.name}
        {subtitle && (
          <span className="opacity-60 ml-1" aria-label={`Source: ${subtitle}`}>
            · {subtitle}
          </span>
        )}
      </span>
      {onChange && (
        <Tooltip content="Pick a different resource">
          <button
            onClick={onChange}
            className="shrink-0 p-0.5 rounded text-theme-text-tertiary opacity-0 group-hover:opacity-100 focus:opacity-100 hover:text-theme-text-primary hover:bg-theme-elevated/70 transition-opacity"
          >
            <Pencil className="w-3 h-3" />
          </button>
        </Tooltip>
      )}
    </div>
  )
}

export function ResourceCompareView({
  a,
  b,
  aData,
  bData,
  errors,
  editorTheme = 'vs-dark',
  onSwap,
  onClose,
  onChangeA,
  onChangeB,
  aSubtitle,
  bSubtitle,
  bleed = false,
}: ResourceCompareViewProps) {
  const [specOnly, setSpecOnly] = useState(false)
  const [unified, setUnified] = useState(false)
  const [hideUnchanged, setHideUnchanged] = useState(true)
  const [rawMetadata, setRawMetadata] = useState(false)

  const aYaml = useMemo(() => (aData ? toComparableYaml(aData, { specOnly, rawMetadata }) : ''), [aData, specOnly, rawMetadata])
  const bYaml = useMemo(() => (bData ? toComparableYaml(bData, { specOnly, rawMetadata }) : ''), [bData, specOnly, rawMetadata])

  const identical = aYaml && bYaml && aYaml === bYaml
  const kindLabel = pluralToKind(a.kind)
  const aError = errors?.find(e => e.side === 'a')?.message
  const bError = errors?.find(e => e.side === 'b')?.message
  const anyError = !!(aError || bError)

  // Settle gate, keyed on the rendered A/B identity. Mount the diff
  // editor only after both sides have first settled (data or error) for
  // the current pair. Once settled, Monaco stays mounted across re-picks
  // — clearing a side's data transiently during a refetch must not
  // blank the editor. When the A/B identity changes (navigating to a
  // new pair on the same route), the latch resets so the loader shows
  // again until the new fetches settle.
  const settleKey = `${a.kind}|${a.namespace}|${a.name}|${a.group ?? ''}|${aSubtitle ?? ''}` +
    `|${b.kind}|${b.namespace}|${b.name}|${b.group ?? ''}|${bSubtitle ?? ''}`
  const settleKeyRef = useRef(settleKey)
  const hasSettledRef = useRef(false)
  if (settleKeyRef.current !== settleKey) {
    settleKeyRef.current = settleKey
    hasSettledRef.current = false
  }
  const bothSettled = (!!aData || !!aError) && (!!bData || !!bError)
  if (bothSettled) hasSettledRef.current = true
  const showInitialLoader = !hasSettledRef.current && !bothSettled

  return (
    <div className="flex-1 min-w-0 flex flex-col h-full bg-theme-base">
      <div className="h-0.5 w-full bg-gradient-to-r from-blue-400/70 via-skyhook-400/40 to-emerald-400/70" />

      <div className="flex items-center gap-3 px-4 py-2.5 border-b border-theme-border bg-theme-surface">
        <GitCompare className="w-5 h-5 text-skyhook-400 shrink-0" />
        <h2 className="text-sm font-semibold text-theme-text-primary shrink-0 whitespace-nowrap">
          Compare <span className="text-theme-text-tertiary mx-0.5">·</span>{' '}
          <span className="text-theme-text-secondary font-medium">{kindLabel}</span>
        </h2>

        <div className="flex items-center gap-2 min-w-0 flex-1">
          <ResourcePill resource={a} side="a" error={aError} onChange={onChangeA} subtitle={aSubtitle} />
          <Tooltip content="Swap A and B">
            <button
              onClick={onSwap}
              className="shrink-0 p-1.5 rounded-lg text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated transition-colors"
            >
              <ArrowLeftRight className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
          <ResourcePill resource={b} side="b" error={bError} onChange={onChangeB} subtitle={bSubtitle} />
        </div>

        <div className="flex items-center gap-1 shrink-0">
          <ToggleButton active={rawMetadata} onClick={() => setRawMetadata(v => !v)} icon={<Sparkles className="w-3.5 h-3.5" />} label="Raw metadata" tooltip="Keep server-assigned noise (uid, resourceVersion, managedFields, last-applied)" />
          <ToggleButton active={specOnly} onClick={() => setSpecOnly(v => !v)} icon={<FileCode2 className="w-3.5 h-3.5" />} label="Spec only" tooltip="Drop status fields from both sides" />
          <ToggleButton active={hideUnchanged} onClick={() => setHideUnchanged(v => !v)} icon={<FileText className="w-3.5 h-3.5" />} label="Diff only" tooltip="Collapse unchanged regions" />
          <ToggleButton active={unified} onClick={() => setUnified(v => !v)} icon={<Rows className="w-3.5 h-3.5" />} label="Unified" tooltip="Switch between side-by-side and single-column" />
        </div>

        <div className="w-px h-5 bg-theme-border shrink-0" aria-hidden />

        <Tooltip content="Close">
          <button
            onClick={onClose}
            className="shrink-0 p-1.5 rounded-lg text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </Tooltip>
      </div>

      {anyError && (
        <div className="px-4 py-2 text-xs text-red-400 bg-red-500/10 border-b border-red-500/20 flex items-center gap-2">
          <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
          <span className="min-w-0">
            {aError && bError ? (
              <>
                Failed to load both sides — <span className="font-mono">A: {aError}</span>{' · '}<span className="font-mono">B: {bError}</span>
              </>
            ) : (
              <>
                Failed to load side {aError ? 'A' : 'B'}: <span className="font-mono">{aError || bError}</span>
              </>
            )}
            {(onChangeA || onChangeB) && ' — use the pencil icon on the affected pill to pick a different resource.'}
          </span>
        </div>
      )}

      {identical && !anyError && (
        <div className="px-4 py-2 text-xs text-emerald-400 bg-emerald-500/10 border-b border-emerald-500/20 flex items-center gap-2">
          <GitCompare className="w-3.5 h-3.5" />
          These resources are identical{specOnly ? ' (spec only)' : ''}.
        </div>
      )}

      <div className={clsx('flex-1 min-h-0', bleed ? '' : 'p-3')}>
        {showInitialLoader ? (
          <PaneLoader label="Loading resources…" className="h-full" />
        ) : (
          <YamlDiffEditor
            original={aYaml}
            modified={bYaml}
            unified={unified}
            hideUnchanged={hideUnchanged && !identical}
            theme={editorTheme}
            height="100%"
            bleed={bleed}
          />
        )}
      </div>
    </div>
  )
}

function ToggleButton({
  active,
  onClick,
  icon,
  label,
  tooltip,
}: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
  tooltip: string
}) {
  return (
    <Tooltip content={tooltip}>
      <button
        onClick={onClick}
        aria-pressed={active}
        className={clsx(
          'flex items-center gap-1.5 px-2.5 py-1.5 text-xs font-medium rounded-lg border transition-colors whitespace-nowrap',
          active
            ? 'border-skyhook-400/50 bg-skyhook-500/15 text-skyhook-300'
            : 'border-transparent text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated',
        )}
      >
        {icon}
        {label}
      </button>
    </Tooltip>
  )
}
