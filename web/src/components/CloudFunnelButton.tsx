import { useEffect, useId, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Bell, Check, Globe, History, ShieldCheck, Sparkles, Users, X } from 'lucide-react'
import { Collapse, CollapseChevron } from '@skyhook-io/k8s-ui/components/ui/Collapse'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { Tooltip } from './ui/Tooltip'
import { CloudConnectFlow } from './CloudConnectFlow'
import { SelfManagedStart } from './SelfManagedStart'
import {
  type Handoff,
  exitFor,
  handoffForBlocked,
  handoffForPrepareError,
  isHandoffOutcome,
  signupUrlFor as buildSignupUrl,
} from './cloudConnectHandoff'
import { showApiError } from './ui/Toast'
import { useConnection } from '../context/ConnectionContext'
import {
  ApiError,
  cloudInstallActive,
  type CloudConnectSelf,
  type CloudInstallConnectedRadar,
  type CloudInstallBlocked,
  type CloudInstallStatus,
  prepareCloudInstall,
  useCapabilities,
  useCloudConnectInfo,
  useCloudConnectSelf,
  useCloudInstallDiscover,
  useCloudInstallStatus,
  useClusterInfo,
} from '../api/client'

// OSS → Cloud funnel: a quiet globe button in the top bar that opens a modal
// pitching Radar Cloud by outcome: the Radar that is awake when you are not.
// It watches, remembers, tells the team and investigates; fleet view is the
// scaffolding underneath, not the headline. Two lanes (capabilities.cloudConnect): "driver" runs
// the in-product connect flow against this server; "wizard" links to the Hub's
// connect wizard.
//
// The only outbound call is the Hub's own copy, fetched when the dialog opens
// (never on a poll or a timer) and falling back per-field to the constants
// below. The request names the lane the footer renders and where this Radar
// runs, both closed enums. The links into the Hub carry utm_content naming
// the link that was clicked and, after an in-app attempt that did not connect,
// radar_outcome saying what happened; see cloudConnectHandoff.ts.
const FALLBACK_APP_URL = 'https://app.radarhq.io'

// Rendered until (or unless) the Hub states its own. Keeping the compiled-in
// copy as the fallback means an unreachable Hub, a self-hosted one, or an
// offline laptop all render exactly what Radar rendered before this fetch
// existed — the dialog never waits on the network and never shows a gap.
// Ordered by what matters most when deciding to connect a cluster: network
// posture, then reversibility, then attestation, then billing. Deliberately
// no data-locality claim — retention and alerts work because cluster data
// flows out through the relay, so the list describes the connection model
// instead. The SOC 2 line comes from the Hub's live list, which owns its
// wording; here it is only the offline fallback.
const DEFAULT_ASSURANCES = [
  'Secure outbound-only tunnel',
  'Disconnect and delete your data anytime',
  'SOC 2 Type II',
  '3 clusters free',
]
// Other OSS surfaces (a GitOps app that deploys to another cluster, say)
// point at Radar Cloud by asking this button to open its dialog, so there
// is one pitch and one flow. The button is mounted whenever Radar runs
// standalone; embedded hosts never mount it and never dispatch this.
const OPEN_EVENT = 'radar:open-cloud-funnel'

export function openCloudFunnel() {
  window.dispatchEvent(new Event(OPEN_EVENT))
}
const ABOUT_URL = 'https://radarhq.io/about'
const BENCHMARK_URL = 'https://radarhq.io/benchmark'
const PRICING_URL = 'https://radarhq.io/pricing'
const SEEN_KEY = 'radar.cloudFunnel.seen'

// localStorage access can throw (SecurityError) where storage is denied —
// sandboxed embeds, some privacy modes. This button mounts in the top bar
// outside the main error boundary, so an uncaught throw would take down the
// chrome; degrade to "not seen" / no-op persistence instead.
function readSeen(): boolean {
  if (typeof window === 'undefined') return false
  try {
    return window.localStorage.getItem(SEEN_KEY) === 'true'
  } catch {
    return false
  }
}

function markSeen() {
  try {
    window.localStorage.setItem(SEEN_KEY, 'true')
  } catch {
    // Storage denied — the ping dot reappears on next mount; harmless.
  }
}

