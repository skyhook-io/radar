import { useEffect, useState } from 'react'
import { clsx } from 'clsx'
import { Loader2 } from 'lucide-react'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { Badge, type BadgeSeverity } from '../ui/Badge'
import { AlertBanner } from '../ui/drawer-components'
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

// The two options the operator chooses in the dialog. Both are always sent
// explicitly to the drain endpoint; nothing relies on server-side defaults.
export interface DrainDialogOptions {
  force: boolean
  deleteEmptyDirData: boolean
}

export const DEFAULT_DRAIN_DIALOG_OPTIONS: DrainDialogOptions = { force: false, deleteEmptyDirData: false }

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
  acknowledgedEmptyDir: boolean
  /** false when the host cannot compute plans (no onPlanDrain); the dialog then only gates on the acknowledgement */
  planSupported: boolean
}

/**
 * Whether the destructive Drain button may be enabled. With plan support the
 * operator must see a current plan first. Enabling deleteEmptyDirData always
 * requires an explicit acknowledgement: the plan is an estimate, and a pod that
 * starts using emptyDir between the estimate and the drain would still lose its data.
 */
export function canConfirmDrain({ plan, nodeName, options, loading, acknowledgedEmptyDir, planSupported }: CanConfirmDrainInput): boolean {
  if (loading) return false
  const current = planMatches(plan, nodeName, options) ? plan : null
  if (planSupported && !current) return false
  if (options.deleteEmptyDirData && !acknowledgedEmptyDir) return false
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

interface DrainPlanContentProps {
  nodeName: string
  plan?: DrainPlan | null
  loading: boolean
  error?: string | null
  options: DrainDialogOptions
  onOptionsChange: (options: DrainDialogOptions) => void
  planSupported: boolean
  acknowledgedEmptyDir: boolean
  onAcknowledgeEmptyDir: (acknowledged: boolean) => void
}

/**
 * The body of the drain dialog: options, the plan table and the emptyDir
 * acknowledgement. Presentational, so it renders without a DOM (tests use
 * react-dom/server); the enclosing dialog owns the confirm gating.
 */
export function DrainPlanContent({
  nodeName, plan, loading, error, options, onOptionsChange, planSupported, acknowledgedEmptyDir, onAcknowledgeEmptyDir,
}: DrainPlanContentProps) {
  const current = planMatches(plan, nodeName, options) ? plan : null
  const atRisk = current ? emptyDirPodsAtRisk(current) : []
  const needsAck = options.deleteEmptyDirData

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
      </div>

      {planSupported && loading && (
        <div className="flex items-center gap-2 text-theme-text-secondary">
          <Loader2 className="w-4 h-4 animate-spin" /> Computing the plan…
        </div>
      )}

      {planSupported && !loading && error && (
        <AlertBanner variant="error" title="Could not compute the drain plan" message={error} />
      )}

      {planSupported && !loading && !error && current && (
        <>
          <div className="text-theme-text-primary">
            {pluralize(current.summary.evict, 'pod')} to evict,{' '}
            {current.pdbsEvaluated
              ? `${current.summary.mayBlock} may block on a PodDisruptionBudget`
              : 'PodDisruptionBudgets not evaluated'}
            , {current.summary.skip} skipped. This is an estimate from {new Date(current.generatedAt).toLocaleTimeString()}; the drain re-lists live state when it runs, and pods, budgets and permissions can change until then.
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

      {needsAck && (
        <label
          className={clsx(
            'flex items-start gap-2 cursor-pointer rounded p-2 border',
            'border-red-500/40 bg-red-500/10 text-theme-text-primary',
          )}
        >
          <input
            type="checkbox"
            checked={acknowledgedEmptyDir}
            onChange={(e) => onAcknowledgeEmptyDir(e.target.checked)}
            className="mt-0.5 rounded border-theme-border"
          />
          <span>
            {current && atRisk.length > 0
              ? `Discard the emptyDir data of ${pluralize(atRisk.length, 'pod')}: ${atRisk.map((p) => `${p.namespace}/${p.name}`).join(', ')}.`
              : current
                ? 'No pod on this node uses emptyDir right now; any that does when the drain runs will lose that data.'
                : 'Discard the emptyDir data of every evicted pod that uses emptyDir volumes.'}{' '}
            I understand this data cannot be recovered.
          </span>
        </label>
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
  /** Whether the host can compute plans. Without it the dialog still gates emptyDir on an acknowledgement. */
  planSupported: boolean
}

export function DrainPlanDialog({
  open, nodeName, plan, loading, error, options, onOptionsChange, onConfirm, onClose, isDraining, planSupported,
}: DrainPlanDialogProps) {
  const [acknowledgedEmptyDir, setAcknowledgedEmptyDir] = useState(false)

  // Any change of the options invalidates the acknowledgement: the operator
  // acknowledges a specific set of pods, not the checkbox in the abstract.
  useEffect(() => {
    setAcknowledgedEmptyDir(false)
  }, [options.force, options.deleteEmptyDirData, open, plan?.generatedAt])

  const confirmEnabled = canConfirmDrain({ plan, nodeName, options, loading, acknowledgedEmptyDir, planSupported })

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
        acknowledgedEmptyDir={acknowledgedEmptyDir}
        onAcknowledgeEmptyDir={setAcknowledgedEmptyDir}
      />
    </ConfirmDialog>
  )
}
