import { useEffect, useState } from 'react'
import { Copy, Eye, EyeOff, List } from 'lucide-react'
import { clsx } from 'clsx'
import type {
  PodEnvironmentResponse,
  PodEnvironmentRevealResponse,
  PodEnvironmentRow,
  PodEnvironmentSource,
} from '../../../types'
import { Badge } from '../../ui/Badge'
import { Section, type CopyHandler } from '../../ui/drawer-components'

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
  const [selected, setSelected] = useState(visibleContainers[0]?.name ?? '')
  const [revealed, setRevealed] = useState<Record<string, PodEnvironmentRevealResponse>>({})
  const [revealing, setRevealing] = useState<Record<string, boolean>>({})
  const [revealErrors, setRevealErrors] = useState<Record<string, string>>({})

  useEffect(() => {
    if (!visibleContainers.some(container => container.name === selected))
      setSelected(visibleContainers[0]?.name ?? '')
  }, [selected, visibleContainers])

  useEffect(() => {
    setRevealed({})
    setRevealErrors({})
  }, [environment])

  const active = visibleContainers.find(container => container.name === selected) ?? visibleContainers[0]
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
    <Section title={'Environment Variables  ·  ' + subtitle} icon={List} defaultExpanded={false}>
      <div className="space-y-3">
        <p className="text-xs text-theme-text-tertiary">
          Shows where each value comes from. Secret values stay hidden until you reveal them.
        </p>
        <p className="text-xs text-theme-text-tertiary">
          Values added automatically when a container starts are not listed.
        </p>

        {environment.coverage.observedSince && (
          <p className="text-xs text-theme-text-tertiary">
            Changes observed since {formatObservedSince(environment.coverage.observedSince)}.
            {environment.coverage.degraded && ' Earlier history may be unavailable.'}
            {environment.coverage.saturated && ' Some older changes were omitted.'}
          </p>
        )}

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

        <div className="overflow-x-auto rounded-lg border border-theme-border">
          <table className="w-full min-w-[680px] text-left text-xs">
            <thead className="bg-theme-elevated text-theme-text-tertiary">
              <tr>
                <th className="px-3 py-2 font-medium">Variable</th>
                <th className="px-3 py-2 font-medium">Value if restarted now</th>
                <th className="px-3 py-2 font-medium">Source</th>
                <th className="px-3 py-2 font-medium">Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-theme-border">
              {active.rows.map(row => {
                const key = rowKey(active.name, row.name)
                const revealedValue = revealed[key]
                return (
                  <tr key={row.name} className="align-top">
                    <td className="px-3 py-2 font-mono text-theme-text-primary">
                      {row.placeholder ? 'All variables from this source' : row.name}
                    </td>
                    <td className="max-w-sm px-3 py-2 font-mono text-theme-text-primary">
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
                    <td className="px-3 py-2">
                      <SourceCell row={row} namespace={namespace} onNavigate={onNavigate} />
                    </td>
                    <td className="px-3 py-2">
                      <StatusCell row={row} />
                    </td>
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
  if (row.sensitive) {
    return (
      <div className="space-y-1">
        <div className="flex items-center gap-1.5">
          <span className="break-all">{revealed ? revealed.value : '••••••••'}</span>
          {row.state === 'masked' && !row.runtimeDependent && !row.placeholder && row.evidence?.kind !== 'removed' && (
            <button
              type="button"
              onClick={onReveal}
              disabled={!revealEnabled || revealing}
              className="text-theme-text-tertiary transition-colors hover:text-theme-text-primary disabled:cursor-not-allowed disabled:opacity-40"
              title={revealed ? 'Hide value' : 'Reveal value'}
            >
              {revealed ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
            </button>
          )}
          {revealed && (
            <button
              type="button"
              onClick={onCopy}
              className="text-theme-text-tertiary transition-colors hover:text-theme-text-primary"
              title="Copy value"
            >
              <Copy className="h-3.5 w-3.5" />
              <span className="sr-only">{copied ? 'Copied' : 'Copy value'}</span>
            </button>
          )}
          {revealing && <span className="text-theme-text-tertiary">Loading…</span>}
        </div>
        {revealed?.encoding === 'base64' && <div className="text-theme-text-tertiary">Base64-encoded binary value</div>}
        {revealError && <div className="text-theme-text-secondary">{revealError}</div>}
      </div>
    )
  }
  if (row.state !== 'resolved' && !row.value) return <span className="text-theme-text-tertiary">—</span>
  return <span className="break-all">{row.value ?? ''}</span>
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
    <div className="flex flex-wrap items-center gap-1.5">
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
        <span className="text-theme-text-tertiary" title={formatSources(row.shadowedSources!)}>
          overrides {row.shadowedSources!.length} earlier source{row.shadowedSources!.length === 1 ? '' : 's'}
        </span>
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
    return (
      <span className="inline-flex min-w-0 flex-col items-start gap-0.5">
        <Badge
          kind={source.kind}
          size="sm"
          onClick={onNavigate ? () => onNavigate({ kind: source.kind, namespace, name: source.name! }) : undefined}
          title={onNavigate ? 'View ' + source.kind + '/' + source.name : undefined}
        >
          {source.kind}/{source.name}
        </Badge>
        {source.key && <span className="max-w-full break-all font-mono text-theme-text-tertiary">{source.key}</span>}
      </span>
    )
  }
  if (source.kind === 'Variable' && source.variable) {
    return <span className="font-mono text-theme-text-secondary">{source.variable}</span>
  }
  const label = source.kind === 'Direct'
    ? 'Set directly'
    : source.kind === 'Pod'
      ? 'Pod'
      : source.kind === 'Container resources'
        ? 'Container resources'
        : source.kind
  return <span className="text-theme-text-secondary">{label}</span>
}

function formatSources(sources: PodEnvironmentSource[]) {
  return sources.map(source => source.kind + (source.name ? '/' + source.name : '') + (source.key ? '[' + source.key + ']' : '')).join(', ')
}

function StatusCell({ row }: { row: PodEnvironmentRow }) {
  if (row.evidence) {
    const label = row.evidence.kind === 'added'
      ? 'Added after start'
      : row.evidence.kind === 'removed'
        ? 'Removed after start'
        : 'Changed after start'
    return <Badge severity="warning" size="sm" title={row.evidence.message}>{label}</Badge>
  }
  if (row.state === 'denied') return <Badge severity="info" size="sm" title={row.message}>Access needed</Badge>
  if (row.state === 'missing') return <Badge severity="warning" size="sm" title={row.message}>Missing</Badge>
  if (row.state === 'unavailable') return <Badge severity="neutral" size="sm" title={row.message}>Unavailable</Badge>
  if (row.currentPodValue) return <Badge tone="note" size="sm" title={row.message}>Current Pod value</Badge>
  return null
}

function rowKey(container: string, variable: string) {
  return container + '\u0000' + variable
}

function withoutKey<T>(record: Record<string, T>, key: string) {
  const next = { ...record }
  delete next[key]
  return next
}

function formatObservedSince(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
