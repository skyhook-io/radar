import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Bell, Check, Globe, History, Sparkles, Users, X } from 'lucide-react'
import { Tooltip } from './ui/Tooltip'

// DESIGN REVIEW MOCK — OSS → Cloud funnel (SKY-1107).
// Three modal variants behind a temporary switcher; one will ship, the
// switcher won't. Ships dark by design: no impressions, no remote config —
// conversion is measured on the receiving end via utm_source.
const SIGNUP_URL = 'https://app.radarhq.io/signup?utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal'
const ABOUT_URL = 'https://www.radarhq.io/about'
const DEMO_URL = 'https://www.radarhq.io/demo'

type Variant = 'letter' | 'features' | 'postcard'

export function CloudFunnelButton() {
  const [open, setOpen] = useState(false)
  const [variant, setVariant] = useState<Variant>('features')
  const [seen, setSeen] = useState(() => localStorage.getItem('radar.cloudFunnel.seen') === 'true')
  const closeRef = useRef<HTMLButtonElement>(null)

  const openModal = () => {
    setOpen(true)
    setSeen(true)
    localStorage.setItem('radar.cloudFunnel.seen', 'true')
  }

  useEffect(() => {
    if (!open) return
    closeRef.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
      if (e.key === '1') setVariant('letter')
      if (e.key === '2') setVariant('features')
      if (e.key === '3') setVariant('postcard')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

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
          {!seen && (
            <span className="absolute top-0.5 right-0.5 w-[7px] h-[7px] rounded-full bg-emerald-500">
              <span className="absolute -inset-[3px] rounded-full border border-emerald-500/70 animate-ping" />
            </span>
          )}
        </button>
      </Tooltip>

      {/* Portaled: the header's backdrop-blur creates a containing block that
          would otherwise trap this fixed overlay inside the 49px bar. */}
      {open && createPortal(
        <div
          className="fixed inset-0 z-[100] flex flex-col items-center justify-center gap-4 bg-black/60 backdrop-blur-sm p-6"
          onClick={(e) => { if (e.target === e.currentTarget) setOpen(false) }}
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-label="Radar Cloud"
            className={`dialog relative overflow-hidden ${variant === 'postcard' ? 'w-[640px]' : 'w-[500px]'} max-w-full max-h-full overflow-y-auto`}
          >
            <button
              ref={closeRef}
              onClick={() => setOpen(false)}
              aria-label="Close"
              className="absolute top-3.5 right-3.5 z-10 p-1.5 rounded-md text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
            >
              <X className="w-4 h-4" />
            </button>

            {variant === 'letter' && <LetterVariant />}
            {variant === 'features' && <FeaturesVariant />}
            {variant === 'postcard' && <PostcardVariant />}

            <ModalFooter onLater={() => setOpen(false)} />
          </div>

          {/* Temporary design-review switcher — not part of the shipping design */}
          <div
            className="flex items-center gap-1 rounded-full bg-theme-surface border border-theme-border shadow-theme-lg px-2 py-1.5 font-mono text-[11px] text-theme-text-tertiary"
            onClick={(e) => e.stopPropagation()}
          >
            <span className="px-1.5 uppercase tracking-wider text-[9.5px]">Design review</span>
            {([
              ['letter', '1 · Letter'],
              ['features', '2 · Features'],
              ['postcard', '3 · Postcard'],
            ] as const).map(([v, label]) => (
              <button
                key={v}
                onClick={() => setVariant(v)}
                className={`px-2.5 py-1 rounded-full transition-colors ${
                  variant === v
                    ? 'bg-emerald-500/20 text-emerald-600 dark:text-emerald-300'
                    : 'hover:bg-theme-hover hover:text-theme-text-primary'
                }`}
              >
                {label}
              </button>
            ))}
          </div>
        </div>,
        document.body
      )}
    </>
  )
}

function RadarSweep({ size = 30 }: { size?: number }) {
  return (
    <div
      aria-hidden
      className="relative rounded-full overflow-hidden shrink-0 border border-emerald-400/60 shadow-[0_0_12px_rgba(16,185,129,0.35)]"
      style={{ width: size, height: size, background: 'radial-gradient(circle at 50% 50%, #072920 0%, #03180f 70%, #010a06 100%)' }}
    >
      <div className="absolute inset-[16%] rounded-full border border-emerald-600/50" />
      <div
        className="absolute inset-0 rounded-full animate-[spin_4s_linear_infinite] motion-reduce:animate-none"
        style={{ background: 'conic-gradient(from 0deg, rgba(167,243,208,0.85) 0deg, rgba(16,185,129,0.25) 40deg, transparent 90deg)' }}
      />
    </div>
  )
}

function Eyebrow({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-2.5 mb-3.5">
      <RadarSweep />
      <span className="font-mono text-[10.5px] tracking-[0.16em] uppercase text-emerald-600 dark:text-emerald-400">{label}</span>
    </div>
  )
}

function Faces({ size = 'md' }: { size?: 'sm' | 'md' }) {
  const cls = size === 'sm' ? 'w-6 h-6 text-[10px]' : 'w-7 h-7 text-[11px]'
  return (
    <div className="flex shrink-0" aria-hidden>
      {[
        ['R', 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300'],
        ['N', 'bg-sky-500/20 text-sky-700 dark:text-sky-300'],
        ['E', 'bg-amber-500/20 text-amber-700 dark:text-amber-300'],
      ].map(([initial, color], i) => (
        <div
          key={initial}
          className={`${cls} ${color} rounded-full grid place-items-center font-bold border-2 border-theme-surface ${i > 0 ? '-ml-1.5' : ''}`}
        >
          {initial}
        </div>
      ))}
    </div>
  )
}

function CtaRow({ onLater }: { onLater: () => void }) {
  return (
    <div className="flex items-center gap-4">
      <a
        href={SIGNUP_URL}
        target="_blank"
        rel="noopener noreferrer"
        className="px-5 py-2 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[13.5px] font-bold shadow-[0_0_22px_rgba(16,185,129,0.35)] hover:shadow-[0_0_30px_rgba(16,185,129,0.5)] hover:-translate-y-px transition-all"
      >
        Try Cloud free
      </a>
      <button onClick={onLater} className="text-[12.5px] text-theme-text-tertiary hover:text-theme-text-primary transition-colors">
        Maybe later
      </button>
    </div>
  )
}

function ModalFooter({ onLater }: { onLater: () => void }) {
  return (
    <div className="px-7 py-4 bg-theme-base border-t border-theme-border">
      <CtaRow onLater={onLater} />
      <div className="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-theme-text-tertiary">
        {['Free for 3 clusters', 'No credit card', 'Your cluster data stays in your cluster'].map((item) => (
          <span key={item} className="flex items-center gap-1">
            <Check className="w-3 h-3 text-emerald-600 dark:text-emerald-400" />
            {item}
          </span>
        ))}
      </div>
      <p className="mt-3.5 text-[11px] text-theme-text-tertiary">
        Prefer to run the control plane in your own VPC? Self-hosting is an option —{' '}
        <a href={DEMO_URL} target="_blank" rel="noopener noreferrer" className="text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
          say hi
        </a>
        .
      </p>
    </div>
  )
}

// Variant 1 — a letter from the team. Most personal, feature mentions stay in prose.
function LetterVariant() {
  return (
    <div className="px-7 pt-6 pb-1">
      <Eyebrow label="A note from the Radar team" />
      <h3 className="text-[21px] font-semibold leading-tight tracking-tight text-theme-text-primary mb-3 text-balance">
        Hi — it's the humans behind Radar.
      </h3>
      <div className="space-y-3 text-[13.5px] leading-relaxed text-theme-text-secondary mb-4">
        <p>
          Radar is built in the open by many hands, and overseen by a small team of us. It's Apache&nbsp;2.0 and
          it stays that way: every feature you're using ships in the open, forever. No rug pulls.
        </p>
        <p>
          The way we keep the lights on is <b className="text-theme-text-primary font-semibold">Radar Cloud</b> —
          the same Radar, plus the parts that are genuinely hard to run yourself: every cluster under one URL,
          your team with SSO, alerts that find you at 3am, history that sticks around, and an AI agent that
          analyzes issues and solves them for you.
        </p>
        <p>
          If it's just you and one cluster — honestly, stay right here. This app is the product, not a demo.
          But if your team is juggling five browser tabs of Radar, we'd love to show you Cloud.
        </p>
      </div>
      <div className="flex items-center gap-2.5 mb-5">
        <Faces size="sm" />
        <span className="text-[12px] text-theme-text-tertiary italic">
          — the Radar team{' '}
          <a href={ABOUT_URL} target="_blank" rel="noopener noreferrer" className="not-italic text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
            meet us →
          </a>
        </span>
      </div>
    </div>
  )
}

// Variant 2 — headline defuses the paywall fear, then a 2×2 feature grid.
function FeaturesVariant() {
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
      body: 'Analyzes issues, pinpoints the root cause, and solves them for you.',
      wide: true,
    },
  ]
  return (
    <div className="px-7 pt-6 pb-1">
      <Eyebrow label="Radar Cloud" />
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
      <div className="flex items-center gap-2 mb-4">
        <Faces size="sm" />
        <p className="text-[11px] text-theme-text-tertiary">
          Radar is built in the open by many hands, and overseen by a small team of humans — the kind you can
          actually talk to.{' '}
          <a href={ABOUT_URL} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap text-theme-text-secondary underline underline-offset-2 hover:text-theme-text-primary">
            Meet us →
          </a>
        </p>
      </div>
    </div>
  )
}

