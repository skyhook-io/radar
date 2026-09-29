import { useEffect, useId, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { AlertTriangle, Info, X } from 'lucide-react'
import { DialogPortal } from '../ui/DialogPortal'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import { AlertBanner } from '../ui/drawer-components'

/** One API call the action will make, shown in the technical section. */
export interface ActionWrite {
  /** Plain-language line, e.g. "patch Cluster payments/pg-orders status". */
  summary: string
  /** The literal body or field change, shown monospaced. */
  detail?: string
}

export interface ActionConfirmDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  /** Imperative title: "Switch over pg-orders?" */
  title: string
  subject: { kind: string; namespace?: string; name: string }
  /** Kubernetes context the write goes to, always visible. */
  context?: string
  /** What happens, stated first. */
  effect: ReactNode
  /** Form fields (targets, options). */
  children?: ReactNode
  notes?: ReactNode[]
  warnings?: ReactNode[]
  /** Extra acknowledgement block, e.g. the GitOps write warning. */
  guard?: ReactNode
  /** Blocks confirm until the guard is satisfied. */
  guardSatisfied?: boolean
  writes?: ActionWrite[]
  /** When set, the user must type this exact text to confirm. */
  typedConfirmation?: string
  confirmLabel: string
  /** Disruptive actions use the danger styling. */
  disruptive?: boolean
  /** Why the action cannot run right now; disables confirm and is shown. */
  disabledReason?: string
  isLoading?: boolean
  /** Error from the last attempt, shown inline so the user can adjust and retry. */
  error?: string | null
  /**
   * The last attempt may or may not have taken effect. Confirm stays locked:
   * repeating it could apply the action twice.
   */
  outcomeUnknown?: boolean
  /** `wide` for dialogs that show evidence beside their form fields. */
  size?: 'default' | 'wide'
}

export function ActionConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  subject,
  context,
  effect,
  children,
  notes = [],
  warnings = [],
  guard,
  guardSatisfied = true,
  writes = [],
  typedConfirmation,
  confirmLabel,
  disruptive = false,
  disabledReason,
  isLoading = false,
  error,
  outcomeUnknown = false,
  size = 'default',
}: ActionConfirmDialogProps) {
  const titleId = useId()
  const [typed, setTyped] = useState('')
  const [showWrites, setShowWrites] = useState(false)
  const writesDisclosure = useDisclosure(showWrites)

  useEffect(() => {
    if (!open) {
      setTyped('')
      setShowWrites(false)
    }
  }, [open])

  const typedOk = !typedConfirmation || typed.trim() === typedConfirmation
  const canConfirm = !disabledReason && !outcomeUnknown && typedOk && guardSatisfied && !isLoading
  const where = [subject.namespace, subject.name].filter(Boolean).join('/')

  return (
    <DialogPortal open={open} onClose={onClose} closable={!isLoading} className={size === 'wide' ? 'w-full max-w-4xl' : 'w-full max-w-xl'} ariaLabelledBy={titleId}>
      <div className="flex items-start gap-3 border-b border-theme-border p-4">
        <div className="min-w-0 flex-1">
          <h3 id={titleId} className="text-lg font-semibold text-theme-text-primary">{title}</h3>
          <div className="mt-0.5 text-xs text-theme-text-tertiary">
            {subject.kind} <span className="font-mono">{where}</span>
            {context && (
              <>
                {' · context '}
                <span className="font-mono">{context}</span>
              </>
            )}
          </div>
        </div>
        <button
          type="button"
          onClick={onClose}
          disabled={isLoading}
          aria-label="Close"
          className="rounded p-1 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary disabled:opacity-50"
        >
          <X className="h-5 w-5" />
        </button>
      </div>

      <div className="max-h-[70vh] space-y-4 overflow-y-auto p-4 text-sm text-theme-text-primary">
        <div className="text-theme-text-primary">{effect}</div>

        {children}

        {warnings.length > 0 && (
          <div className="space-y-2">
            {warnings.map((w, i) => (
              <div key={i} className="flex items-start gap-2 text-sm text-theme-text-primary">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
                <div className="min-w-0">{w}</div>
              </div>
            ))}
          </div>
        )}

        {notes.length > 0 && (
          <div className="space-y-1.5">
            {notes.map((n, i) => (
              <div key={i} className="flex items-start gap-2 text-xs text-theme-text-secondary">
                <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
                <div className="min-w-0">{n}</div>
              </div>
            ))}
          </div>
        )}

        {guard}

        {writes.length > 0 && (
          <div>
            <button
              type="button"
              {...writesDisclosure.buttonProps}
              onClick={() => setShowWrites((v) => !v)}
              className="flex items-center gap-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary"
            >
              <CollapseChevron open={showWrites} className="h-3.5 w-3.5" />
              {writes.length === 1 ? 'The API write' : `The API writes, in order (${writes.length})`}
            </button>
            <Collapse open={showWrites} id={writesDisclosure.panelId}>
              <ol className="mt-2 space-y-2 pl-5 text-xs">
                {writes.map((w, i) => (
                  <li key={i} className="list-decimal text-theme-text-secondary">
                    <div>{w.summary}</div>
                    {w.detail && (
                      <pre className="mt-1 overflow-auto whitespace-pre-wrap rounded bg-theme-base/60 p-2 font-mono text-[11.5px] text-theme-text-primary">
                        {w.detail}
                      </pre>
                    )}
                  </li>
                ))}
              </ol>
            </Collapse>
          </div>
        )}

        {typedConfirmation && (
          <label className="block">
            <span className="text-xs text-theme-text-secondary">
              Type <span className="font-mono font-semibold text-theme-text-primary">{typedConfirmation}</span> to confirm
            </span>
            <input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              className="mt-1 w-full rounded-lg border border-theme-border bg-theme-base px-3 py-1.5 font-mono text-sm text-theme-text-primary focus:outline-none focus:ring-2 focus:ring-skyhook-500"
            />
          </label>
        )}

        {error &&
          (outcomeUnknown ? (
            <AlertBanner variant="warning" title="Radar could not tell whether this took effect" message={error} />
          ) : (
            <AlertBanner variant="error" title="The write did not happen" message={error} />
          ))}
        {disabledReason && <AlertBanner variant="info" title="This action is not available" message={disabledReason} />}
      </div>

      <div className="flex items-center justify-end gap-3 border-t border-theme-border p-4">
        <button
          type="button"
          onClick={onClose}
          disabled={isLoading}
          className="rounded-lg px-4 py-2 text-sm font-medium text-theme-text-secondary transition-colors hover:bg-theme-elevated hover:text-theme-text-primary disabled:opacity-50"
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={onConfirm}
          disabled={!canConfirm}
          className={clsx(
            'flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50',
            disruptive ? 'bg-red-600 text-white hover:bg-red-700' : 'btn-brand',
          )}
        >
          {isLoading && (
            <svg className="h-4 w-4 animate-spin" viewBox="0 0 24 24" aria-hidden>
              <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" fill="none" />
              <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
            </svg>
          )}
          {confirmLabel}
        </button>
      </div>
    </DialogPortal>
  )
}