export function CloudFunnelButton() {
  const [open, setOpen] = useState(false)
  const [seen, setSeen] = useState(readSeen)
  const [inFlowView, setInFlowView] = useState(false)
  const [selfManaged, setSelfManaged] = useState(false)
  const [blocked, setBlocked] = useState<CloudInstallBlocked | null>(null)
  // Set once an in-app attempt has ended without connecting, naming what
  // happened (a flow failure kind, a blocked plan, a canceled plan, or the
  // prepare call itself failing) and whether it is worth trying again. It
  // relabels the CTA "Try again" for a retryable failure and lets the browser
  // link say what the person is coming from. Survives closing the modal (the
  // outcome belongs to the cluster, not the dialog session) but not a reload,
  // and is reset below when the kubeconfig context changes — it must never
  // describe a cluster the user has switched away from.
  const [handoff, setHandoff] = useState<Handoff | null>(null)
  const prepareFailed = handoff?.retryable ?? false

  const capabilities = useCapabilities()
  const clusterInfo = useClusterInfo()
  const lane = capabilities.data?.cloudConnect?.lane ?? 'wizard'
  const clusterConnected = useConnection().connection.state === 'connected'
  const pitchLane = lane === 'driver' && !clusterConnected ? 'wizard' : lane
  const appUrl = capabilities.data?.cloudConnect?.appUrl || FALLBACK_APP_URL
  // utm_content names the link that was clicked. It travels only in the link
  // the user opens; Radar sends nothing on its own. Only the blocked card may
  // deep-link the install page (see exitFor); the pitch buttons and the
  // footer link go to signup.
  const signupUrl = buildSignupUrl(appUrl, 'wizard-signup-button')

  // Only while the dialog is open — never on the capabilities poll. The Hub
  // learns that someone opened it, which is congruent with what the dialog is
  // for; it must not learn that Radar is merely running.
  const connectInfo = useCloudConnectInfo(capabilities.data?.cloudConnect?.apiUrl, open, {
    lane: pitchLane,
    mode: capabilities.data?.deployment?.mode,
  })

  // In-cluster Radar can't install its own connection, but it knows exactly
  // which install it is — so the wizard link can carry the real target, and a
  // GitOps-owned install can be told the imperative command isn't for it.
  const inCluster = capabilities.data?.deployment?.mode === 'in-cluster'
  const self = useCloudConnectSelf(open && inCluster)

  // Each time the dialog opens, look for a Radar in the cluster that already
  // carries Cloud settings — installed by a colleague after a handoff, or from
  // another machine. The plan would refuse to install over it; better to say
  // "already connected" and point at it than to offer a click that ends blocked.
  const discovered = useCloudInstallDiscover(open && lane === 'driver' && clusterConnected, clusterInfo.data?.context)
  // The server ranks these: one it can link to first. A failed lookup is
  // not a verdict — the CTA returns and the plan does its own inspection —
  // and neither is the previous opening's answer: this component outlives
  // the dialog, so a reopen refetches over cached data, and the gate must
  // hold for that refetch too (isFetching, not isPending) while a refetch
  // that fails must not keep showing what it found last time.
  const alreadyConnected = !clusterConnected || discovered.isError ? undefined : discovered.data?.connected[0]
  const discoverPending = lane === 'driver' && discovered.isFetching

  // The flow is server-owned: polling here both drives the live progress view
  // and re-attaches to an ongoing flow after a reload or modal close.
  const flowStatus = useCloudInstallStatus(lane === 'driver')
  const flow = flowStatus.data
  const flowLive = cloudInstallActive(flow?.state) || flow?.state === 'connected' || flow?.state === 'failed'

  const queryClient = useQueryClient()
  const applyStatus = (st: CloudInstallStatus) => {
    if (st.state !== 'blocked') queryClient.setQueryData(['cloud-install-status'], st)
    flowStatus.invalidate()
  }

  const prepare = useMutation({
    mutationFn: prepareCloudInstall,
    onSuccess: (st) => {
      if (st.state === 'blocked' && st.blocked) setBlocked(st.blocked)
      else applyStatus(st)
    },
    onError: (err) => {
      // A single-flight 409 is not a failure: its body IS the live flow (one
      // started in another tab, or before this tab's status cache refreshed).
      // Attach to it rather than showing an error over a running install.
      const live = err instanceof ApiError && err.status === 409 ? (err.data as CloudInstallStatus | undefined) : undefined
      if (live?.state) {
        applyStatus(live)
        return
      }
      // Anything else failed before a flow existed. Return to the pitch rather
      // than leaving the flow view armed, where a later status change would
      // pull the user into a screen they did not ask for.
      exitFlow(handoffForPrepareError(err))
      showApiError('Could not inspect this cluster for Cloud connect', err instanceof Error ? err.message : undefined)
    },
    // No meta.errorMessage: the global handler cannot tell a single-flight 409
    // (a successful attach) from a real failure, and would report failure over
    // a running install. Toast explicitly on the paths that are failures.
  })

  const openModal = () => {
    setOpen(true)
    setSelfManaged(false)
    setSeen(true)
    markSeen()
    // Re-open lands on a live flow if one is running.
    if (lane === 'driver' && flowLive) setInFlowView(true)
  }

  const startConnect = () => {
    setBlocked(null)
    setHandoff(null)
    setInFlowView(true)
    prepare.mutate()
  }

  // A flow that ended without connecting returns to a pitch whose CTA must
  // not read like a first attempt, and whose browser link knows what happened.
  // The caller passes the outcome because dismissing already overwrote the
  // status by this point. Every exit sets it, null included, so a connected
  // flow clears what an earlier attempt left behind.
  const exitFlow = (outcome: Handoff | null = null) => {
    setHandoff(outcome)
    setInFlowView(false)
    setBlocked(null)
  }

  // What the person is coming from. A blocked plan is read from local state,
  // not the status: the status query keeps serving its last polled value
  // (idle, usually) while the blocked view is up. A blocked plan counts as an
  // attempt: the user tried the in-app path and was refused, and the wizard
  // is the right next step for a GitOps-owned install. A failed flow carries
  // the server's kind and its own verdict on retrying — the failed card
  // already honors retrySafe, and the pitch must not contradict it two clicks
  // later. Leaving a live plan is the user canceling it, not a failure. Only
  // a connected flow, or no flow at all, leaves nothing to carry.
  const outcomeOf = (st: CloudInstallStatus): Handoff | null => {
    if (blocked) return handoffForBlocked(blocked.reason, blocked.attempted)
    if (st.state === 'failed') {
      return {
        outcome: isHandoffOutcome(st.failure?.kind) ? st.failure.kind : 'failure_kind_unknown',
        retryable: st.failure?.retrySafe ?? false,
      }
    }
    if (st.state === 'blocked' && st.blocked) return handoffForBlocked(st.blocked.reason, st.blocked.attempted)
    if (st.state === 'connected' || st.state === 'idle') return null
    return { outcome: 'install_plan_canceled', retryable: false }
  }

  useEffect(() => {
    window.addEventListener(OPEN_EVENT, openModal)
    return () => window.removeEventListener(OPEN_EVENT, openModal)
  })

  // Re-attach to a server-owned flow whenever one is observed while the modal
  // is open — the status query may resolve after openModal ran.
  useEffect(() => {
    if (open && lane === 'driver' && flowLive) setInFlowView(true)
  }, [open, lane, flowLive])

  // handoff and a blocked plan outlive the dialog but must not outlive the
  // cluster they describe: a context switch swaps every query cache, yet this
  // component stays mounted, so without the reset cluster A's outcome would
  // relabel the CTA (and the link) for cluster B, and a blocked view left
  // armed would reopen on B with A's refusal, feeding it back into the
  // outcome on Back.
  const contextName = clusterInfo.data?.context
  useEffect(() => {
    setHandoff(null)
    setBlocked(null)
  }, [contextName])

  // The server owns the "nothing to pitch" decision: an already-tunneled
  // deployment gets no cloudConnect capability at all. Waiting for
  // capabilities (rather than defaulting to visible) keeps the funnel from
  // flashing at a connected cluster's operator before that answer arrives.
  if (!capabilities.data?.cloudConnect) return null

  const showFlow = inFlowView && (blocked !== null || prepare.isPending || flowLive)
  // The prepare POST can take tens of seconds (chart download + preflight);
  // until the status poll observes the server-side flow, synthesize the
  // preparing state so the modal never renders empty. A blocked result gets
  // the same treatment: it lives only in local state (never seeded into the
  // status query), so a slow or failed first /status fetch must not drop the
  // explanation back to the pitch.
  const flowForView: CloudInstallStatus | undefined =
    prepare.isPending && !flowLive
      ? { state: 'preparing' }
      : (flow ?? (blocked ? { state: 'blocked', blocked } : undefined))

  return (
    <>
      {/* Tooltip is suppressed while the modal is open — it portals above the
          modal backdrop and would otherwise paint on top of the dialog. */}
      <Tooltip content="Radar Cloud: awake when you're not" delay={100} position="bottom" disabled={open}>
        <button
          onClick={openModal}
          aria-label="Radar Cloud"
          aria-haspopup="dialog"
          className="relative p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <Globe className="w-4 h-4" />
          {cloudInstallActive(flow?.state) ? (
            <span className="absolute top-0.5 right-0.5 w-[7px] h-[7px] rounded-full bg-emerald-500 animate-pulse motion-reduce:animate-none" />
          ) : (
            !seen && (
              <span className="absolute top-0.5 right-0.5 w-[7px] h-[7px] rounded-full bg-emerald-500">
                <span className="absolute -inset-[3px] rounded-full border border-emerald-500/70 animate-ping motion-reduce:animate-none" />
              </span>
            )
          )}
        </button>
      </Tooltip>

      <DialogPortal
        open={open}
        onClose={() => setOpen(false)}
        className="w-[580px] max-w-full max-h-[calc(100vh-2rem)] overflow-hidden flex flex-col"
      >
        <button
          onClick={() => setOpen(false)}
          aria-label="Close"
          className="absolute top-3.5 right-3.5 z-10 p-1.5 rounded-md text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
        >
          <X className="w-4 h-4" />
        </button>

        {/* Only the body scrolls on short viewports — the close control and
            the footer CTA stay pinned so they never scroll away. This matters
            more with the connect flow, whose plan card is the tallest state. */}
        {showFlow && flowForView ? (
          <div className="min-h-0 overflow-y-auto">
            <div className="px-8 pt-7">
              <Eyebrow />
            </div>
            <CloudConnectFlow
              status={flowForView}
              blocked={blocked}
              exit={exitFor(appUrl, 'driver-blocked-card-browser-link', outcomeOf(flowForView), clusterInfo.data?.context)}
              where={{ context: clusterInfo.data?.context, cluster: clusterInfo.data?.cluster }}
              onStatus={applyStatus}
              onExit={() => exitFlow(outcomeOf(flowForView))}
            />
          </div>
        ) : selfManaged ? (
          <>
            <div className="shrink-0 px-8 pt-7">
              <Eyebrow />
            </div>
            <SelfManagedStart appUrl={appUrl} onBack={() => setSelfManaged(false)} />
          </>
        ) : (
          <>
            <div className="min-h-0 overflow-y-auto">
              <PitchBody lane={pitchLane} freeTier={connectInfo.data?.freeTier} />
            </div>
            <ModalFooter
              lane={lane}
              clusterConnected={clusterConnected}
              signupUrl={signupUrl}
              // One link name whether or not an attempt preceded the click; the
              // outcome, when present, is what says an attempt happened.
              driverBrowserUrl={buildSignupUrl(appUrl, 'driver-footer-browser-link', handoff)}
              prepareFailed={prepareFailed}
              // A prepare error's reason, kept on the pitch after its toast is gone.
              prepareError={handoff?.detail}
              assurances={connectInfo.data?.assurances}
              notice={connectInfo.data?.notice}
              self={inCluster ? self.data : undefined}
              clusterName={clusterInfo.data?.context}
              alreadyConnected={alreadyConnected}
              connectedCount={alreadyConnected ? (discovered.data?.connected.length ?? 0) : 0}
              discoverPending={discoverPending}
              clustersUrl={`${appUrl}/clusters`}
              // Also covers the capabilities query: until it resolves, lane
              // defaults to wizard and Radar does not yet know it is
              // in-cluster, so the CTA would escape before classification.
              selfLoading={inCluster && self.isPending}
              onConnect={startConnect}
              onSelfManaged={() => setSelfManaged(true)}
              onLater={() => setOpen(false)}
            />
          </>
        )}
      </DialogPortal>
    </>
  )
}

