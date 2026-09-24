import { Info, Loader2, RefreshCw } from 'lucide-react'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { Badge, type BadgeSeverity } from '../ui/Badge'
import { AlertBanner } from '../ui/drawer-components'
import { Tooltip } from '../ui/Tooltip'
import { pluralize } from '../../utils/pluralize'

// Shapes returned by POST /api/nodes/{name}/drain-plan.
export type DrainOutcome = 'evict' | 'skip' | 'may-block'

export interface DrainPlanPod {
  namespace: string
  name: string
  outcome: DrainOutcome
  reason: string
  emptyDir: boolean
  pdb?: string
  pdbChecked: boolean
}

export interface DrainPlan {
  node: string
  generatedAt: string
  estimate: boolean
  options: { ignoreDaemonSets: boolean; deleteEmptyDirData: boolean; force: boolean; gracePeriodSeconds?: number }
  summary: { evict: number; skip: number; mayBlock: number }
  pods: DrainPlanPod[]
  pdbsEvaluated: boolean
  pdbError?: string
}

// The options the operator chooses in the dialog. force and deleteEmptyDirData are always
// sent explicitly to the drain endpoint; nothing relies on server-side defaults. waitForDeletion
// does not affect the plan (what would be evicted), only whether the drain waits for the pods
// to actually leave before reporting done.
export interface DrainDialogOptions {
  force: boolean
  deleteEmptyDirData: boolean
  waitForDeletion: boolean
}

export const DEFAULT_DRAIN_DIALOG_OPTIONS: DrainDialogOptions = { force: false, deleteEmptyDirData: false, waitForDeletion: true }

/** A plan is only usable when it was computed for this node with these options. */
export function planMatches(plan: DrainPlan | null | undefined, nodeName: string, options: DrainDialogOptions): plan is DrainPlan {
  return Boolean(
    plan &&
      plan.node === nodeName &&
      plan.options.force === options.force &&
      plan.options.deleteEmptyDirData === options.deleteEmptyDirData,
  )
}

/** Pods whose emptyDir data would be discarded by this drain (evicted, not skipped). */
export function emptyDirPodsAtRisk(plan: DrainPlan): DrainPlanPod[] {
  return plan.pods.filter((p) => p.emptyDir && p.outcome !== 'skip')
}

export interface CanConfirmDrainInput {
  plan: DrainPlan | null | undefined
  nodeName: string
  options: DrainDialogOptions
  loading: boolean
  /** the last plan request failed; with plan support the drain stays disabled until a plan is shown again */
  error?: string | null
  /** false when the host cannot compute plans (no onPlanDrain); the dialog then confirms without one */
  planSupported: boolean
}

/**
 * Whether the destructive Drain button may be enabled. With plan support the
 * operator must see a current plan first. Deleting emptyDir data is its own
 * deliberate choice, off by default and warned about by name, so it does not
 * gate the button a second time.
 */
export function canConfirmDrain({ plan, nodeName, options, loading, error, planSupported }: CanConfirmDrainInput): boolean {
  if (loading) return false
  if (planSupported && error) return false
  const current = planMatches(plan, nodeName, options) ? plan : null
  if (planSupported && !current) return false
  return true
}

const OUTCOME_SEVERITY: Record<DrainOutcome, BadgeSeverity> = {
  evict: 'warning',
  'may-block': 'error',
  skip: 'neutral',
}

const OUTCOME_LABEL: Record<DrainOutcome, string> = {
  evict: 'evict',
  'may-block': 'may block',
  skip: 'skip',
}

const ESTIMATE_CAVEAT =
  'An estimate, not a guarantee: the drain re-lists live state when it runs, and pods, budgets and permissions can change until then. ' +
  'Evictions a budget refuses are retried until the drain deadline; a pod covered by more than one budget is refused outright.'

interface DrainPlanContentProps {
  nodeName: string
  plan?: DrainPlan | null
  loading: boolean
  error?: string | null
  options: DrainDialogOptions
  onOptionsChange: (options: DrainDialogOptions) => void
  planSupported: boolean
  /** Recompute the plan with the current options; also the retry after a failed plan. */
  onRefreshPlan?: () => void
}

/**
 * The body of the drain dialog: options, the plan table and the emptyDir
 * warning. Presentational, so it renders without a DOM (tests use
 * react-dom/server); the enclosing dialog owns the confirm gating.
 */