// Variant 3 — compact split postcard: emerald art panel + terse checklist.
function PostcardVariant() {
  return (
    <div className="flex items-stretch">
      <div
        className="w-[220px] shrink-0 flex flex-col justify-between p-5 text-emerald-50"
        style={{ background: 'radial-gradient(120% 120% at 20% 15%, #0e3b2f 0%, #072018 55%, #03180f 100%)' }}
      >
        <RadarSweep size={54} />
        <div>
          <p className="text-[15px] font-semibold leading-snug text-balance">
            We keep Radar free. Cloud keeps our lights on.
          </p>
          <a
            href={ABOUT_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="mt-2.5 inline-flex items-center gap-1.5 text-[11px] text-emerald-300/90 hover:text-emerald-200"
          >
            <Faces size="sm" /> meet the humans →
          </a>
        </div>
      </div>
      <div className="flex-1 px-6 pt-6 pb-1">
        <h3 className="text-[19px] font-semibold leading-tight tracking-tight text-theme-text-primary mb-2 text-balance">
          Same Radar. Every cluster. One URL.
        </h3>
        <p className="text-[12.5px] leading-relaxed text-theme-text-secondary mb-3.5">
          Everything here stays Apache&nbsp;2.0, forever. Cloud adds the parts that are hard to run yourself:
        </p>
        <ul className="space-y-2 mb-5 text-[12.5px] text-theme-text-secondary">
          {[
            'Fleet-wide issues, checks and search',
            'Your team — SSO, roles, RBAC intact',
            'Slack alerts when things break at 3am',
            'History that sticks around',
            'An AI agent that analyzes and solves issues',
          ].map((line) => (
            <li key={line} className="flex items-start gap-2">
              <span className="mt-[7px] w-1 h-1 rounded-full bg-emerald-500 shrink-0" />
              {line}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}