// The Hub's list renders verbatim — no client-side additions, so the Hub owns
// the wording and can update it without a binary release.
function assuranceItems(fromHub?: string[]): string[] {
  return fromHub?.length ? fromHub : DEFAULT_ASSURANCES
}

function RadarSweep() {
  return (
    <div
      aria-hidden
      className="relative w-[30px] h-[30px] rounded-full overflow-hidden shrink-0 border border-emerald-400/60 shadow-[0_0_12px_rgba(16,185,129,0.35)]"
      style={{ background: 'radial-gradient(circle at 50% 50%, #072920 0%, #03180f 70%, #010a06 100%)' }}
    >
      <div className="absolute inset-[16%] rounded-full border border-emerald-600/50" />
      <div
        className="absolute inset-0 rounded-full animate-[spin_4s_linear_infinite] motion-reduce:animate-none"
        style={{ background: 'conic-gradient(from 0deg, rgba(167,243,208,0.85) 0deg, rgba(16,185,129,0.25) 40deg, transparent 90deg)' }}
      />
    </div>
  )
}

function Eyebrow() {
  return (
    <div className="flex items-center gap-3 mb-5">
      <RadarSweep />
      <span className="font-mono text-[10.5px] tracking-[0.16em] uppercase text-emerald-600 dark:text-emerald-400">Radar Cloud</span>
    </div>
  )
}