export function DrainPlanContent({
  nodeName, plan, loading, error, options, onOptionsChange, planSupported, onRefreshPlan,
}: DrainPlanContentProps) {
  const current = planMatches(plan, nodeName, options) ? plan : null
  const atRisk = current ? emptyDirPodsAtRisk(current) : []

  return (
    <div className="flex flex-col gap-3 text-sm text-theme-text-secondary">
      <div className="flex flex-wrap gap-x-6 gap-y-2">
        <label className="flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={options.force}
            onChange={(e) => onOptionsChange({ ...options, force: e.target.checked })}
            className="rounded border-theme-border"
          />
          Force: evict pods not managed by a controller (they are not recreated)
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={options.deleteEmptyDirData}
            onChange={(e) => onOptionsChange({ ...options, deleteEmptyDirData: e.target.checked })}
            className="rounded border-theme-border"
          />
          Delete emptyDir data: evict pods that use emptyDir volumes (their data is lost)
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={options.waitForDeletion}
            onChange={(e) => onOptionsChange({ ...options, waitForDeletion: e.target.checked })}
            className="rounded border-theme-border"
          />
          Wait for the pods to actually be deleted before reporting the node drained (kubectl parity)
        </label>
      </div>

      {planSupported && loading && (
        <div className="flex items-center gap-2 text-theme-text-secondary">
          <Loader2 className="w-4 h-4 animate-spin" /> Computing the plan…
        </div>
      )}

      {planSupported && !loading && error && (
        <AlertBanner variant="error" title="Could not compute the drain plan" message={error}>
          {onRefreshPlan && (
            <button
              type="button"
              onClick={onRefreshPlan}
              className="mt-2 rounded border border-theme-border bg-theme-surface px-2 py-1 text-xs text-theme-text-primary transition-colors hover:bg-theme-hover"
            >
              Try again
            </button>
          )}
        </AlertBanner>
      )}

      {planSupported && !loading && !error && current && (
        <>
          <div className="flex items-start justify-between gap-2">
            <div className="text-theme-text-primary">
              {pluralize(current.summary.evict, 'pod')} to evict,{' '}
              {current.pdbsEvaluated
                ? `${current.summary.mayBlock} may block on a PodDisruptionBudget`
                : 'PodDisruptionBudgets not evaluated'}
              , {current.summary.skip} skipped.
            </div>
            <div className="flex items-center gap-1 shrink-0 text-xs text-theme-text-tertiary">
              <span>Estimated at {new Date(current.generatedAt).toLocaleTimeString()}</span>
              <Tooltip content={ESTIMATE_CAVEAT} className="max-w-xs">
                <button
                  type="button"
                  aria-label="About this estimate"
                  className="p-0.5 rounded text-theme-text-tertiary transition-colors hover:text-theme-text-secondary"
                >
                  <Info className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
              {onRefreshPlan && (
                <Tooltip content="Recompute the plan">
                  <button
                    type="button"
                    aria-label="Recompute the plan"
                    onClick={onRefreshPlan}
                    className="p-1 rounded text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                  >
                    <RefreshCw className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              )}
            </div>
          </div>

          {!current.pdbsEvaluated && (
            <AlertBanner
              variant="warning"
              title="PodDisruptionBudgets were not evaluated"
              message={current.pdbError ?? 'Budgets could not be listed; evictions may still be refused.'}
            />
          )}

          <div className="max-h-72 overflow-auto rounded border border-theme-border">
            <table className="w-full text-xs">
              <tbody>
                {current.pods.map((p) => (
                  <tr key={`${p.namespace}/${p.name}`} className="border-b border-theme-border last:border-b-0 align-top">
                    <td className="px-2 py-1.5 whitespace-nowrap">
                      <Badge severity={OUTCOME_SEVERITY[p.outcome]} size="sm">{OUTCOME_LABEL[p.outcome]}</Badge>
                    </td>
                    <td className="px-2 py-1.5 font-mono whitespace-nowrap text-theme-text-primary">
                      {p.namespace}/{p.name}
                    </td>
                    <td className="px-2 py-1.5 text-theme-text-secondary">{p.reason}</td>
                  </tr>
                ))}
                {current.pods.length === 0 && (
                  <tr>
                    <td className="px-2 py-3 text-theme-text-secondary" colSpan={3}>No pods are scheduled on this node.</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}

      {options.deleteEmptyDirData && (
        <AlertBanner
          variant="warning"
          title="emptyDir data will be discarded"
          message={
            current && atRisk.length > 0
              ? `${pluralize(atRisk.length, 'pod')} that would be evicted ${atRisk.length === 1 ? 'uses' : 'use'} emptyDir: ${atRisk.map((p) => `${p.namespace}/${p.name}`).join(', ')}. That data cannot be recovered.`
              : current
                ? 'No pod that would be evicted uses emptyDir right now. Any that does when the drain runs loses that data, which cannot be recovered.'
                : 'Every evicted pod that uses emptyDir volumes loses that data, which cannot be recovered.'
          }
        />
      )}
    </div>
  )
}

interface DrainPlanDialogProps {
  open: boolean
  nodeName: string
  plan?: DrainPlan | null
  loading: boolean
  error?: string | null
  options: DrainDialogOptions
  onOptionsChange: (options: DrainDialogOptions) => void
  onConfirm: (options: DrainDialogOptions) => void
  onClose: () => void
  isDraining: boolean
  /** Whether the host can compute plans. Without it the dialog confirms without one. */
  planSupported: boolean
  /** Recompute the plan with the current options; also the retry after a failed plan. */
  onRefreshPlan?: () => void
}

export function DrainPlanDialog({
  open, nodeName, plan, loading, error, options, onOptionsChange, onConfirm, onClose, isDraining, planSupported, onRefreshPlan,
}: DrainPlanDialogProps) {
  const confirmEnabled = canConfirmDrain({ plan, nodeName, options, loading, error, planSupported })

  return (
    <ConfirmDialog
      open={open}
      onClose={onClose}
      onConfirm={() => onConfirm(options)}
      title="Drain Node"
      message={`Cordon "${nodeName}" and evict the pods listed below. The list is an estimate: the drain re-lists live state when it runs.`}
      confirmLabel={isDraining ? 'Draining...' : 'Drain'}
      variant="danger"
      isLoading={isDraining}
      isClosable
      confirmDisabled={!confirmEnabled}
      className="max-w-3xl"
    >
      <DrainPlanContent
        nodeName={nodeName}
        plan={plan}
        loading={loading}
        error={error}
        options={options}
        onOptionsChange={onOptionsChange}
        planSupported={planSupported}
        onRefreshPlan={onRefreshPlan}
      />
    </ConfirmDialog>
  )
}
