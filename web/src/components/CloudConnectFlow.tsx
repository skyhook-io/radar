import { useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { AlertTriangle, ArrowUpRight, Check, ExternalLink, GitBranch, Info, Loader2, ShieldAlert, X } from 'lucide-react'
import { Collapse, CollapseChevron } from '@skyhook-io/k8s-ui/components/ui/Collapse'
import {
  ApiError,
  cancelCloudInstall,
  type CloudInstallBlocked,
  type CloudInstallRecoveryGuidance,
  type CloudInstallStatus,
  dismissCloudInstall,
  startCloudInstall,
} from '../api/client'
import { showApiError } from './ui/Toast'

// The driver-lane connect flow rendered inside the Cloud funnel modal. The
// flow itself is server-owned (it survives modal close and page reloads);
// this component only renders the observed status and issues transitions.
export function CloudConnectFlow({
  status,
  blocked,
  signupUrl,
  onStatus,
  onExit,
}: {
  status: CloudInstallStatus
  blocked: CloudInstallBlocked | null
  signupUrl: string
  // Push a mutation's status response into the shared query state.
  onStatus: (st: CloudInstallStatus) => void
  // Leave the flow view (back to the pitch, or close after dismiss).
  onExit: () => void
}) {
  if (blocked) {
    return <BlockedView blocked={blocked} signupUrl={signupUrl} onExit={onExit} />
  }

  switch (status.state) {
    case 'preparing':
      return (
        <div className="px-8 py-10 flex flex-col items-center gap-3 text-center">
          <Loader2 className="w-5 h-5 animate-spin text-emerald-600 dark:text-emerald-400" />
          <p className="text-[13px] text-theme-text-secondary">
            Checking this cluster and preparing the install. This can take a moment on a slow link.
          </p>
        </div>
      )
    case 'ready':
      return <PlanCard status={status} onStatus={onStatus} onExit={onExit} />
    case 'starting':
    case 'awaiting_approval':
      return <ApprovalCard status={status} onStatus={onStatus} />
    case 'blocked':
      return null
    case 'provisioning':
    case 'waiting_tunnel':
      return <ProgressCard status={status} onStatus={onStatus} />
    case 'connected':
      return <ConnectedCard status={status} onStatus={onStatus} onExit={onExit} />
    case 'failed':
      return <FailedCard status={status} onStatus={onStatus} onExit={onExit} />
    default:
      return null
  }
}

function BlockedView({
  blocked,
  signupUrl,
  onExit,
}: {
  blocked: CloudInstallBlocked
  signupUrl: string
  onExit: () => void
}) {
  const icon =
    blocked.reason === 'gitops' ? (
      <GitBranch className="w-4 h-4 shrink-0 mt-0.5 text-emerald-600 dark:text-emerald-400" />
    ) : blocked.reason === 'preflight' ? (
      <ShieldAlert className="w-4 h-4 shrink-0 mt-0.5 text-amber-500" />
    ) : (
      <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5 text-amber-500" />
    )
  const title =
    blocked.reason === 'gitops'
      ? 'This install is managed by GitOps'
      : blocked.reason === 'preflight'
        ? 'Your Kubernetes identity can’t install this'
        : 'This cluster can’t be connected from here'
  return (
    <div className="px-8 pt-6 pb-5">
      <div className="card-inner-lg flex gap-2.5">
        {icon}
        <div className="min-w-0">
          <div className="text-[13px] font-semibold text-theme-text-primary">{title}</div>
          <p className="mt-1 text-[12px] leading-relaxed text-theme-text-secondary">{blocked.message}</p>
          {blocked.blocking && blocked.blocking.length > 0 && (
            <ul className="mt-2 space-y-1 text-[11.5px] text-theme-text-tertiary">
              {blocked.blocking.map((line) => (
                <li key={line} className="flex items-start gap-1.5">
                  <span className="mt-[6px] w-1 h-1 rounded-full bg-amber-500 shrink-0" />
                  {line}
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      <div className="mt-4 flex items-center gap-4">
        {/* Only a preflight denial has a legitimate browser alternative —
            someone with more Kubernetes permission can run the wizard. GitOps
            and unsupported refusals named a specific reason and target that a
            generic signup link cannot carry, so offering it would contradict
            the message directly above. */}
        {blocked.reason === 'preflight' && (
          <a
            href={signupUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="text-[12.5px] font-semibold text-emerald-600 dark:text-emerald-400 hover:underline underline-offset-2"
          >
            Connect through the browser wizard instead →
          </a>
        )}
        <button onClick={onExit} className="text-[12.5px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors">
          Back
        </button>
      </div>
    </div>
  )
}

function PlanCard({
  status,
  onStatus,
  onExit,
}: {
  status: CloudInstallStatus
  onStatus: (st: CloudInstallStatus) => void
  onExit: () => void
}) {
  const plan = status.plan
  const [clusterName, setClusterName] = useState(plan?.defaultClusterName ?? '')
  const [acceptAdoption, setAcceptAdoption] = useState(false)
  const [ackUncertainty, setAckUncertainty] = useState(false)
  const [ackShared, setAckShared] = useState(false)
  const [notesOpen, setNotesOpen] = useState(false)

  // The approval tab is opened synchronously by the click below (popup
  // blockers reject window.open from an async callback) and navigated once the
  // Hub returns the URL. A blocked or closed tab degrades to "Open again".
  const approvalTab = useRef<Window | null>(null)
  const start = useMutation({
    mutationFn: () =>
      startCloudInstall({
        flowId: status.flowId ?? '',
        clusterName,
        acceptAdoption,
        acknowledgeIncompleteDiscovery: ackUncertainty,
        acknowledgeSharedListener: ackShared,
      }),
    onSuccess: (st) => {
      // Only navigate to an approval the server still considers live. A cancel
      // from another tab (or a server-side failure) can land while start is in
      // flight, and opening its connectUrl anyway would present an approvable
      // request for a flow the user already stopped.
      if (st.connectUrl && st.state === 'awaiting_approval') {
        if (approvalTab.current && !approvalTab.current.closed) {
          approvalTab.current.location.href = st.connectUrl
        } else {
          window.open(st.connectUrl, '_blank', 'noopener')
        }
      } else {
        approvalTab.current?.close()
      }
      approvalTab.current = null
      onStatus(st)
    },
    onError: () => {
      approvalTab.current?.close()
      approvalTab.current = null
    },
    meta: { errorMessage: 'Could not start the Cloud connection' },
  })
  const discard = useMutation({
    mutationFn: () => cancelCloudInstall(status.flowId ?? ''),
    onSuccess: (st) => {
      onStatus(st)
      onExit()
    },
    meta: { errorMessage: 'Could not discard the connection plan' },
  })

  if (!plan) return null
  const adopt = plan.mode === 'adopt'
  const startDisabled =
    start.isPending ||
    (adopt && !acceptAdoption) ||
    (!!plan.uncertainty && !ackUncertainty) ||
    (!!plan.sharedListener && !ackShared)

  return (
    <div className="px-8 pt-6 pb-5">
      <h4 className="text-[15px] font-semibold text-theme-text-primary mb-3">
        {adopt ? 'Adopt and connect this cluster' : 'Connect this cluster'}
      </h4>
      <div className="card-inner-lg space-y-1.5 text-[12px] text-theme-text-secondary">
        <PlanRow label="Kubernetes context" value={plan.contextName} mono />
        <PlanRow label="Target" value={`namespace ${plan.namespace} · release ${plan.release}`} mono />
        {adopt ? (
          <>
            <PlanRow label="Action" value="Atomically upgrade and connect the existing Helm release" />
            <PlanRow label="Chart" value={`${plan.currentChartVersion} → ${plan.targetChartVersion}`} mono />
            <PlanRow
              label="If anything fails"
              value={`Helm rolls back to the pre-adoption release (revision ${plan.currentRevision})`}
            />
          </>
        ) : (
          <>
            <PlanRow label="Action" value="Install a new connected Radar release" />
            <PlanRow label="Chart" value={`${plan.targetChartVersion} (Radar ${plan.targetAppVersion})`} mono />
          </>
        )}
        {plan.currentImageTag && (
          <PlanRow
            label="Note"
            value={`Pinned image.tag "${plan.currentImageTag}" will be cleared so the chart's stable Radar runs`}
          />
        )}
        {plan.preservedImageRepository && (
          <PlanRow label="Image repository" value={`${plan.preservedImageRepository} (preserved)`} mono />
        )}
      </div>

      {/* Advisories come from the preflight in CLI prose (long, and usually the
          benign "namespace doesn't exist yet" note on a fresh install). Keep
          their presence visible so nobody approves blind, but don't let the
          wall of text bury the decision. */}
      {plan.advisories && plan.advisories.length > 0 && (
        <div className="mt-2.5">
          <button
            type="button"
            onClick={() => setNotesOpen((v) => !v)}
            aria-expanded={notesOpen}
            className="flex items-center gap-1.5 text-[11.5px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors"
          >
            <CollapseChevron open={notesOpen} className="w-3.5 h-3.5" />
            <Info className="w-3.5 h-3.5 shrink-0" />
            {plan.advisories.length === 1 ? '1 preflight note' : `${plan.advisories.length} preflight notes`}
          </button>
          <Collapse open={notesOpen}>
            <ul className="mt-1.5 space-y-1.5 pl-5">
              {plan.advisories.map((note) => (
                <li key={note} className="text-[11.5px] leading-snug text-theme-text-tertiary">
                  {note}
                </li>
              ))}
            </ul>
          </Collapse>
        </div>
      )}

      <label className="mt-3.5 block">
        <span className="text-[11.5px] font-medium text-theme-text-secondary">Cluster name in Radar</span>
        <input
          value={clusterName}
          onChange={(e) => setClusterName(e.target.value)}
          maxLength={80}
          className="mt-1 w-full px-2.5 py-1.5 rounded-md bg-theme-elevated border border-theme-border text-[13px] text-theme-text-primary focus:outline-none focus:border-emerald-500/60"
        />
      </label>

      {plan.sharedListener && (
        <ConsentRow checked={ackShared} onChange={setAckShared} tone="amber">
          This Radar answers beyond localhost, so anyone who can open this page could connect this cluster to
          their own Radar organization. Continue anyway.
        </ConsentRow>
      )}

      {plan.uncertainty && (
        <ConsentRow checked={ackUncertainty} onChange={setAckUncertainty} tone="amber">
          {plan.uncertainty} Continue with this target anyway.
        </ConsentRow>
      )}
      {adopt && (
        <ConsentRow checked={acceptAdoption} onChange={setAcceptAdoption} tone="emerald">
          Upgrade and connect the existing Radar installation. Sign-in and final approval happen in the
          browser before anything changes.
        </ConsentRow>
      )}

      <div className="mt-4 flex items-center gap-4">
        <button
          onClick={() => {
            // 'noopener' in the features string makes window.open return null,
            // which would lose the handle this pre-open exists to keep — sever
            // opener on the handle instead.
            const tab = window.open('about:blank', '_blank')
            if (tab) tab.opener = null
            approvalTab.current = tab
            start.mutate()
          }}
          disabled={startDisabled}
          className="px-5 py-2 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 disabled:opacity-50 disabled:cursor-not-allowed text-emerald-950 text-[13px] font-bold transition-all"
        >
          {start.isPending ? 'Starting…' : 'Continue in browser'}
        </button>
        {/* Disabled while start is in flight: cancelling there only marks the
            server flow canceling, and start's own success would still land
            afterwards — reopening the approval tab and overwriting the newer
            status with a stale one. */}
        <button
          onClick={() => discard.mutate()}
          disabled={start.isPending || discard.isPending}
          className="text-[12.5px] text-theme-text-tertiary hover:text-theme-text-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          Cancel
        </button>
      </div>
      {/* Radar has no Hub identity here — that is what the device flow is for —
       *  so this cannot assert whether an account already exists. Phrased to
       *  read correctly for a returning operator and a first-time one alike. */}
      <p className="mt-2.5 text-[11px] text-theme-text-tertiary">
        Nothing is installed yet. You'll approve this cluster in the browser, creating your account and
        organization first if you don't have one.{' '}
        {adopt ? 'Then your existing Radar is upgraded and connected.' : 'Then Radar is installed in your cluster.'}
      </p>
    </div>
  )
}

function PlanRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <span className="w-[130px] shrink-0 text-theme-text-tertiary">{label}</span>
      <span className={`min-w-0 text-theme-text-primary ${mono ? 'font-mono text-[11.5px]' : ''}`}>{value}</span>
    </div>
  )
}

function ConsentRow({
  checked,
  onChange,
  tone,
  children,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  tone: 'emerald' | 'amber'
  children: React.ReactNode
}) {
  return (
    <label className="mt-3 flex items-start gap-2 cursor-pointer">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className={tone === 'amber' ? 'mt-0.5 accent-amber-500' : 'mt-0.5 accent-emerald-500'}
      />
      <span className="text-[11.5px] leading-snug text-theme-text-secondary">{children}</span>
    </label>
  )
}

function ApprovalCard({ status, onStatus }: { status: CloudInstallStatus; onStatus: (st: CloudInstallStatus) => void }) {
  const cancel = useCancelButton(status, onStatus)
  const starting = status.state === 'starting'
  return (
    <div className="px-8 pt-6 pb-5">
      <div className="flex items-center gap-2.5 mb-3">
        <Loader2 className="w-4 h-4 animate-spin text-emerald-600 dark:text-emerald-400" />
        <h4 className="text-[15px] font-semibold text-theme-text-primary">
          {starting ? 'Opening the approval page…' : 'Waiting for browser approval'}
        </h4>
      </div>
      <p className="text-[12.5px] leading-relaxed text-theme-text-secondary mb-3.5">
        Approve connecting <b className="text-theme-text-primary">{status.clusterName}</b> in the browser tab.
        Sign-in and org setup happen there too, and this screen advances automatically.
      </p>
      {status.connectUrl && (
        <div className="card-inner flex items-center gap-2">
          <span className="flex-1 min-w-0 truncate font-mono text-[11px] text-theme-text-tertiary">
            {status.connectUrl}
          </span>
          <button
            onClick={() => window.open(status.connectUrl, '_blank', 'noopener')}
            className="shrink-0 flex items-center gap-1 text-[11.5px] font-semibold text-emerald-600 dark:text-emerald-400 hover:underline underline-offset-2"
          >
            Open again <ExternalLink className="w-3 h-3" />
          </button>
        </div>
      )}
      <div className="mt-4">{cancel}</div>
    </div>
  )
}

function ProgressCard({ status, onStatus }: { status: CloudInstallStatus; onStatus: (st: CloudInstallStatus) => void }) {
  const provisioning = status.state === 'provisioning'
  const cancel = useCancelButton(status, onStatus)
  const steps: Array<{ label: string; state: 'done' | 'active' | 'todo' }> = [
    { label: 'Approved in browser', state: 'done' },
    { label: `Installing Radar (namespace ${status.plan?.namespace ?? 'radar'})`, state: provisioning ? 'active' : 'done' },
    { label: 'Waiting for Radar to connect to Cloud', state: provisioning ? 'todo' : 'active' },
  ]
  return (
    <div className="px-8 pt-6 pb-5">
      <h4 className="text-[15px] font-semibold text-theme-text-primary mb-3.5">
        Connecting {status.clusterName}
      </h4>
      <ul className="space-y-2.5">
        {steps.map((step) => (
          <li key={step.label} className="flex items-center gap-2.5 text-[12.5px]">
            {step.state === 'done' ? (
              <Check className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
            ) : step.state === 'active' ? (
              <Loader2 className="w-4 h-4 animate-spin text-emerald-600 dark:text-emerald-400" />
            ) : (
              <span className="w-4 h-4 grid place-items-center">
                <span className="w-1.5 h-1.5 rounded-full bg-theme-border" />
              </span>
            )}
            <span className={step.state === 'todo' ? 'text-theme-text-tertiary' : 'text-theme-text-secondary'}>
              {step.label}
            </span>
          </li>
        ))}
      </ul>
      <div className="mt-4">
        {provisioning ? (
          <p className="text-[11px] text-theme-text-tertiary">
            Installing. This step completes atomically and can’t be canceled midway.
          </p>
        ) : (
          cancel
        )}
      </div>
    </div>
  )
}

function useCancelButton(status: CloudInstallStatus, onStatus: (st: CloudInstallStatus) => void) {
  const cancel = useMutation({
    mutationFn: () => cancelCloudInstall(status.flowId ?? ''),
    onSuccess: onStatus,
    meta: { errorMessage: 'Could not cancel the connection' },
  })
  return (
    <button
      onClick={() => cancel.mutate()}
      disabled={cancel.isPending}
      className="flex items-center gap-1 text-[12px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors disabled:opacity-50"
    >
      <X className="w-3.5 h-3.5" /> Cancel connection
    </button>
  )
}

function ConnectedCard({
  status,
  onStatus,
  onExit,
}: {
  status: CloudInstallStatus
  onStatus: (st: CloudInstallStatus) => void
  onExit: () => void
}) {
  const dismiss = useDismiss(status, onStatus, onExit)
  const connected = status.connected
  if (!connected) return null
  return (
    <div className="px-8 pt-6 pb-5">
      <div className="flex items-center gap-2.5 mb-3">
        <span className="w-7 h-7 rounded-full bg-emerald-500/20 grid place-items-center">
          <Check className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
        </span>
        <h4 className="text-[15px] font-semibold text-theme-text-primary">
          {status.clusterName} is connected to Radar Cloud
        </h4>
      </div>
      <p className="text-[12.5px] leading-relaxed text-theme-text-secondary mb-4">
        Radar is live in the cluster and connected to Radar Cloud. The Radar you're running keeps working,
        and the cluster is now also reachable for your team at one URL.
      </p>
      <div className="flex items-center gap-4 mb-4">
        <a
          href={connected.clusterUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="px-5 py-2 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[13px] font-bold transition-all inline-flex items-center gap-1.5"
        >
          Open in Radar Cloud <ArrowUpRight className="w-3.5 h-3.5" />
        </a>
        <button onClick={dismiss} className="text-[12.5px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors">
          Done
        </button>
      </div>
      <p className="text-[11px] text-theme-text-tertiary mb-3">
        Watch the rollout locally:{' '}
        <code className="font-mono text-[10.5px] text-theme-text-secondary">{connected.trackCommand}</code>
      </p>
      {connected.rollback && <GuidanceDetails title="How to undo this later" guidance={connected.rollback} />}
    </div>
  )
}

function FailedCard({
  status,
  onStatus,
  onExit,
}: {
  status: CloudInstallStatus
  onStatus: (st: CloudInstallStatus) => void
  onExit: () => void
}) {
  const dismiss = useDismiss(status, onStatus, onExit)
  const failure = status.failure
  if (!failure) return null
  return (
    <div className="px-8 pt-6 pb-5">
      <div className="flex items-start gap-2.5 mb-3">
        <AlertTriangle className="w-4 h-4 shrink-0 mt-1 text-amber-500" />
        <h4 className="text-[14px] font-semibold leading-snug text-theme-text-primary">{failure.message}</h4>
      </div>
      {failure.guidance && (
        <GuidanceBlock
          guidance={failure.guidance}
          showSummary={failure.guidance.summary !== failure.message}
        />
      )}
      <div className="mt-4 flex items-center gap-4">
        <button
          onClick={dismiss}
          className="px-4 py-1.5 rounded-[10px] bg-theme-elevated hover:bg-theme-hover border border-theme-border text-[12.5px] font-semibold text-theme-text-primary transition-colors"
        >
          {failure.retrySafe ? 'Start over' : 'Close'}
        </button>
      </div>
    </div>
  )
}

function useDismiss(status: CloudInstallStatus, onStatus: (st: CloudInstallStatus) => void, onExit: () => void) {
  const dismiss = useMutation({
    mutationFn: () => dismissCloudInstall(status.flowId ?? ''),
    onSuccess: (st) => {
      onStatus(st)
      onExit()
    },
    onError: (err) => {
      // 410: the flow is already gone — another tab dismissed it, or the
      // server restarted. Nothing is left to dismiss, so exiting IS the
      // requested outcome; erroring would leave a card whose only button
      // fails forever. Push idle into the shared status too (the same shape
      // the success path receives from the server): terminal states are not
      // polled, so a cached one would re-attach this dead card on reopen.
      if (err instanceof ApiError && err.status === 410) {
        onStatus({ state: 'idle' })
        onExit()
        return
      }
      showApiError('Could not dismiss the connection flow', err instanceof Error ? err.message : undefined)
    },
    // No meta.errorMessage: the global handler would also toast the 410
    // already-gone case, which this mutation treats as success.
  })
  return () => dismiss.mutate()
}

function GuidanceBlock({
  guidance,
  showSummary,
}: {
  guidance: CloudInstallRecoveryGuidance
  // Suppressed where the surrounding UI already states the summary — the
  // failure headline, or the rollback disclosure's own title.
  showSummary?: boolean
}) {
  return (
    <div className="card-inner-lg space-y-2 text-[11.5px] leading-relaxed text-theme-text-secondary">
      {showSummary && guidance.summary && (
        <p className="font-semibold text-theme-text-primary">{guidance.summary}</p>
      )}
      {guidance.lines?.map((line) => <p key={line}>{line}</p>)}
      {guidance.inspect && guidance.inspect.length > 0 && (
        <pre className="p-2 rounded-md bg-theme-elevated overflow-x-auto font-mono text-[10.5px] text-theme-text-primary">
          {guidance.inspect.join('\n')}
        </pre>
      )}
      {guidance.clusterUrl && (
        <a
          href={guidance.clusterUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400 hover:underline underline-offset-2"
        >
          Open in Radar Cloud <ExternalLink className="w-3 h-3" />
        </a>
      )}
    </div>
  )
}

function GuidanceDetails({ title, guidance }: { title: string; guidance: CloudInstallRecoveryGuidance }) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex items-center gap-1.5 text-[11.5px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors"
      >
        <CollapseChevron open={open} className="w-3.5 h-3.5" />
        {title}
      </button>
      <Collapse open={open}>
        <div className="mt-2">
          <GuidanceBlock guidance={guidance} />
        </div>
      </Collapse>
    </div>
  )
}