function ModalFooter({
  lane,
  clusterConnected,
  signupUrl,
  driverBrowserUrl,
  prepareFailed,
  prepareError,
  assurances,
  notice,
  self,
  selfLoading,
  clusterName,
  alreadyConnected,
  connectedCount = 0,
  discoverPending = false,
  clustersUrl,
  onConnect,
  onSelfManaged,
  onLater,
}: {
  lane: 'driver' | 'wizard'
  clusterConnected: boolean
  signupUrl: string
  // Same destination as signupUrl, distinct utm_content, and the outcome of
  // an in-app attempt when one preceded this render.
  driverBrowserUrl: string
  prepareFailed: boolean
  prepareError?: string
  // Live copy from the Hub; undefined until (or unless) it arrives.
  assurances?: string[]
  notice?: string
  // Present only in-cluster: what this Radar knows about its own install.
  self?: CloudConnectSelf
  // True while in-cluster self-classification is still in flight.
  selfLoading?: boolean
  // The kubeconfig context, named under the CTA so the click is about a
  // specific cluster — kept out of the button, whose width must not depend
  // on the name.
  clusterName?: string
  // Driver lane: a Radar in the cluster that already carries Cloud settings.
  alreadyConnected?: CloudInstallConnectedRadar
  connectedCount?: number
  // True while that lookup is in flight: a click now would start the very
  // plan the lookup exists to pre-empt, and the flow view would then hide
  // its answer.
  discoverPending?: boolean
  // The configured Hub's clusters list — where to look when an install's
  // settings say it is connected but not where.
  clustersUrl?: string
  onConnect: () => void
  onSelfManaged: () => void
  onLater: () => void
}) {
  const gitops = self?.ownership === 'gitops'
  const ambiguous = self?.ownership === 'ambiguous'
  // The server decides who gets a link: it withholds wizardUrl whenever the
  // handoff must inspect before it acts (ambiguous ownership, or GitOps
  // evidence it could not verify). A GitOps install with a link goes to the
  // wizard's Argo/Flux tab, which generates a values patch for the repo rather
  // than an imperative command the controller would revert.
  const cliOnly = (gitops || ambiguous) && !self?.wizardUrl
  // Until classification resolves we cannot know which lane applies, and a
  // fast click would escape to signup before we could route this install.
  const selfPending = selfLoading === true
  return (
    <div className="shrink-0 px-8 py-5 bg-theme-base border-t border-theme-border">
      {self && self.ownership !== 'unknown' && (
        <div className="mb-3.5 card-inner p-3 text-[12px] leading-relaxed text-theme-text-secondary">
          {ambiguous ? (
            <>
              Radar found conflicting management metadata on this install, so it can't say whether a Helm
              upgrade or a repository change is the right move. Run{' '}
              <code className="font-mono text-[11px]">radar cloud install</code> from a machine with
              kubectl. It inspects the release and refuses rather than guessing.
            </>
          ) : gitops ? (
            <>
              This Radar is managed by{' '}
              <b className="text-theme-text-primary">{self.controller || 'a GitOps controller'}</b>, so
              connecting it is a values change in your repository; an imperative upgrade would be reverted.{' '}
              {cliOnly ? (
                <>
                  Radar found that evidence but couldn't confirm it against the live object, so run{' '}
                  <code className="font-mono text-[11px]">radar cloud install</code> from a machine with
                  kubectl. It inspects the release before generating anything.
                </>
              ) : (
                <>
                  The wizard generates the values patch for that controller, plus the one command that
                  creates the token Secret. The token never goes into your repository.
                </>
              )}
            </>
          ) : (
            <>
              Detected this install: namespace{' '}
              <code className="font-mono text-[11px] text-theme-text-primary">{self.namespace}</code>, release{' '}
              <code className="font-mono text-[11px] text-theme-text-primary">{self.release}</code>. The
              wizard will target it directly.
            </>
          )}
        </div>
      )}
      {notice && (
        <div className="mb-3.5 card-inner p-3 text-[12px] leading-relaxed text-theme-text-secondary">{notice}</div>
      )}
      {lane === 'driver' && alreadyConnected && (
        <div className="mb-3.5 card-inner p-3 text-[12px] leading-relaxed text-theme-text-secondary">
          <span className="font-semibold text-theme-text-primary">This cluster is already connected to Radar Cloud.</span>{' '}
          Found release{' '}
          <code className="font-mono text-[11px] text-theme-text-primary">{alreadyConnected.release || alreadyConnected.deployment}</code>{' '}
          in namespace <code className="font-mono text-[11px] text-theme-text-primary">{alreadyConnected.namespace}</code>{' '}
          {alreadyConnected.hubHost ? (
            <>
              configured for a Radar Cloud at{' '}
              <code className="font-mono text-[11px] text-theme-text-primary">{alreadyConnected.hubHost}</code> — open that one to see
              this cluster.
            </>
          ) : alreadyConnected.clusterUrl ? (
            <>configured to connect. Open Radar Cloud to see it; if it isn’t there, its page shows the recovery options.</>
          ) : (
            <>
              with Cloud settings Radar can’t read from here. Look for it in Radar Cloud; if it isn’t there, an admin can
              recover the pairing with <code className="font-mono text-[11px] text-theme-text-primary">radar cloud status</code>.
            </>
          )}
          {connectedCount > 1 && <> {connectedCount - 1} more install{connectedCount > 2 ? 's' : ''} carry Cloud settings too.</>}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2.5">
        {lane === 'driver' && alreadyConnected ? (
          // Another Hub is named, not linked: the link would open ours.
          !alreadyConnected.hubHost && (
            <a
              href={alreadyConnected.clusterUrl || clustersUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="whitespace-nowrap px-6 py-2.5 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[14px] font-bold shadow-[0_0_22px_rgba(16,185,129,0.35)] hover:shadow-[0_0_30px_rgba(16,185,129,0.5)] hover:-translate-y-px transition-all"
            >
              {alreadyConnected.clusterUrl ? 'Open in Radar Cloud' : 'Open Radar Cloud'}
            </a>
          )
        ) : lane === 'driver' && clusterConnected ? (
          <>
            <button
              onClick={onConnect}
              disabled={discoverPending}
              className={`whitespace-nowrap px-6 py-2.5 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[14px] font-bold shadow-[0_0_22px_rgba(16,185,129,0.35)] hover:shadow-[0_0_30px_rgba(16,185,129,0.5)] hover:-translate-y-px transition-all ${discoverPending ? 'opacity-60 pointer-events-none' : ''}`}
            >
              {/* Trailing ellipsis: further input follows the click — the
                  inspect step and a plan the user approves in the browser. */}
              {prepareFailed ? 'Try again' : 'Connect this cluster…'}
            </button>
            {/* Always visible: the browser wizard is a different workflow, not
                a recovery path — install-averse operators need the door before
                anything fails, or they close the modal instead. */}
            <a
              href={driverBrowserUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="whitespace-nowrap text-[12.5px] text-theme-text-secondary hover:text-theme-text-primary hover:underline underline-offset-2 transition-colors"
            >
              or set up in the browser
            </a>
          </>
        ) : cliOnly ? null : (
          <a
            href={lane === 'driver' ? driverBrowserUrl : self?.wizardUrl || signupUrl}
            aria-disabled={selfPending}
            target="_blank"
            rel="noopener noreferrer"
            onClick={(e) => { if (selfPending) e.preventDefault() }}
            className={`px-5 py-2 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[13.5px] font-bold shadow-[0_0_22px_rgba(16,185,129,0.35)] hover:shadow-[0_0_30px_rgba(16,185,129,0.5)] hover:-translate-y-px transition-all ${selfPending ? 'opacity-60 pointer-events-none' : ''}`}
          >
            {lane === 'driver' ? 'Continue in Radar Cloud' : self?.ownership === 'helm' || gitops ? 'Connect this cluster' : 'Try Cloud free'}
          </a>
        )}
        {/* A door for the teams that cannot send cluster data to a hosted
            service, shown beside the CTA rather than inside the disclosure:
            they close the dialog before they would ever expand it. */}
        {!(lane === 'driver' && alreadyConnected) && (
          <button
            type="button"
            onClick={onSelfManaged}
            className="whitespace-nowrap px-4 py-2 rounded-[10px] border border-theme-border bg-theme-surface hover:bg-theme-hover text-theme-text-primary text-[13px] font-medium transition-colors"
          >
            {/* Trailing ellipsis: the steps come before any sign-up. */}
            Run Radar Cloud yourself…
          </button>
        )}
        <button onClick={onLater} className="ml-auto whitespace-nowrap text-[12px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors">
          {lane === 'driver' && alreadyConnected ? 'Close' : 'Maybe later'}
        </button>
      </div>
      {/* The last attempt's stop, when Radar could not even inspect the
          cluster: the toast that first said so is gone in seconds, and "Try
          again" alone does not say what to try again for. The footer link
          above is the way around it. */}
      {lane === 'driver' && prepareError && (
        <p className="mt-2.5 text-[11.5px] leading-relaxed text-amber-600 dark:text-amber-400">
          Radar couldn’t inspect this cluster: {prepareError}
        </p>
      )}
      {/* Mechanics, not marketing: a falsifiable claim the plan card then
          fulfills. Sits next to the button whose click it de-risks. */}
      {lane === 'driver' && clusterConnected && !alreadyConnected && (
        <p className="mt-2.5 text-[11px] leading-relaxed text-theme-text-tertiary">
          Nothing installs on click. Radar inspects{' '}
          {clusterName ? <span className="text-theme-text-secondary">{clusterName}</span> : 'the cluster'} and shows
          you a plan; you approve it in the browser before anything changes.
        </p>
      )}
      {/* A 2-column grid, not flex-wrap: the long data-locality chip cannot
          share a single row with the other three at this width, and flex
          wrapping strands it as a 3+1 orphan. Two balanced columns read as a
          designed layout at any chip length the Hub sends. */}
      <div className="mt-4 grid grid-cols-2 gap-x-3 gap-y-1.5 text-[11px] text-theme-text-tertiary">
        {assuranceItems(assurances).map((item) => (
          <span key={item} className="flex items-center gap-1">
            <Check className="w-3 h-3 shrink-0 text-emerald-600 dark:text-emerald-400" />
            {item}
          </span>
        ))}
      </div>
    </div>
  )
}

// The detail sits behind a disclosure so the first screen stays short.
function PitchBody({ lane, freeTier }: { lane: 'driver' | 'wizard'; freeTier?: string }) {
  const [moreOpen, setMoreOpen] = useState(false)
  const moreId = useId()
  // Hub-served prose fragment; the compiled fallback carries the same
  // staleness trade as DEFAULT_ASSURANCES (rendered only when the Hub is
  // unreachable or predates the field).
  const freeLine = freeTier || 'free for 3 clusters'
  // lead is the scannable anchor (medium, primary); rest stays secondary.
  // Ordered by what the OSS binary cannot do once the laptop closes: watch,
  // remember, tell the team, investigate. Fleet view and the record come
  // last: they are where those land, not why someone connects.
  const highlights = [
    { icon: Bell, lead: 'Watches', rest: ': Slack alerts when something breaks, even with Radar closed' },
    { icon: History, lead: 'Remembers', rest: ': up to 30 days of what changed, past any restart' },
    { icon: Users, lead: 'Tells your team', rest: ": one link, no kubeconfig; your RBAC still applies" },
    { icon: Sparkles, lead: 'Investigates', rest: ': a root cause with evidence, no CLI or API key' },
    { icon: Globe, lead: 'Sees the whole fleet', rest: ': every cluster in one URL' },
    { icon: ShieldCheck, lead: 'Keeps the record', rest: ': an audit log on paid plans; SSO and SCIM on Enterprise' },
  ]
  return (
    <div className="px-8 pt-7 pb-2">
      <Eyebrow />
      <h3 className="text-[22px] font-semibold leading-tight tracking-tight text-theme-text-primary mb-3">
        Radar, awake when you're not.
      </h3>
      <p className="text-[14px] leading-relaxed text-theme-text-secondary mb-5">
        Connect this cluster and Radar keeps working after you close the laptop.
        <br />
        The Radar you're running{' '}
        <b className="text-theme-text-primary font-semibold">stays free and open source, always.</b>
      </p>
      <ul className="space-y-2 mb-4">
        {highlights.map(({ icon: Icon, lead, rest }) => (
          <li key={lead} className="flex items-start gap-2.5 text-[13px] leading-relaxed text-theme-text-secondary">
            <Icon className="w-4 h-4 shrink-0 mt-[3px] text-emerald-600 dark:text-emerald-400" />
            <span>
              <span className="font-medium text-theme-text-primary">{lead}</span>
              {rest}
            </span>
          </li>
        ))}
      </ul>
      {/* Underlined on purpose: at the bullet list's own color and size, and
          with a leading glyph, it otherwise reads as one more bullet. */}
      <button
        type="button"
        onClick={() => setMoreOpen((v) => !v)}
        aria-expanded={moreOpen}
        aria-controls={moreId}
        className="flex items-center gap-1.5 mt-5 mb-3 text-[12.5px] text-theme-text-secondary underline underline-offset-2 decoration-theme-border hover:text-theme-text-primary transition-colors"
      >
        <CollapseChevron open={moreOpen} className="w-3.5 h-3.5" />
        How it works and what it costs
      </button>
      <Collapse open={moreOpen}>
        <div id={moreId} className="pt-1 pb-3 pl-[18px] space-y-3.5">
          <section>
            {/* No heading: the disclosure's own label already names this one. */}
            <p className="text-[12px] leading-relaxed text-theme-text-secondary">
              {lane === 'driver'
                ? 'Setup runs here in the app: Radar is installed in your cluster and connects outward to Radar Cloud. You review the plan and approve in your browser before anything is installed.'
                : 'Radar runs in your cluster and connects outward to Radar Cloud. You approve the connection before anything is installed.'}
            </p>
          </section>
          <section>
            <h4 className="text-[12.5px] font-semibold text-theme-text-primary mb-0.5">Investigations</h4>
            <p className="text-[12px] leading-relaxed text-theme-text-secondary">
              Hosted investigations run inside Radar Cloud on a monthly budget that every plan includes, so
              there is no CLI to install and no API key to bring. In our public benchmark, an agent working
              through Radar's tools reached a correct root cause in a median 41 seconds and got 91% of faults
              right within five minutes.{' '}
              <a href={BENCHMARK_URL} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
                See the benchmark →
              </a>
            </p>
          </section>
          <section>
            <h4 className="text-[12.5px] font-semibold text-theme-text-primary mb-0.5">What it costs</h4>
            <p className="text-[12px] leading-relaxed text-theme-text-secondary">
              Radar Cloud is {freeLine}, investigations included. The paid plans beyond that are priced per
              cluster, never per seat, and are what keep the lights on. The Radar you're running stays
              Apache&nbsp;2.0 either way: every feature, forever.{' '}
              <a href={PRICING_URL} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
                See pricing →
              </a>
            </p>
          </section>
          <section>
            <h4 className="text-[12.5px] font-semibold text-theme-text-primary mb-0.5">Who's behind it</h4>
            <p className="text-[12px] leading-relaxed text-theme-text-secondary">
              Radar is built in the open and run by Skyhook, a CNCF Silver member and a small team of
              humans, the kind you can actually talk to.{' '}
              <a href={ABOUT_URL} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
                Meet us →
              </a>
            </p>
          </section>
        </div>
      </Collapse>
      <div className="mb-5 border-l-2 border-emerald-500/40 pl-3.5">
        <p className="text-[12px] leading-relaxed text-theme-text-secondary">
          Don't need Radar Cloud right now? That's fine. What you're running is already a full product,
          not a demo. We're here if you ever do.
        </p>
      </div>
    </div>
  )
}
