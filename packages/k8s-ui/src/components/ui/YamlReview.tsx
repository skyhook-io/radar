import { useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, ArrowLeft, Check, Loader2, ShieldAlert, ShieldCheck, X } from 'lucide-react'
import { normalizeYamlForReview, splitYamlDocuments } from '../../utils/yaml'
import { Badge } from './Badge'
import { StatusDot } from './status-tone'
import { YamlDiffEditor } from './YamlEditor'

export interface YamlPreviewResult {
  index: number
  status: 'accepted' | 'rejected' | 'unavailable'
  apiVersion?: string
  kind?: string
  namespace?: string
  name?: string
  action?: 'create' | 'update' | 'unknown'
  submittedYaml?: string
  baselineYaml?: string
  predictedYaml?: string
  warnings?: string[]
  error?: string
  reviewedResourceVersion?: string
  redacted?: boolean
}

export interface YamlReviewProps {
  submittedYaml: string
  originalYaml?: string
  documents: YamlPreviewResult[]
  nonAtomic?: boolean
  force?: boolean
  isApplying?: boolean
  applyError?: string | null
  applyLabel?: string
  onClose?: () => void
  onBack: () => void
  onApply: () => void
}

function splitYaml(content: string) {
  return splitYamlDocuments(content).map((document) => document.content)
}

function previewSeverity(status: YamlPreviewResult['status']) {
  if (status === 'accepted') return 'success' as const
  if (status === 'rejected') return 'error' as const
  return 'warning' as const
}

export function canApplyYamlPreview(
  documents: YamlPreviewResult[],
  unavailableAcknowledged: boolean,
  reviewReady = true,
) {
  return (
    reviewReady &&
    !documents.some((document) => document.status === 'rejected') &&
    (unavailableAcknowledged || !documents.some((document) => document.status === 'unavailable'))
  )
}

export function reviewedResourceVersionsForPreview(documents: YamlPreviewResult[]) {
  return Object.fromEntries(
    documents.flatMap((document) => {
      if (document.action === 'create') return [[document.index, '']]
      if (document.action === 'update' && document.reviewedResourceVersion) {
        return [[document.index, document.reviewedResourceVersion]]
      }
      return []
    }),
  )
}

