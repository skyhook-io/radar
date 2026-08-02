import { useEffect, useState, type ReactNode } from 'react'
import {
  Box,
  Check,
  CircleAlert,
  CircleOff,
  Copy,
  Cpu,
  Equal,
  Eye,
  EyeOff,
  FileCode2,
  History,
  KeyRound,
  List,
  LockKeyhole,
  Minus,
  Variable,
} from 'lucide-react'
import { clsx } from 'clsx'
import type {
  PodEnvironmentResponse,
  PodEnvironmentRevealResponse,
  PodEnvironmentRow,
  PodEnvironmentSource,
} from '../../../types'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { Section, type CopyHandler } from '../../ui/drawer-components'
import { Tooltip } from '../../ui/Tooltip'

interface ContainerEnvironmentSectionProps {
  environment: PodEnvironmentResponse
  namespace: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  onReveal?: (container: string, variable: string) => Promise<PodEnvironmentRevealResponse>
  onCopy: CopyHandler
  copied: string | null
}

export function ContainerEnvironmentSection({
  environment,
  namespace,
  onNavigate,
  onReveal,
  onCopy,
  copied,
}: ContainerEnvironmentSectionProps) {
  const visibleContainers = environment.containers.filter(container => container.rows.length > 0 || container.truncated)
  const defaultContainerName = (visibleContainers.find(container => container.role === 'container') ?? visibleContainers[0])?.name ?? ''
  const [selected, setSelected] = useState(defaultContainerName)
  const [revealed, setRevealed] = useState<Record<string, PodEnvironmentRevealResponse>>({})
  const [revealing, setRevealing] = useState<Record<string, boolean>>({})
  const [revealErrors, setRevealErrors] = useState<Record<string, string>>({})

  useEffect(() => {
    if (!visibleContainers.some(container => container.name === selected))
      setSelected(defaultContainerName)
  }, [defaultContainerName, selected, visibleContainers])

  useEffect(() => {
    setRevealed({})
    setRevealErrors({})
  }, [environment])

  const active = visibleContainers.find(container => container.name === selected)
    ?? visibleContainers.find(container => container.name === defaultContainerName)
  const total = visibleContainers.reduce(
    (sum, container) => sum + container.rows.filter(row => !row.placeholder).length,
    0,
  )
  const unavailableSources = visibleContainers.reduce(
    (sum, container) => sum + container.rows.filter(row => row.placeholder).length,
    0,
  )
  const secretBacked = visibleContainers.reduce(
    (sum, container) => sum + container.rows.filter(row => row.sensitive && !row.placeholder).length,
    0,
  )
  const subtitle = total + ' variable' + (total === 1 ? '' : 's')
    + (secretBacked ? ' · ' + secretBacked + ' from Secrets' : '')
    + (unavailableSources ? ' · ' + unavailableSources + ' source' + (unavailableSources === 1 ? '' : 's') + ' unavailable' : '')

  if (!active) return null
  const showStatus = active.rows.some(hasEnvironmentStatus)

  const reveal = async (row: PodEnvironmentRow) => {
    const key = rowKey(active.name, row.name)
    if (revealed[key]) {
      setRevealed(current => withoutKey(current, key))
      return
    }
    if (!onReveal) return
    setRevealing(current => ({ ...current, [key]: true }))
    setRevealErrors(current => withoutKey(current, key))
    try {
      const value = await onReveal(active.name, row.name)
      setRevealed(current => ({ ...current, [key]: value }))
    } catch (error) {
      setRevealErrors(current => ({ ...current, [key]: error instanceof Error ? error.message : 'Reveal failed' }))
    } finally {
      setRevealing(current => withoutKey(current, key))
    }
  }

  return (
    <Section title={'Environment Variables  ·  ' + subtitle} icon={List} defaultExpanded={false} contentClassName="pl-2">
      <div className="@container/env min-w-0 space-y-3">
        <p className="text-xs text-theme-text-tertiary">
          Shows values from the Pod's current configuration and where they come from. Secret values stay hidden until you reveal them.
        </p>
        <p className="text-xs text-theme-text-tertiary">
          <Tooltip content="The cluster can add networking-related variables when the container starts. They are not stored on the Pod, so Radar cannot show them." position="right">
            <span className="cursor-help border-b border-dotted border-theme-text-tertiary" tabIndex={0}>Only variables declared on the Pod are shown.</span>
          </Tooltip>
        </p>

        {visibleContainers.length > 1 && (
          <div className="flex flex-wrap gap-1.5" role="tablist" aria-label="Container">
            {visibleContainers.map(container => (
              <button
                key={container.name}
                type="button"
                role="tab"
                aria-selected={container.name === active.name}
                onClick={() => {
                  setSelected(container.name)
                  setRevealed({})
                  setRevealErrors({})
                }}
                className={clsx(
                  'rounded-md border px-2.5 py-1 text-xs transition-colors',
                  container.name === active.name
                    ? 'selection selection-text selection-ring border-transparent'
                    : 'border-theme-border text-theme-text-secondary hover:bg-theme-hover',
                )}
              >
                {container.name}
                {container.role !== 'container' && ' · ' + container.role}
              </button>
            ))}
          </div>
        )}

        <div className="overflow-hidden rounded-lg border border-theme-border">
          <table className="w-full table-fixed text-left text-xs">
            <colgroup>
              <col className={showStatus ? 'w-[42%] @min-[760px]/env:w-[26%]' : 'w-[46%] @min-[760px]/env:w-[30%]'} />
              <col className={showStatus ? 'w-[36%] @min-[760px]/env:w-[30%]' : 'w-[42%] @min-[760px]/env:w-[38%]'} />
              <col className={showStatus ? 'w-[11%] @min-[760px]/env:w-[24%]' : 'w-[12%] @min-[760px]/env:w-[32%]'} />
              {showStatus && <col className="w-[11%] @min-[760px]/env:w-[20%]" />}
            </colgroup>
            <thead className="bg-theme-elevated text-theme-text-tertiary">
              <tr>
                <th className="px-2 py-1.5 font-medium @min-[760px]/env:px-3 @min-[760px]/env:py-2">Variable</th>
                <th className="px-2 py-1.5 font-medium @min-[760px]/env:px-3 @min-[760px]/env:py-2">
                  <ExplainedLabel label="Value" explanation="The value in the Pod's current configuration. A running container may still have an older value if its ConfigMap or Secret changed after it started." />
                </th>
                <th className="px-1 py-1.5 text-center font-medium @min-[760px]/env:px-3 @min-[760px]/env:py-2 @min-[760px]/env:text-left">
                  <ExplainedLabel label="Source" explanation="Where this variable gets its value." />
                </th>
                {showStatus && (
                  <th className="px-1 py-1.5 text-center font-medium @min-[760px]/env:px-3 @min-[760px]/env:py-2 @min-[760px]/env:text-left">
                    <ExplainedLabel label="Status" explanation="A change since the container started, or a problem reading this value." />
                  </th>
                )}
              </tr>
            </thead>
            <tbody className="table-divide-subtle">
              {active.rows.map(row => {
                const key = rowKey(active.name, row.name)
                const revealedValue = revealed[key]
                return (
                  <tr key={row.name} className="align-top">
                    <td className="min-w-0 break-all px-2 py-1.5 font-mono text-theme-text-primary @min-[760px]/env:px-3 @min-[760px]/env:py-2">
                      {row.placeholder ? 'All variables from this source' : row.name}
                    </td>
                    <td className="min-w-0 px-2 py-1.5 font-mono text-theme-text-primary @min-[760px]/env:px-3 @min-[760px]/env:py-2">
                      <ValueCell
                        row={row}
                        revealed={revealedValue}
                        revealing={revealing[key] === true}
                        revealError={revealErrors[key]}
                        onReveal={() => reveal(row)}
                        onCopy={() => onCopy(revealedValue?.value ?? row.value ?? '', key)}
                        copied={copied === key}
                        revealEnabled={Boolean(onReveal)}
                      />
                    </td>
                    <td className="min-w-0 px-1 py-1.5 @min-[760px]/env:px-3 @min-[760px]/env:py-2">
                      <div className="flex justify-center @min-[760px]/env:hidden">
                        <CompactSourceCell row={row} namespace={namespace} onNavigate={onNavigate} />
                      </div>
                      <div className="hidden @min-[760px]/env:block">
                        <SourceCell row={row} namespace={namespace} onNavigate={onNavigate} />
                      </div>
                    </td>
                    {showStatus && (
                      <td className="min-w-0 px-1 py-1.5 @min-[760px]/env:px-3 @min-[760px]/env:py-2">
                        <div className="flex justify-center @min-[760px]/env:hidden">
                          <CompactStatusCell row={row} />
                        </div>
                        <div className="hidden @min-[760px]/env:block">
                          <StatusCell row={row} />
                        </div>
                      </td>
                    )}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>

        {active.truncated && (
          <p className="text-xs text-warning-text">Some variables in this container were omitted.</p>
        )}

        {environment.partial && <p className="text-xs text-warning-text">Some sources could not be read.</p>}
        {environment.truncated && <p className="text-xs text-warning-text">This Pod has more sources or variables than Radar can show at once.</p>}
        {environment.coverage.degraded && (
          <p className="text-xs text-warning-text">{environment.coverage.degradedReason || 'Recent changes could not be checked.'}</p>
        )}
        {environment.coverage.saturated && <p className="text-xs text-warning-text">Some recent changes may not be shown.</p>}
      </div>
    </Section>
  )
}

function ValueCell({
  row,
  revealed,
  revealing,
  revealError,
  onReveal,
  onCopy,
  copied,
  revealEnabled,
}: {
  row: PodEnvironmentRow
  revealed?: PodEnvironmentRevealResponse
  revealing: boolean
  revealError?: string
  onReveal: () => void
  onCopy: () => void
  copied: boolean
  revealEnabled: boolean
}) {
  if (row.runtimeDependent) {
    const explanation = row.message || 'The final value is decided when the container starts.'
    return (
      <Tooltip content={row.value ? `${explanation} Pod configuration: ${row.value}` : explanation} position="top">
        <span className="cursor-help border-b border-dotted border-theme-text-tertiary text-theme-text-tertiary" tabIndex={0}>Set at startup</span>
      </Tooltip>
    )
  }
  if (row.state === 'missing') {
    if (row.optional) {
      return (
        <Tooltip content={row.message || 'This optional variable is not set.'} position="top">
          <span className="cursor-help border-b border-dotted border-theme-text-tertiary text-theme-text-tertiary" tabIndex={0}>Not set</span>
        </Tooltip>
      )
    }
    return <span className="text-theme-text-tertiary">—</span>
  }
  if (row.sensitive) {
    return (
      <div className="space-y-1">
        <div className="group/value flex items-center gap-1.5">
          <span className="break-all">{revealed ? revealed.value : '••••••••'}</span>
          {row.state === 'masked' && !row.runtimeDependent && !row.placeholder && row.evidence?.kind !== 'removed' && (
            <button
              type="button"
              onClick={onReveal}
              disabled={!revealEnabled || revealing}
              className="text-theme-text-tertiary transition-colors hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-40"
              aria-label={revealed ? 'Hide value' : 'Reveal value'}
            >
              <Tooltip content={revealed ? 'Hide Secret value' : 'Reveal this Secret value'} delay={150}>
                {revealed ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
              </Tooltip>
            </button>
          )}
          {revealed && (
            <ValueCopyButton onCopy={onCopy} copied={copied} />
          )}
          {revealing && <span className="text-theme-text-tertiary">Loading…</span>}
        </div>
        {revealed?.encoding === 'base64' && <div className="text-theme-text-tertiary">Base64-encoded binary value</div>}
        {revealError && <div className="text-theme-text-secondary">{revealError}</div>}
      </div>
    )
  }
  if (row.state !== 'resolved' && !row.value) return <span className="text-theme-text-tertiary">—</span>
  return (
    <div className="group/value flex min-w-0 items-center gap-1.5">
      <span className="min-w-0 break-all">{row.value ?? ''}</span>
      <ValueCopyButton onCopy={onCopy} copied={copied} />
    </div>
  )
}

function ValueCopyButton({ onCopy, copied }: { onCopy: () => void; copied: boolean }) {
  return (
    <button
      type="button"
      onClick={onCopy}
      className={clsx(
        'shrink-0 text-theme-text-tertiary transition-[color,opacity] hover:text-theme-text-primary focus-visible:opacity-100',
        copied ? 'opacity-100' : 'opacity-0 group-hover/value:opacity-100 group-focus-within/value:opacity-100',
      )}
      aria-label={copied ? 'Value copied' : 'Copy value'}
    >
      <Tooltip content={copied ? 'Copied' : 'Copy value'} delay={150}>
        {copied
          ? <Check className="h-3.5 w-3.5 text-green-400" aria-hidden />
          : <Copy className="h-3.5 w-3.5" aria-hidden />}
      </Tooltip>
    </button>
  )
}

function CompactSourceCell({
  row,
  namespace,
  onNavigate,
}: {
  row: PodEnvironmentRow
  namespace: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}) {
  const source = row.source
  const navigate = (source.kind === 'Secret' || source.kind === 'ConfigMap') && source.name && onNavigate
    ? () => onNavigate({ kind: source.kind, namespace, name: source.name! })
    : undefined
  const kind = source.kind === 'Secret' || source.kind === 'ConfigMap' || source.kind === 'Pod'
    ? source.kind
    : undefined
  const badge = (
    <Badge
      kind={kind}
      tone={kind ? undefined : 'structural'}
      size="sm"
      className="!px-1"
      onClick={navigate}
    >
      {compactSourceIcon(source.kind)}
      <span className="sr-only">{navigate ? `View ${source.kind}/${source.name}` : source.kind}</span>
    </Badge>
  )

  return (
    <Tooltip content={compactSourceExplanation(row, Boolean(navigate))} delay={150} position="top">
      <span className={clsx('inline-flex', navigate ? 'cursor-pointer' : '!cursor-help')} tabIndex={navigate ? undefined : 0}>{badge}</span>
    </Tooltip>
  )
}

function compactSourceIcon(kind: string) {
  const className = 'h-3.5 w-3.5'
  if (kind === 'Secret') return <KeyRound className={className} aria-hidden />
  if (kind === 'ConfigMap') return <FileCode2 className={className} aria-hidden />
  if (kind === 'Pod') return <Box className={className} aria-hidden />
  if (kind === 'Container resources') return <Cpu className={className} aria-hidden />
  if (kind === 'Variable') return <Variable className={className} aria-hidden />
  return <Equal className={className} aria-hidden />
}

function compactSourceExplanation(row: PodEnvironmentRow, canNavigate: boolean) {
  let explanation = describeSource(row.source)
  if ((row.dependencies?.length ?? 0) > 0) {
    explanation += ' It also uses ' + formatSources(row.dependencies!) + '.'
  }
  if ((row.shadowedSources?.length ?? 0) > 0) {
    explanation += ' Overrides earlier ' + formatSources(row.shadowedSources!) + '.'
  }
  if (canNavigate) {
    explanation += ` Click to view the ${row.source.kind}.`
  }
  return explanation
}

function SourceCell({
  row,
  namespace,
  onNavigate,
}: {
  row: PodEnvironmentRow
  namespace: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}) {
  const dependencies = row.dependencies ?? []
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
      <SourceLabel source={row.source} namespace={namespace} onNavigate={onNavigate} />
      {dependencies.length > 0 && <span className="text-theme-text-tertiary">using</span>}
      {dependencies.map((dependency, index) => (
        <SourceLabel
          key={dependency.kind + '\u0000' + dependency.name + '\u0000' + dependency.key + '\u0000' + index}
          source={dependency}
          namespace={namespace}
          onNavigate={onNavigate}
        />
      ))}
      {(row.shadowedSources?.length ?? 0) > 0 && (
        <Tooltip content={'Earlier source' + (row.shadowedSources!.length === 1 ? ': ' : 's: ') + formatSources(row.shadowedSources!)}>
          <span className="cursor-help border-b border-dotted border-theme-text-tertiary text-theme-text-tertiary" tabIndex={0}>
            overrides {row.shadowedSources!.length} earlier source{row.shadowedSources!.length === 1 ? '' : 's'}
          </span>
        </Tooltip>
      )}
    </div>
  )
}

function SourceLabel({
  source,
  namespace,
  onNavigate,
}: {
  source: PodEnvironmentSource
  namespace: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}) {
  if ((source.kind === 'Secret' || source.kind === 'ConfigMap') && source.name) {
    const explanation = describeSource(source)
    const navigationHint = onNavigate ? ` Click to view the ${source.kind}.` : ''
    return (
      <span className="inline-flex min-w-0 max-w-full flex-wrap items-center gap-x-1.5 gap-y-0.5">
        <Tooltip content={explanation + navigationHint} position="top" wrapperClassName="min-w-0">
          <span className={clsx('inline-flex min-w-0 max-w-full', onNavigate ? 'cursor-pointer' : '!cursor-help')} tabIndex={onNavigate ? undefined : 0}>
            <Badge
              kind={source.kind}
              size="sm"
              className="max-w-full"
              onClick={onNavigate ? () => onNavigate({ kind: source.kind, namespace, name: source.name! }) : undefined}
            >
              <span className="min-w-0 truncate">{source.kind}/{source.name}</span>
            </Badge>
          </span>
        </Tooltip>
        {source.key && <span className="min-w-0 max-w-full break-all font-mono text-theme-text-tertiary">{source.key}</span>}
      </span>
    )
  }
  if (source.kind === 'Variable' && source.variable) {
    return (
      <Tooltip content={`This value uses the earlier ${source.variable} environment variable.`}>
        <span className="cursor-help border-b border-dotted border-theme-text-tertiary font-mono text-theme-text-secondary" tabIndex={0}>{source.variable}</span>
      </Tooltip>
    )
  }
  const label = source.kind === 'Direct'
    ? 'Set directly'
    : source.kind === 'Pod'
      ? 'Pod'
      : source.kind === 'Container resources'
        ? 'Container resources'
        : source.kind
  const explanation = describeSource(source)
  return (
    <Tooltip content={explanation}>
      <span className="cursor-help border-b border-dotted border-theme-text-tertiary text-theme-text-secondary" tabIndex={0}>{label}</span>
    </Tooltip>
  )
}

function describeSource(source: PodEnvironmentSource) {
  if (source.kind === 'Direct') return 'Set directly in this container\'s configuration.'
  if (source.kind === 'Secret') {
    return `Secret/${source.name} stores sensitive configuration${source.key ? `; this value uses its ${source.key} key` : ''}. The value stays hidden until you reveal it.`
  }
  if (source.kind === 'ConfigMap') {
    return `ConfigMap/${source.name} stores non-sensitive configuration${source.key ? `; this value uses its ${source.key} key` : ''}.`
  }
  if (source.kind === 'Pod') {
    return `Comes from the Pod itself${source.key ? ` (${source.key})` : ''}, such as its name, namespace, IP, or assigned node. The running container read this value when it started.`
  }
  if (source.kind === 'Container resources') {
    return `Calculated from a container's CPU or memory request or limit${source.key ? ` (${source.key})` : ''}. The running container read this value when it started.`
  }
  if (source.kind === 'Variable') return `Uses the earlier ${source.variable} environment variable.`
  return `Value source: ${source.kind}.`
}

function formatSources(sources: PodEnvironmentSource[]) {
  return sources.map(source => source.kind
    + (source.name ? '/' + source.name : source.variable ? '/' + source.variable : '')
    + (source.key ? '[' + source.key + ']' : '')).join(', ')
}

function CompactStatusCell({ row }: { row: PodEnvironmentRow }) {
  if (row.state === 'missing' && !row.optional) {
    const { label, explanation } = missingStatus(row)
    const severity = row.missingImpact === 'restartBlocked' ? 'warning' : 'error'
    return <CompactStatusBadge label={label} explanation={explanation} severity={severity} icon={<CircleAlert className="h-3.5 w-3.5" aria-hidden />} />
  }
  if (row.evidence) {
    const label = row.evidence.kind === 'added'
      ? 'Added after start'
      : row.evidence.kind === 'removed'
        ? 'Removed after start'
        : 'Changed after start'
    return <CompactStatusBadge label={label} explanation={row.evidence.message} severity="warning" icon={<History className="h-3.5 w-3.5" aria-hidden />} />
  }
  if (row.state === 'denied') return <CompactStatusBadge label="Access needed" explanation={row.message} severity="info" icon={<LockKeyhole className="h-3.5 w-3.5" aria-hidden />} />
  if (row.runtimeDependent) return <CompactStatusBadge label="At startup" explanation={row.message} severity="neutral" icon={<Minus className="h-3.5 w-3.5" aria-hidden />} />
  if (row.state === 'missing' && row.optional) return <CompactStatusBadge label="Not set" explanation={row.message} severity="neutral" icon={<Minus className="h-3.5 w-3.5" aria-hidden />} />
  if (row.state === 'unavailable') return <CompactStatusBadge label="Unavailable" explanation={row.message} severity="neutral" icon={<CircleOff className="h-3.5 w-3.5" aria-hidden />} />
  return null
}

function CompactStatusBadge({
  label,
  explanation,
  severity,
  icon,
}: {
  label: string
  explanation?: string
  severity?: BadgeSeverity
  icon: ReactNode
}) {
  return (
    <Tooltip content={explanation || label} delay={150} position="left">
      <span className="inline-flex cursor-help" tabIndex={0}>
        <Badge severity={severity} size="sm" className="!px-1">
          {icon}
          <span className="sr-only">{label}</span>
        </Badge>
      </span>
    </Tooltip>
  )
}

function StatusCell({ row }: { row: PodEnvironmentRow }) {
  if (row.state === 'missing' && !row.optional) {
    const { label, explanation } = missingStatus(row)
    const severity = row.missingImpact === 'restartBlocked' ? 'warning' : 'error'
    return <StatusBadge explanation={explanation}><Badge severity={severity} size="sm">{label}</Badge></StatusBadge>
  }
  if (row.evidence) {
    const label = row.evidence.kind === 'added'
      ? 'Added after start'
      : row.evidence.kind === 'removed'
        ? 'Removed after start'
        : 'Changed after start'
    return <StatusBadge explanation={row.evidence.message}><Badge severity="warning" size="sm">{label}</Badge></StatusBadge>
  }
  if (row.state === 'denied') return <StatusBadge explanation={row.message}><Badge severity="info" size="sm">Access needed</Badge></StatusBadge>
  if (row.runtimeDependent) return <StatusBadge explanation={row.message}><Badge severity="neutral" size="sm">At startup</Badge></StatusBadge>
  if (row.state === 'missing' && row.optional) return <StatusBadge explanation={row.message}><Badge severity="neutral" size="sm">Not set</Badge></StatusBadge>
  if (row.state === 'unavailable') return <StatusBadge explanation={row.message}><Badge severity="neutral" size="sm">Unavailable</Badge></StatusBadge>
  return null
}

function missingStatus(row: PodEnvironmentRow) {
  const label = row.missingImpact === 'restartBlocked' ? 'Restart blocked' : 'Prevents start'
  const explanation = [row.message, row.evidence?.message].filter(Boolean).join(' ')
  return { label, explanation }
}

function hasEnvironmentStatus(row: PodEnvironmentRow) {
  return Boolean(row.evidence)
    || row.state === 'denied'
    || (row.state === 'missing' && !row.optional)
    || (row.state === 'unavailable' && !row.runtimeDependent)
}

function StatusBadge({ explanation, children }: { explanation?: string; children: ReactNode }) {
  return (
    <Tooltip content={explanation} position="left" disabled={!explanation}>
      <span className={clsx(explanation && 'cursor-help')} tabIndex={explanation ? 0 : undefined}>{children}</span>
    </Tooltip>
  )
}

function ExplainedLabel({ label, explanation }: { label: string; explanation: string }) {
  return (
    <Tooltip content={explanation}>
      <span className="cursor-help border-b border-dotted border-theme-text-tertiary" tabIndex={0}>{label}</span>
    </Tooltip>
  )
}

function rowKey(container: string, variable: string) {
  return container + '\u0000' + variable
}

function withoutKey<T>(record: Record<string, T>, key: string) {
  const next = { ...record }
  delete next[key]
  return next
}
