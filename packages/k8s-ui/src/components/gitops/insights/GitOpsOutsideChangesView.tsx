import { useState, type ReactNode } from 'react'
import { AlertTriangle, Hand } from 'lucide-react'
import type { GitOpsOutsideChanges, GitOpsOutsideOutcome, GitOpsOutsideRef } from '../../../types/gitops-outside'
import { formatRelativeAgeTime } from '../../../utils/format'
import { Collapse, CollapseChevron, useDisclosure } from '../../ui/Collapse'
import { PaneLoader } from '../../ui/PaneLoader'
import { Tooltip } from '../../ui/Tooltip'
import { AlertBanner } from '../../ui/drawer-components'

// What the root's policy says Flux does about a hand edit. Present tense and
// never a promise: drift detection can change, and unknown reads as unknown.
export function outsideOutcomeText(code: GitOpsOutsideOutcome): string {
  switch (code) {
    case 'reverted':
      return 'Flux reverts this on its next run'
    case 'reported':
      return 'Flux reports this drift and leaves it'
    case 'stays':
      return 'Stays until a Helm upgrade changes this field'
    case 'may-overwrite':
      return 'Flux may overwrite this on its next run'
    case 'ignored':
      return "Ignored by this release's drift rules"
    case 'opted-out':
      return 'This object opts out of Flux reconciliation'
    case 'suspended':
      return 'Flux is suspended; nothing changes until it resumes'
  }
  return "Radar couldn't read this object's Flux policy."
}

export function GitOpsOutsideChangesView({
  data,
  error,
  loading,
  onOpenResource,
}: {
  data?: GitOpsOutsideChanges
  error?: Error | null
  loading?: boolean
  onOpenResource?: (ref: { group?: string; kind: string; namespace?: string; name: string }) => void
}) {
  if (loading && !data) return <PaneLoader label="Reading field managers…" />
  if (error && !data) return <AlertBanner variant="error" title="Couldn't read changes made outside Flux" message={error.message} className="m-4" />
  if (!data) return null

  const coverageNotes = outsideCoverageNotes(data)
  return (
    <div className="min-h-0 flex-1 overflow-auto px-4 py-3 text-sm">
      {data.driftEvents.length > 0 && (
        <div className="mb-3 rounded-md border border-theme-border bg-theme-surface px-3 py-2">
          <div className="mb-1 flex items-center gap-1.5 text-xs font-medium text-theme-text-primary">
            <AlertTriangle className="h-3.5 w-3.5 text-theme-text-tertiary" />
            Flux reported drift
          </div>
          <ul className="space-y-0.5 text-xs text-theme-text-secondary">
            {data.driftEvents.map((ev, i) => (
              <li key={i}>
                <span className="text-theme-text-tertiary">{formatRelativeAgeTime(ev.lastTimestamp)}</span> {ev.message}
              </li>
            ))}
          </ul>
        </div>
      )}

      {data.objects.length === 0 ? (
        <div className="flex items-center gap-2 py-4 text-theme-text-tertiary">
          <Hand className="h-4 w-4" />
          <span>Nothing was changed outside Flux in the objects Radar could read.</span>
        </div>
      ) : (
        <table className="w-full table-fixed border-collapse text-xs">
          <thead>
            <tr className="border-b border-theme-border text-left text-theme-text-tertiary">
              <th className="w-1/5 py-1.5 pr-3 font-medium">Object</th>
              <th className="w-1/4 py-1.5 pr-3 font-medium">Field</th>
              <th className="w-1/6 py-1.5 pr-3 font-medium">Written by</th>
              <th className="py-1.5 font-medium">What Flux does</th>
            </tr>
          </thead>
          <tbody>
            {data.objects.flatMap((obj) =>
              obj.changes.map((change, i) => (
                <tr key={`${refKey(obj.ref)}-${change.manager}-${i}`} className="border-b border-theme-border/60 align-top">
                  <td className="py-1.5 pr-3">
                    {i === 0 && <ObjectLink objRef={obj.ref} onOpen={onOpenResource} />}
                  </td>
                  <td className="py-1.5 pr-3 font-mono text-theme-text-primary">
                    {change.paths.map((path) => (
                      <Tooltip key={path} content={path} delay={400} wrapperClassName="block max-w-full">
                        <div className="truncate">
                          {path}
                          {change.values?.[path] !== undefined && <span className="text-theme-text-secondary"> = <span>{change.values[path]}</span></span>}
                        </div>
                      </Tooltip>
                    ))}
                    {change.morePaths > 0 && <div className="text-theme-text-tertiary">+{change.morePaths} more</div>}
                  </td>
                  <td className="py-1.5 pr-3 text-theme-text-secondary">
                    <Tooltip content={`Field manager ${change.manager} (${change.operation})`} delay={300}>
                      <span>{change.tool}</span>
                    </Tooltip>
                    {change.time && <div className="text-theme-text-tertiary">{formatRelativeAgeTime(change.time)}</div>}
                  </td>
                  <td className="py-1.5 text-theme-text-secondary">{outsideOutcomeText(change.outcome)}</td>
                </tr>
              )),
            )}
          </tbody>
        </table>
      )}

      {data.harmless.length > 0 && (
        <Fold title={`Actions that change no desired state (${data.harmless.reduce((n, h) => n + h.count, 0)})`}>
          {data.harmless.map((h) => (
            <div key={h.tool}>{h.tool} · {h.count} {h.count === 1 ? 'object' : 'objects'}</div>
          ))}
        </Fold>
      )}
      {data.otherWriters.length > 0 && (
        <Fold title={`Other controllers and tools (${data.otherWriters.length})`}>
          <p className="mb-1 text-theme-text-tertiary">Not counted as edits: controllers, operators and the Helm CLI (often the install from before Flux adopted the release).</p>
          {data.otherWriters.map((o) => (
            <div key={o.manager}>{o.manager} · {o.objects} {o.objects === 1 ? 'object' : 'objects'}</div>
          ))}
        </Fold>
      )}
      {coverageNotes.length > 0 && (
        <AlertBanner variant="warning" title="This list may be incomplete" items={coverageNotes} className="mt-3" />
      )}
    </div>
  )
}