export function YamlReview({
  submittedYaml,
  originalYaml,
  documents,
  nonAtomic = false,
  force = false,
  isApplying = false,
  applyError,
  applyLabel = 'Apply reviewed changes',
  onClose,
  onBack,
  onApply,
}: YamlReviewProps) {
  const [selectedIndex, setSelectedIndex] = useState(0)
  const reviewRef = useRef<HTMLDivElement>(null)
  const [unified, setUnified] = useState(
    () => typeof window !== 'undefined' && window.innerWidth < 900,
  )
  const diffLayoutChosen = useRef(false)
  const [hideUnchanged, setHideUnchanged] = useState(true)
  const [unavailableAcknowledged, setUnavailableAcknowledged] = useState(false)
  const [diffReady, setDiffReady] = useState(false)
  const reviewSignature = useMemo(
    () =>
      JSON.stringify(
        documents.map(
          ({
            index,
            status,
            action,
            reviewedResourceVersion,
            predictedYaml,
            submittedYaml: documentYaml,
            error,
          }) => ({
            index,
            status,
            action,
            reviewedResourceVersion,
            predictedYaml,
            documentYaml,
            error,
          }),
        ),
      ),
    [documents],
  )
  const submitted = useMemo(() => splitYaml(submittedYaml), [submittedYaml])
  const original = useMemo(
    () => (originalYaml ? splitYaml(originalYaml) : submitted),
    [originalYaml, submitted],
  )
  const selected = documents[selectedIndex] ?? documents[0]
  const before = normalizeYamlForReview(
    selected?.baselineYaml ||
      (originalYaml
        ? (original[selected?.index] ?? '')
        : selected?.submittedYaml || submitted[selected?.index] || ''),
  )
  const after = normalizeYamlForReview(
    selected?.predictedYaml || selected?.submittedYaml || submitted[selected?.index] || '',
  )
  const unavailable = documents.filter((document) => document.status === 'unavailable')
  const rejected = documents.filter((document) => document.status === 'rejected')
  const accepted = documents.filter((document) => document.status === 'accepted')
  const canApply = canApplyYamlPreview(documents, unavailableAcknowledged, diffReady)
  const applyBlockedReason = isApplying
    ? 'Applying reviewed changes'
    : rejected.length > 0
      ? `Fix ${rejected.length} rejected document${rejected.length === 1 ? '' : 's'} before applying`
      : unavailable.length > 0 && !unavailableAcknowledged
        ? `Acknowledge ${unavailable.length} unverified document${unavailable.length === 1 ? '' : 's'} before applying`
        : !diffReady
          ? 'Waiting for the review diff to render'
          : undefined
  const noChanges = selected?.status === 'accepted' && before === after

  useEffect(() => {
    setUnavailableAcknowledged(false)
  }, [reviewSignature])

  useEffect(() => {
    const review = reviewRef.current
    if (!review) return
    const adaptDiffLayout = () => {
      if (!diffLayoutChosen.current) setUnified(review.clientWidth < 800)
    }
    adaptDiffLayout()
    const observer = new ResizeObserver(adaptDiffLayout)
    observer.observe(review)
    return () => observer.disconnect()
  }, [])

  return (
    <div ref={reviewRef} className="flex h-full min-h-0 flex-col bg-theme-base">
      <div className="flex shrink-0 flex-wrap items-center gap-3 border-b border-theme-border bg-theme-elevated/70 px-4 py-2.5">
        <button
          type="button"
          onClick={onBack}
          disabled={isApplying}
          className="flex items-center gap-1.5 rounded px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:opacity-50"
        >
          <ArrowLeft className="h-3.5 w-3.5" />
          Back to edit
        </button>
        <div className="h-4 w-px bg-theme-border" />
        <div>
          <div className="text-sm font-semibold text-theme-text-primary">
            Review cluster-admitted changes
          </div>
          <div className="text-xs text-theme-text-tertiary">
            {accepted.length} accepted · {rejected.length} rejected · {unavailable.length} not
            previewable
          </div>
        </div>
        <div className="ml-auto flex items-center gap-2">
          <div className="flex rounded-md border border-theme-border bg-theme-base p-0.5 text-xs">
            <button
              type="button"
              onClick={() => {
                diffLayoutChosen.current = true
                setUnified(false)
              }}
              aria-pressed={!unified}
              className={`rounded px-2 py-1 ${!unified ? 'bg-theme-elevated text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary'}`}
            >
              Side by side
            </button>
            <button
              type="button"
              onClick={() => {
                diffLayoutChosen.current = true
                setUnified(true)
              }}
              aria-pressed={unified}
              className={`rounded px-2 py-1 ${unified ? 'bg-theme-elevated text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary'}`}
            >
              Unified
            </button>
          </div>
          <label className="flex items-center gap-1.5 text-xs text-theme-text-secondary">
            <input
              type="checkbox"
              checked={hideUnchanged}
              onChange={(event) => setHideUnchanged(event.target.checked)}
              className="h-3.5 w-3.5 rounded border-theme-border bg-theme-base"
            />
            Collapse unchanged
          </label>
          {onClose && (
            <button
              type="button"
              onClick={onClose}
              disabled={isApplying}
              aria-label="Close review"
              className="rounded p-1 text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:opacity-50"
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        {documents.length > 1 && (
          <nav
            className="max-h-36 shrink-0 overflow-auto border-b border-theme-border bg-theme-surface p-2 md:max-h-none md:w-56 md:border-b-0 md:border-r"
            aria-label="Previewed YAML documents"
          >
            {documents.map((document, index) => (
              <button
                type="button"
                key={`${document.index}:${document.kind}:${document.name}`}
                onClick={() => setSelectedIndex(index)}
                className={`mb-1 flex w-full items-start gap-2 rounded-md border px-2.5 py-2 text-left ${
                  index === selectedIndex
                    ? 'border-theme-border bg-theme-elevated'
                    : 'border-transparent hover:bg-theme-hover'
                }`}
              >
                <StatusDot
                  tone={
                    document.status === 'accepted'
                      ? 'healthy'
                      : document.status === 'rejected'
                        ? 'unhealthy'
                        : 'degraded'
                  }
                  size="md"
                  className="mt-1"
                />
                <span className="min-w-0">
                  <span className="block truncate text-xs font-medium text-theme-text-primary">
                    {document.kind || `Document ${document.index + 1}`}
                  </span>
                  <span className="block truncate text-[11px] text-theme-text-tertiary">
                    {document.namespace ? `${document.namespace}/` : ''}
                    {document.name || 'Unnamed'}
                  </span>
                </span>
              </button>
            ))}
          </nav>
        )}

        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          {selected && (
            <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-theme-border bg-theme-surface px-3 py-2">
              <Badge severity={previewSeverity(selected.status)} size="sm">
                {selected.status === 'accepted' ? (
                  <Check className="h-3 w-3" />
                ) : (
                  <AlertTriangle className="h-3 w-3" />
                )}
                {selected.status === 'unavailable' ? 'Not previewable' : selected.status}
              </Badge>
              {selected.action && selected.action !== 'unknown' && (
                <Badge severity="neutral" size="sm">
                  Will {selected.action}
                </Badge>
              )}
              <span className="text-xs text-theme-text-tertiary">
                {selected.baselineYaml
                  ? 'Live at review → server preview'
                  : originalYaml
                    ? 'Live when editing started → server preview'
                    : 'Effective submitted object → server preview'}
              </span>
              {selected.redacted && (
                <Badge tone="note" size="sm">
                  Secret values masked
                </Badge>
              )}
              {noChanges && (
                <Badge severity="neutral" size="sm">
                  No effective changes
                </Badge>
              )}
            </div>
          )}

          {(selected?.error || (selected?.warnings?.length ?? 0) > 0) && (
            <div className="shrink-0 space-y-1 border-b border-theme-border bg-theme-surface px-3 py-2 text-xs">
              {selected.error && (
                <div
                  className={
                    selected.status === 'rejected'
                      ? 'text-red-600 dark:text-red-400'
                      : 'text-warning-text'
                  }
                >
                  {selected.error}
                </div>
              )}
              {selected.warnings?.map((warning) => (
                <div key={warning} className="flex items-start gap-1.5 text-warning-text">
                  <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span>{warning}</span>
                </div>
              ))}
            </div>
          )}

          <div className="min-h-0 flex-1 bg-theme-surface">
            <YamlDiffEditor
              original={before}
              modified={after}
              height="100%"
              unified={unified}
              hideUnchanged={hideUnchanged}
              bleed
              onReadyChange={setDiffReady}
            />
          </div>
        </div>
      </div>

      <div className="shrink-0 border-t border-theme-border bg-theme-elevated/80 px-4 py-3">
        {applyError && (
          <div className="mb-2 rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">
            {applyError}
          </div>
        )}
        {nonAtomic && (
          <div className="mb-2 flex items-start gap-2 text-xs text-theme-text-secondary">
            <ShieldAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning-text" />
            <span>
              Resources apply sequentially, not atomically. Earlier documents can succeed before a
              later document fails.
            </span>
          </div>
        )}
        {unavailable.length > 0 && rejected.length === 0 && (
          <label className="mb-2 flex items-start gap-2 text-xs text-theme-text-secondary">
            <input
              type="checkbox"
              checked={unavailableAcknowledged}
              onChange={(event) => setUnavailableAcknowledged(event.target.checked)}
              className="mt-0.5 h-3.5 w-3.5 rounded border-theme-border bg-theme-base"
            />
            <span>
              Apply with {unavailable.length} unverified document
              {unavailable.length === 1 ? '' : 's'}. This can happen when documents depend on
              earlier resources, an API does not support dry-run, or live state could not be read.
              Unverified documents are not protected against changes after review.
            </span>
          </label>
        )}
        {rejected.length > 0 && (
          <div className="mb-2 flex items-center gap-2 text-xs text-red-600 dark:text-red-400">
            <ShieldAlert className="h-3.5 w-3.5" />
            Fix the rejected document{rejected.length === 1 ? '' : 's'} before applying.
          </div>
        )}
        <div className="flex items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-theme-text-tertiary">
            <span className="flex items-center gap-2">
              <ShieldCheck className="h-3.5 w-3.5 shrink-0" />
              Preview used Kubernetes server-side dry-run. Admission can still change before apply.
            </span>
            <Badge severity={force ? 'warning' : 'neutral'} size="sm">
              Force {force ? 'on' : 'off'}
            </Badge>
            <span>
              {force
                ? 'Other managers’ fields can be overwritten.'
                : 'Other-manager conflicts block the save.'}
            </span>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {applyBlockedReason && !isApplying && (
              <span
                aria-live="polite"
                className="max-w-48 text-right text-[11px] text-theme-text-tertiary"
              >
                {applyBlockedReason}
              </span>
            )}
            <button
              type="button"
              onClick={onApply}
              disabled={!canApply || isApplying}
              title={applyBlockedReason}
              className="btn-brand flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-lg px-4 py-1.5 text-xs font-medium disabled:cursor-not-allowed"
            >
              {isApplying && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              {isApplying ? 'Applying…' : applyLabel}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
