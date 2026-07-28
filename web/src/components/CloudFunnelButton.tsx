import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useMutation } from '@tanstack/react-query'
import { Bell, Check, Globe, History, Sparkles, Users, X } from 'lucide-react'
import { Tooltip } from './ui/Tooltip'
import { CloudConnectFlow } from './CloudConnectFlow'
import {
  cloudInstallActive,
  type CloudInstallBlocked,
  prepareCloudInstall,
  useCapabilities,
  useCloudInstallStatus,
} from '../api/client'

// OSS → Cloud funnel: a quiet globe button in the top bar that opens a modal
// pitching Radar Cloud. Two lanes (capabilities.cloudConnect, see
// docs/cloud-connect.md): "driver" runs the in-product connect flow against
// this server; "wizard" links to the Hub's connect wizard. Ships dark by
// design: no impressions, no remote config — conversion is measured on the
// receiving end (utm_source on wizard links, Hub funnel events on the driver).
const FALLBACK_APP_URL = 'https://app.radarhq.io'
const SIGNUP_QUERY = '?utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal'
const ABOUT_URL = 'https://www.radarhq.io/about'
const SELF_HOSTED_DOCS_URL = 'https://radarhq.io/docs/cloud/self-hosted/'

export function CloudFunnelButton() {
  const [open, setOpen] = useState(false)
  const [seen, setSeen] = useState(() => localStorage.getItem('radar.cloudFunnel.seen') === 'true')
  const [inFlowView, setInFlowView] = useState(false)
  const [blocked, setBlocked] = useState<CloudInstallBlocked | null>(null)
  const closeRef = useRef<HTMLButtonElement>(null)

  const capabilities = useCapabilities()
  const lane = capabilities.data?.cloudConnect?.lane ?? 'wizard'
  const appUrl = capabilities.data?.cloudConnect?.appUrl || FALLBACK_APP_URL
  const signupUrl = `${appUrl}/signup${SIGNUP_QUERY}`

  // The flow is server-owned: polling here both drives the live progress view
  // and re-attaches to an ongoing flow after a reload or modal close.
  const flowStatus = useCloudInstallStatus(lane === 'driver')
  const flow = flowStatus.data
  const flowLive = cloudInstallActive(flow?.state) || flow?.state === 'connected' || flow?.state === 'failed'

  const prepare = useMutation({
    mutationFn: prepareCloudInstall,
    onSuccess: (st) => {
      if (st.state === 'blocked' && st.blocked) setBlocked(st.blocked)
      flowStatus.invalidate()
    },
    meta: { errorMessage: 'Could not inspect this cluster for Cloud connect' },
  })

  const openModal = () => {
    setOpen(true)
    setSeen(true)
    localStorage.setItem('radar.cloudFunnel.seen', 'true')
    // Re-open lands on a live flow if one is running.
    if (lane === 'driver' && flowLive) setInFlowView(true)
  }

  const startConnect = () => {
    setBlocked(null)
    setInFlowView(true)
    prepare.mutate()
  }

  const exitFlow = () => {
    setInFlowView(false)
    setBlocked(null)
  }

  useEffect(() => {
    if (!open) return
    closeRef.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  const showFlow = inFlowView && (blocked !== null || prepare.isPending || flowLive)

  return (
    <>
      <Tooltip content="Radar Cloud — all your clusters, one URL" delay={100} position="bottom">
        <button
          onClick={openModal}
          aria-label="Radar Cloud"
          aria-haspopup="dialog"
          className="relative p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <Globe className="w-4 h-4" />
          {cloudInstallActive(flow?.state) ? (
            <span className="absolute top-0.5 right-0.5 w-[7px] h-[7px] rounded-full bg-emerald-500 animate-pulse" />
          ) : (
            !seen && (
              <span className="absolute top-0.5 right-0.5 w-[7px] h-[7px] rounded-full bg-emerald-500">
                <span className="absolute -inset-[3px] rounded-full border border-emerald-500/70 animate-ping" />
              </span>
            )
          )}
        </button>
      </Tooltip>

      {/* Portaled: the header's backdrop-blur creates a containing block that
          would otherwise trap this fixed overlay inside the 49px bar. */}
      {open && createPortal(
        <div
          className="fixed inset-0 z-[100] flex items-center justify-center bg-black/60 backdrop-blur-sm p-6"
          onClick={(e) => { if (e.target === e.currentTarget) setOpen(false) }}
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-label="Radar Cloud"
            className="dialog relative overflow-hidden w-[500px] max-w-full max-h-full overflow-y-auto"
          >
            <button
              ref={closeRef}
              onClick={() => setOpen(false)}
              aria-label="Close"
              className="absolute top-3.5 right-3.5 z-10 p-1.5 rounded-md text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
            >
              <X className="w-4 h-4" />
            </button>

            {showFlow && flow ? (
              <>
                <div className="px-7 pt-6">
                  <Eyebrow />
                </div>
                <CloudConnectFlow
                  status={flow}
                  blocked={blocked}
                  signupUrl={signupUrl}
                  onRefresh={() => flowStatus.invalidate()}
                  onExit={exitFlow}
                />
              </>
            ) : (
              <>
                <ModalBody />
                <ModalFooter
                  lane={lane}
                  signupUrl={signupUrl}
                  onConnect={startConnect}
                  onLater={() => setOpen(false)}
                />
              </>
            )}
          </div>
        </div>,
        document.body
      )}
    </>
  )
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
    <div className="flex items-center gap-2.5 mb-3.5">
      <RadarSweep />
      <span className="font-mono text-[10.5px] tracking-[0.16em] uppercase text-emerald-600 dark:text-emerald-400">Radar Cloud</span>
    </div>
  )
}

function Faces() {
  return (
    <div className="flex shrink-0" aria-hidden>
      {[
        ['R', 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300'],
        ['N', 'bg-sky-500/20 text-sky-700 dark:text-sky-300'],
        ['E', 'bg-amber-500/20 text-amber-700 dark:text-amber-300'],
      ].map(([initial, color], i) => (
        <div
          key={initial}
          className={`w-6 h-6 text-[10px] ${color} rounded-full grid place-items-center font-bold border-2 border-theme-surface ${i > 0 ? '-ml-1.5' : ''}`}
        >
          {initial}
        </div>
      ))}
    </div>
  )
}

function ModalFooter({
  lane,
  signupUrl,
  onConnect,
  onLater,
}: {
  lane: 'driver' | 'wizard'
  signupUrl: string
  onConnect: () => void
  onLater: () => void
}) {
  return (
    <div className="px-7 py-4 bg-theme-base border-t border-theme-border">
      <div className="flex items-center gap-4">
        {lane === 'driver' ? (
          <>
            <button
              onClick={onConnect}
              className="px-5 py-2 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[13.5px] font-bold shadow-[0_0_22px_rgba(16,185,129,0.35)] hover:shadow-[0_0_30px_rgba(16,185,129,0.5)] hover:-translate-y-px transition-all"
            >
              Connect this cluster
            </button>
            <a
              href={signupUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="text-[12.5px] text-theme-text-secondary hover:text-theme-text-primary underline underline-offset-2 transition-colors"
            >
              or start in the browser
            </a>
          </>
        ) : (
          <a
            href={signupUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="px-5 py-2 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[13.5px] font-bold shadow-[0_0_22px_rgba(16,185,129,0.35)] hover:shadow-[0_0_30px_rgba(16,185,129,0.5)] hover:-translate-y-px transition-all"
          >
            Try Cloud free
          </a>
        )}
        <button onClick={onLater} className="text-[12.5px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors">
          Maybe later
        </button>
      </div>
      <div className="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-theme-text-tertiary">
        {['Free for 3 clusters', 'No credit card', 'Your cluster data stays in your cluster'].map((item) => (
          <span key={item} className="flex items-center gap-1">
            <Check className="w-3 h-3 text-emerald-600 dark:text-emerald-400" />
            {item}
          </span>
        ))}
      </div>
      <p className="mt-3.5 text-[11px] text-theme-text-tertiary">
        Prefer to run the control plane in your own VPC? Self-hosting is self-serve — 30-day trial, no sales
        call.{' '}
        <a href={SELF_HOSTED_DOCS_URL} target="_blank" rel="noopener noreferrer" className="text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
          Read the docs
        </a>
        .
      </p>
    </div>
  )
}

// Headline defuses the paywall fear before anything is pitched; the grid
// carries the concrete capabilities; the humans strip closes with the
// anti-sell — the credibility beat that makes the sell land.
function ModalBody() {
  const features = [
    {
      icon: Globe,
      title: 'All your clusters, one URL',
      body: 'Fleet-wide issues, checks and search — instead of five browser tabs.',
    },
    {
      icon: Users,
      title: 'Bring the team',
      body: "SSO, invites and roles — your cluster's RBAC still has the final say.",
    },
    {
      icon: Bell,
      title: 'Alerts that find you',
      body: 'Slack or webhook the moment something breaks — even at 3am.',
    },
    {
      icon: History,
      title: 'History that sticks around',
      body: 'A timeline that survives restarts — and keeps getting longer.',
    },
    {
      icon: Sparkles,
      title: 'An AI agent on your fleet',
      body: 'Analyzes issues, pinpoints the root cause, and proposes the fix.',
      wide: true,
    },
  ]
  return (
    <div className="px-7 pt-6 pb-1">
      <Eyebrow />
      <h3 className="text-[21px] font-semibold leading-tight tracking-tight text-theme-text-primary mb-2.5 text-balance">
        First things first: Radar stays free.
      </h3>
      <p className="text-[13.5px] leading-relaxed text-theme-text-secondary mb-4">
        The app you're looking at is Apache&nbsp;2.0 — every feature, forever, no rug pulls.{' '}
        <b className="text-theme-text-primary font-semibold">Radar Cloud is how we keep the lights on:</b> the
        same Radar, plus the parts that are genuinely hard to run on your own.
      </p>
      <div className="grid grid-cols-2 gap-2 mb-4">
        {features.map(({ icon: Icon, title, body, wide }) => (
          <div key={title} className={`card-inner-lg flex gap-2.5 ${wide ? 'col-span-2' : ''}`}>
            <Icon className="w-4 h-4 shrink-0 mt-0.5 text-emerald-600 dark:text-emerald-400" />
            <div>
              <div className="text-[12.5px] font-semibold text-theme-text-primary">{title}</div>
              <p className="mt-0.5 text-[11.5px] leading-snug text-theme-text-tertiary">{body}</p>
            </div>
          </div>
        ))}
      </div>
      <div className="flex items-start gap-2.5 mb-4">
        <div className="mt-0.5">
          <Faces />
        </div>
        <div className="min-w-0">
          <p className="text-[12px] leading-snug text-theme-text-secondary">
            If it's just you and one cluster — honestly, stay right here. This app is the product, not a demo.
          </p>
          <p className="mt-1 text-[11px] leading-snug text-theme-text-tertiary">
            Radar is built in the open by many hands, and overseen by a small team of humans — the kind you can
            actually talk to.{' '}
            <a href={ABOUT_URL} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
              Meet us →
            </a>
          </p>
        </div>
      </div>
    </div>
  )
}