function outsideCoverageNotes(data: GitOpsOutsideChanges): string[] {
  const c = data.coverage
  const notes: string[] = []
  if (c.truncated) notes.push(`Radar stopped after ${c.listed} objects or its time limit; the list is incomplete.`)
  if (c.denied?.length) notes.push(`You can't list ${c.denied.map((d) => `${d.resource} in ${d.namespace}`).join(', ')}.`)
  if (c.failed?.length) notes.push(`Radar couldn't list ${c.failed.map((d) => `${d.resource} in ${d.namespace}`).join(', ')}.`)
  if (c.remote) notes.push("This root applies to another cluster, whose objects Radar doesn't read.")
  if (c.noInventory) notes.push('This Kustomization records no inventory (Flux keeps one only when pruning is on), so Radar can\'t tell which objects it applies.')
  if (c.labelScan) notes.push('This release records no inventory, so Radar checked common kinds by Flux labels only.')
  if (c.notChecked?.length) notes.push(`Not checked: ${c.notChecked.join(', ')}.`)
  if (c.skipped?.includes('secrets')) notes.push('Secrets are never read: listing them would return their data.')
  return notes
}

function refKey(ref: GitOpsOutsideRef): string {
  return `${ref.group}/${ref.kind}/${ref.namespace}/${ref.name}`
}

function ObjectLink({ objRef, onOpen }: { objRef: GitOpsOutsideRef; onOpen?: (ref: { group?: string; kind: string; namespace?: string; name: string }) => void }) {
  const label = (
    <>
      <span className="text-theme-text-tertiary">{objRef.kind}</span> <span className="text-theme-text-primary">{objRef.name}</span>
    </>
  )
  if (!onOpen) return <div className="truncate">{label}</div>
  return (
    <button type="button" className="max-w-full truncate text-left hover:underline" onClick={() => onOpen({ group: objRef.group, kind: objRef.kind, namespace: objRef.namespace, name: objRef.name })}>
      {label}
    </button>
  )
}

function Fold({ title, children }: { title: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const disclosure = useDisclosure(open)
  return (
    <div className="mt-3 rounded-md border border-theme-border">
      <button
        {...disclosure.buttonProps}
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-theme-text-secondary hover:bg-theme-hover"
      >
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        {title}
      </button>
      <Collapse open={open} id={disclosure.panelId}>
        <div className="space-y-0.5 px-3 pb-2 text-xs text-theme-text-secondary">{children}</div>
      </Collapse>
    </div>
  )
}
