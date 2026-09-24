import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { ArrowLeft, ArrowRight, MessageSquareHeart, Star, X } from 'lucide-react'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { useCapabilities } from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import { apiUrl, getApiBase, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { PollSubmitError, dismissPoll, recordPollShown, submitPoll, usePollStatus } from '../../api/poll'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../utils/animation'
import { openExternal } from '../../utils/navigation'
import { openCloudFunnelWith } from '../CloudFunnelButton'
import { nudgeSeenThisLoad, useNudgeVisible, useOtherNudgeVisible } from '../nudges/activeNudges'
import { CURRENT_ROUND, POLL_COPY, type PollAnswers, type PollQuestion } from './pollDefinition'
import {
  type ClosingLine,
  type PollContext,
  closingLine,
  discoveryLinks,
  newSubmissionId,
  offeredOptions,
  pruneAnswers,
  toggleChoice,
  visibleQuestions,
} from './pollFlow'
import { type BrowserPollState, browserAllows, mergePollState, readPollState, recordActiveDay, writePollState } from './pollStorage'

const NUDGE_ID = 'oss-poll'
// Never the first thing someone sees: the card waits until Radar has been
// open for a minute and nothing else is asking for attention.
const CARD_DELAY_MS = 60_000
const GITHUB_URL = 'https://github.com/skyhook-io/radar'
// `?poll-preview` opens the card at once, for review. Nothing is recorded or
// sent in preview, so it can be clicked through on any build.
const PREVIEW_PARAM = 'poll-preview'

function storage(): Storage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

/**
 * The in-product "help us improve Radar OSS" poll: a corner card that opens a
 * one-question-per-screen dialog. The host must not mount it when Radar is
 * embedded in another app.
 */
export function OssPoll({ onNavigate }: { onNavigate: (path: string) => void }) {
  const capabilities = useCapabilities()
  const rawMode = capabilities.data ? (capabilities.data.deployment?.mode ?? 'local') : undefined
  const offerable = rawMode === 'local' || rawMode === 'in-cluster'
  const mode: PollContext['mode'] = rawMode === 'in-cluster' ? 'in-cluster' : 'local'
  const cloudAvailable = !!capabilities.data?.cloudConnect

  const status = usePollStatus(offerable)
  const [browserState, setBrowserState] = useState<BrowserPollState | null>(null)
  const apiBase = getApiBase()

  // Today counts as a day of use, before anything is decided.
  const counted = useRef(false)
  useEffect(() => {
    if (counted.current) return
    counted.current = true
    const store = storage()
    const current = store ? readPollState(store, apiBase) : null
    if (!store || !current) {
      setBrowserState(null)
      return
    }
    const next = recordActiveDay(current, Date.now())
    writePollState(store, apiBase, next)
    setBrowserState(next)
  }, [apiBase])

  const updateBrowserState = useCallback(
    (patch: Partial<BrowserPollState>) => {
      const store = storage()
      const next = store ? mergePollState(store, apiBase, patch) : null
      if (next) setBrowserState(next)
    },
    [apiBase],
  )

  const allowed =
    offerable &&
    !!status.data?.eligible &&
    status.data.round === CURRENT_ROUND.id &&
    browserAllows(browserState, CURRENT_ROUND.id, Date.now())

  const [delayPassed, setDelayPassed] = useState(false)
  useEffect(() => {
    if (!allowed) return
    const t = window.setTimeout(() => setDelayPassed(true), CARD_DELAY_MS)
    return () => window.clearTimeout(t)
  }, [allowed])

  const [searchParams] = useSearchParams()
  const preview = searchParams.has(PREVIEW_PARAM)
  const [phase, setPhase] = useState<'idle' | 'card' | 'dialog' | 'done'>('idle')
  const othersShowing = useOtherNudgeVisible(NUDGE_ID)

  useEffect(() => {
    if (preview && offerable && !othersShowing) setPhase((p) => (p === 'idle' ? 'card' : p))
  }, [preview, offerable, othersShowing])

  useEffect(() => {
    if (preview || phase !== 'idle' || !allowed || !delayPassed || othersShowing) return
    if (nudgeSeenThisLoad('github-star')) return
    // Another tab may have shown, dismissed or answered the poll since this
    // one loaded; its storage write is the only signal that reaches here.
    const store = storage()
    const fresh = store ? readPollState(store, apiBase) : null
    if (!browserAllows(fresh, CURRENT_ROUND.id, Date.now())) {
      setPhase('done')
      return
    }
    setPhase('card')
    recordPollShown()
    updateBrowserState({ lastShownAt: Date.now() })
  }, [preview, phase, allowed, delayPassed, othersShowing, updateBrowserState, apiBase])

  // The card also steps aside for a nudge that arrives after it, such as an
  // update notice whose version check lands late. The dialog, once opened, is
  // the person's choice and stays.
  const cardVisible = phase === 'card' && !othersShowing
  useNudgeVisible(NUDGE_ID, cardVisible || phase === 'dialog')

  const later = () => {
    if (!preview) dismissPoll('snooze')
    setPhase('done')
  }
  const never = () => {
    if (!preview) {
      dismissPoll('never')
      updateBrowserState({ never: true })
    }
    setPhase('done')
  }

  return (
    <>
      <PollCard show={cardVisible} onAccept={() => setPhase('dialog')} onLater={later} onNever={never} />
      {(phase === 'dialog' || phase === 'done') && (
        <PollDialog
          open={phase === 'dialog'}
          mode={mode}
          cloudAvailable={cloudAvailable}
          onClose={() => setPhase('done')}
          onSubmitted={() => {
            if (!preview) updateBrowserState({ submittedRound: CURRENT_ROUND.id })
          }}
          onNavigate={onNavigate}
          preview={preview}
        />
      )}
    </>
  )
}

function PollCard({
  show,
  onAccept,
  onLater,
  onNever,
}: {
  show: boolean
  onAccept: () => void
  onLater: () => void
  onNever: () => void
}) {
  const { shouldRender, isOpen } = useAnimatedUnmount(show, overlayExitMs('menu'))
  if (!shouldRender) return null
  return (
    <div
      role="dialog"
      aria-labelledby="oss-poll-card-title"
      inert={!show || undefined}
      className={clsx(
        'fixed bottom-4 right-4 z-50 w-[340px] max-w-[calc(100vw-2rem)] bg-theme-surface border border-theme-border rounded-lg shadow-theme-lg p-4 origin-bottom-right',
        TRANSITION_MENU,
        isOpen ? 'opacity-100 translate-y-0 scale-100' : 'opacity-0 translate-y-1 scale-[0.97]',
        !show && 'pointer-events-none',
      )}
      style={overlayTransitionStyle(isOpen, 'menu')}
    >
      <div className="flex items-start gap-3">
        <div className="flex items-center justify-center w-8 h-8 bg-accent-muted rounded-full shrink-0">
          <MessageSquareHeart className="w-4 h-4 text-accent" />
        </div>
        <div className="flex-1 min-w-0">
          <h4 id="oss-poll-card-title" className="text-sm font-medium text-theme-text-primary pr-5">
            {POLL_COPY.cardTitle}
          </h4>
          <p className="text-xs text-theme-text-secondary mt-1">{POLL_COPY.cardBody}</p>
          <div className="flex items-center gap-2 mt-3">
            <button type="button" onClick={onAccept} className="btn-brand text-xs px-3 py-1.5">
              {POLL_COPY.cardAccept}
            </button>
            <button
              type="button"
              onClick={onLater}
              className="px-2 py-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            >
              {POLL_COPY.cardLater}
            </button>
            <span className="text-theme-text-tertiary text-xs" aria-hidden>
              ·
            </span>
            <button
              type="button"
              onClick={onNever}
              className="px-1 py-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
            >
              {POLL_COPY.cardNever}
            </button>
          </div>
        </div>
        <button
          type="button"
          onClick={onLater}
          aria-label="Close"
          className="absolute top-3 right-3 p-1 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  )
}

type SendState =
  | { kind: 'idle' }
  | { kind: 'sending' }
  | { kind: 'failed'; message: string }
  | { kind: 'closed' }

type ContactState = 'none' | 'ok' | 'failed' | 'resending'

function PollDialog({
  open,
  mode,
  cloudAvailable,
  onClose,
  onSubmitted,
  onNavigate,
  preview,
}: {
  open: boolean
  mode: PollContext['mode']
  cloudAvailable: boolean
  onClose: () => void
  onSubmitted: () => void
  onNavigate: (path: string) => void
  preview: boolean
}) {
  const apiResources = useAPIResources()
  // What the detection-gated options depend on, decided once when the poll
  // opens. A status that changes mid-poll must not change what was asked.
  const [available, setAvailable] = useState<PollContext['available'] | null>(null)
  useEffect(() => {
    if (available || apiResources.isLoading) return
    let cancelled = false
    const argocd = !!apiResources.data?.some((r) => r.name === 'applications' && r.group === 'argoproj.io')
    fetch(apiUrl('/prometheus/status'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => (res.ok ? res.json() : null))
      .catch(() => null)
      .then((prom) => {
        if (!cancelled) setAvailable({ argocd, prometheus: !!prom?.connected })
      })
    return () => {
      cancelled = true
    }
  }, [available, apiResources.isLoading, apiResources.data])

  const ctx = useMemo<PollContext>(() => ({ mode, available: available ?? { argocd: false, prometheus: false } }), [mode, available])
  const [answers, setAnswers] = useState<PollAnswers>({})
  const questions = useMemo(() => visibleQuestions(ctx, answers), [ctx, answers])
  const [step, setStep] = useState(0)
  const [screen, setScreen] = useState<'questions' | 'send' | 'thanks'>('questions')
  const [email, setEmail] = useState('')
  const [wantsCall, setWantsCall] = useState(false)
  const [send, setSend] = useState<SendState>({ kind: 'idle' })
  const [contact, setContact] = useState<ContactState>('none')
  const [submitted, setSubmitted] = useState<PollAnswers>({})
  // Kept across retries so a retry after a timeout can't be counted twice.
  const submissionId = useRef(newSubmissionId())
  const emailRef = useRef<HTMLInputElement>(null)

  const question = questions[Math.min(step, questions.length - 1)]
  const setAnswer = (id: string, next: { choices?: string[]; text?: string }) =>
    setAnswers((prev) => ({ ...prev, [id]: { ...prev[id], ...next } }))

  const next = () => {
    if (step + 1 < questions.length) setStep(step + 1)
    else setScreen('send')
  }
  // Skipping clears a half-given answer so it is not sent, then moves on.
  // Recomputing the list first matters: skipping Q5 also drops its follow-ups.
  const skip = () => {
    if (!question) return
    const remaining = { ...answers }
    delete remaining[question.id]
    const nextQuestions = visibleQuestions(ctx, remaining)
    const at = nextQuestions.findIndex((q) => q.id === question.id)
    setAnswers(remaining)
    if (at >= 0 && at + 1 < nextQuestions.length) setStep(at + 1)
    else setScreen('send')
  }
  const back = () => {
    if (screen === 'send') {
      setScreen('questions')
      setStep(questions.length - 1)
    } else if (step > 0) setStep(step - 1)
  }

  const trimmedEmail = email.trim()
  const callNeedsEmail = wantsCall && !trimmedEmail

  const doSend = async () => {
    if (callNeedsEmail) {
      emailRef.current?.focus()
      return
    }
    const clean = pruneAnswers(ctx, answers)
    if (preview) {
      console.info('[poll preview] would send', { answers: clean, email: trimmedEmail || undefined, wantsCall })
      setSubmitted(clean)
      setContact(trimmedEmail ? 'ok' : 'none')
      setScreen('thanks')
      return
    }
    setSend({ kind: 'sending' })
    try {
      const result = await submitPoll({
        submissionId: submissionId.current,
        answers: clean,
        ...(trimmedEmail ? { email: trimmedEmail, wantsCall } : {}),
      })
      setSubmitted(clean)
      setContact(trimmedEmail ? (result.contact === 'ok' ? 'ok' : 'failed') : 'none')
      setSend({ kind: 'idle' })
      setScreen('thanks')
      onSubmitted()
    } catch (err) {
      if (err instanceof PollSubmitError && err.status === 410) setSend({ kind: 'closed' })
      else if (err instanceof PollSubmitError && err.status === 400) setSend({ kind: 'failed', message: err.message })
      else setSend({ kind: 'failed', message: POLL_COPY.sendFailed })
    }
  }

  const resendContact = async () => {
    setContact('resending')
    try {
      const result = await submitPoll({
        submissionId: submissionId.current,
        answers: {},
        email: trimmedEmail,
        wantsCall,
        contactOnly: true,
      })
      setContact(result.contact === 'ok' ? 'ok' : 'failed')
    } catch {
      setContact('failed')
    }
  }

  const titleId = 'oss-poll-dialog-title'

  return (
    <DialogPortal open={open} onClose={onClose} labelledBy={titleId} className="w-[520px] max-w-[calc(100vw-2rem)]">
      <div className="relative flex flex-col max-h-[85vh]">
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="absolute top-4 right-4 p-1 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
        >
          <X className="w-4 h-4" />
        </button>

        {screen === 'questions' && !available && (
          <div className="px-7 py-10 text-sm text-theme-text-secondary" aria-live="polite">
            <h3 id={titleId} className="sr-only">
              {POLL_COPY.cardTitle}
            </h3>
            Loading…
          </div>
        )}

        {screen === 'questions' && available && question && (
          <>
            <div className="px-7 pt-6 overflow-y-auto">
              <p className="text-[11px] font-medium text-theme-text-tertiary tabular-nums" aria-live="polite">
                {step + 1} of {questions.length}
              </p>
              {step === 0 && <p className="mt-2 text-xs text-theme-text-secondary">{POLL_COPY.intro}</p>}
              <QuestionView
                key={question.id}
                titleId={titleId}
                question={question}
                ctx={ctx}
                answer={answers[question.id]}
                onChange={(a) => setAnswer(question.id, a)}
              />
            </div>
            <Footer
              onBack={step > 0 ? back : undefined}
              onSkip={skip}
              primary={{ label: 'Next', onClick: next }}
            />
          </>
        )}

        {screen === 'send' && (
          <>
            <div className="px-7 pt-6 overflow-y-auto">
              <h3 id={titleId} className="text-base font-semibold text-theme-text-primary pr-8">
                {POLL_COPY.send}
              </h3>
              <label className="block mt-4 text-sm font-medium text-theme-text-primary" htmlFor="oss-poll-email">
                {POLL_COPY.emailLabel}
              </label>
              <input
                ref={emailRef}
                id="oss-poll-email"
                type="email"
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                aria-describedby="oss-poll-email-help"
                aria-invalid={callNeedsEmail || undefined}
                className="mt-1.5 w-full rounded-md border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent/40"
              />
              <p id="oss-poll-email-help" className="mt-1.5 text-xs text-theme-text-secondary">
                {POLL_COPY.emailHelp}
              </p>
              <label className="mt-4 flex items-start gap-2.5 text-sm text-theme-text-primary cursor-pointer">
                <input
                  type="checkbox"
                  checked={wantsCall}
                  onChange={(e) => setWantsCall(e.target.checked)}
                  className="mt-0.5 accent-skyhook-600"
                />
                <span>{POLL_COPY.callLabel}</span>
              </label>
              {callNeedsEmail && (
                <p className="mt-2 text-xs text-amber-600 dark:text-amber-400" role="alert">
                  {POLL_COPY.callNeedsEmail}
                </p>
              )}
              {send.kind === 'failed' && (
                <p className="mt-4 text-xs text-red-600 dark:text-red-400" role="alert">
                  {send.message}
                </p>
              )}
              {send.kind === 'closed' && (
                <p className="mt-4 text-xs text-theme-text-secondary" role="status">
                  {POLL_COPY.closed}
                </p>
              )}
            </div>
            <Footer
              onBack={back}
              primary={
                send.kind === 'closed'
                  ? { label: 'Close', onClick: onClose }
                  : {
                      label: send.kind === 'sending' ? POLL_COPY.sending : send.kind === 'failed' ? POLL_COPY.tryAgain : POLL_COPY.send,
                      onClick: doSend,
                      disabled: send.kind === 'sending',
                    }
              }
            />
          </>
        )}

        {screen === 'thanks' && (
          <ThankYou
            titleId={titleId}
            answers={submitted}
            mode={ctx.mode}
            preview={preview}
            line={closingLine(submitted, cloudAvailable || preview)}
            contact={contact}
            onResendContact={resendContact}
            onClose={onClose}
            onNavigate={(path) => {
              onClose()
              onNavigate(path)
            }}
          />
        )}
      </div>
    </DialogPortal>
  )
}

function Footer({
  onBack,
  onSkip,
  primary,
}: {
  onBack?: () => void
  onSkip?: () => void
  primary: { label: string; onClick: () => void; disabled?: boolean }
}) {
  return (
    <div className="flex items-center justify-between gap-3 px-7 py-4 mt-5 border-t border-theme-border">
      {onBack ? (
        <button
          type="button"
          onClick={onBack}
          className="inline-flex items-center gap-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <ArrowLeft className="w-3.5 h-3.5" /> Back
        </button>
      ) : (
        <span />
      )}
      <div className="flex items-center gap-2">
        {onSkip && (
          <button
            type="button"
            onClick={onSkip}
            className="px-3 py-1.5 text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors"
          >
            {POLL_COPY.skip}
          </button>
        )}
        <button type="button" onClick={primary.onClick} disabled={primary.disabled} className="btn-brand text-sm px-4 py-1.5 disabled:opacity-60">
          {primary.label}
        </button>
      </div>
    </div>
  )
}

function QuestionView({
  titleId,
  question,
  ctx,
  answer,
  onChange,
}: {
  titleId: string
  question: PollQuestion
  ctx: PollContext
  answer: { choices?: string[]; text?: string } | undefined
  onChange: (next: { choices?: string[]; text?: string }) => void
}) {
  const options = offeredOptions(question, ctx)
  const chosen = answer?.choices ?? []
  const showText =
    question.kind === 'text' || question.textAlways || (question.textOption !== undefined && chosen.includes(question.textOption))
  const inputType = question.kind === 'single' ? 'radio' : 'checkbox'
  const legendRef = useRef<HTMLLegendElement>(null)
  // A new question is a new screen: move focus to its title so screen readers
  // announce it instead of staying on the Next button.
  useEffect(() => {
    legendRef.current?.focus()
  }, [])
  const textLabel = question.textLabel ?? (question.kind === 'text' ? question.title : POLL_COPY.otherPlaceholder)

  return (
    <fieldset className="mt-3">
      <legend ref={legendRef} tabIndex={-1} id={titleId} className="text-base font-semibold text-theme-text-primary pr-8 outline-none">
        {question.title}
      </legend>
      {question.description && <p className="mt-1.5 text-sm text-theme-text-secondary">{question.description}</p>}
      {question.hint && <p className="mt-1 text-xs text-theme-text-tertiary">{question.hint}</p>}

      {options.length > 0 && (
        <div className="mt-4 space-y-1.5">
          {options.map((o) => {
            const checked = chosen.includes(o.id)
            return (
              <label
                key={o.id}
                className={clsx(
                  'flex items-start gap-2.5 rounded-md border px-3 py-2 text-sm cursor-pointer transition-colors',
                  checked
                    ? 'border-accent/60 bg-accent-muted text-theme-text-primary'
                    : 'border-theme-border text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
                )}
              >
                <input
                  type={inputType}
                  name={question.id}
                  checked={checked}
                  onChange={() => onChange({ choices: toggleChoice(question, chosen, o.id) })}
                  // A checked radio fires no change when clicked again; this is
                  // what lets an optional single-choice answer be cleared.
                  onClick={() => {
                    if (question.kind === 'single' && checked) onChange({ choices: [] })
                  }}
                  className="mt-0.5 accent-skyhook-600"
                />
                <span>{o.label}</span>
              </label>
            )
          })}
        </div>
      )}

      {showText && (
        <div className="mt-3">
          {question.kind !== 'text' && (
            <label htmlFor={`${question.id}-text`} className="block text-sm font-medium text-theme-text-primary mb-1.5">
              {textLabel}
              {question.textAlways && <span className="font-normal text-theme-text-tertiary"> (optional)</span>}
            </label>
          )}
          <textarea
            id={`${question.id}-text`}
            aria-label={question.kind === 'text' ? question.title : undefined}
            value={answer?.text ?? ''}
            maxLength={2000}
            rows={question.kind === 'text' ? 4 : 2}
            placeholder={question.textPlaceholder}
            onChange={(e) => onChange({ text: e.target.value })}
            className="w-full resize-y rounded-md border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent/40"
          />
        </div>
      )}
    </fieldset>
  )
}

function ThankYou({
  titleId,
  answers,
  mode,
  preview,
  line,
  contact,
  onResendContact,
  onClose,
  onNavigate,
}: {
  titleId: string
  answers: PollAnswers
  mode: PollContext['mode']
  preview: boolean
  line: ClosingLine
  contact: ContactState
  onResendContact: () => void
  onClose: () => void
  onNavigate: (path: string) => void
}) {
  const links = discoveryLinks(answers)
  const [cloudDismissed, setCloudDismissed] = useState(false)

  const openCloud = (view: 'pitch' | 'self-managed') => {
    onClose()
    openCloudFunnelWith({ campaign: 'oss-poll', view })
  }

  const cloudLine =
    line === 'self_managed' ? POLL_COPY.selfManaged : line === 'investigations' ? POLL_COPY.investigations : null

  return (
    <>
      <div className="px-7 pt-6 overflow-y-auto space-y-4">
        <h3 id={titleId} className="text-base font-semibold text-theme-text-primary pr-8">
          {POLL_COPY.thanks}
        </h3>
        {preview && <p className="text-xs text-theme-text-tertiary">Preview: nothing was sent.</p>}

        {contact === 'failed' || contact === 'resending' ? (
          <div className="text-sm text-theme-text-secondary">
            <p>{POLL_COPY.contactFailed}</p>
            <button
              type="button"
              onClick={onResendContact}
              disabled={contact === 'resending'}
              className="mt-1.5 text-sm text-accent underline underline-offset-2 disabled:opacity-60"
            >
              {contact === 'resending' ? POLL_COPY.sending : POLL_COPY.contactRetry}
            </button>
          </div>
        ) : null}

        {line === 'already_cloud' && <p className="text-sm text-theme-text-secondary">{POLL_COPY.alreadyCloud}</p>}
        {line === 'right_fit' && <p className="text-sm text-theme-text-secondary">{POLL_COPY.rightFit}</p>}
        {cloudLine && !cloudDismissed && (
          <div className="card-inner">
            <p className="text-sm text-theme-text-secondary">{cloudLine}</p>
            <div className="mt-3 flex items-center gap-3">
              <button
                type="button"
                onClick={() => openCloud(line === 'self_managed' ? 'self-managed' : 'pitch')}
                className="btn-brand text-xs px-3 py-1.5"
              >
                {POLL_COPY.seeHow}
              </button>
              <button
                type="button"
                onClick={() => setCloudDismissed(true)}
                className="text-xs text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              >
                {POLL_COPY.noThanks}
              </button>
            </div>
          </div>
        )}

        <StarAsk mode={mode} preview={preview} />

        {links.length > 0 && (
          <div className="flex flex-wrap gap-x-4 gap-y-1.5">
            {links.map((l) => (
              <button
                key={l.path}
                type="button"
                onClick={() => onNavigate(l.path)}
                className="inline-flex items-center gap-1 text-xs text-theme-text-secondary underline underline-offset-2 decoration-theme-border hover:text-theme-text-primary transition-colors"
              >
                {l.label}
                <ArrowRight className="w-3 h-3" />
              </button>
            ))}
          </div>
        )}
      </div>
      <Footer primary={{ label: 'Done', onClick: onClose }} />
    </>
  )
}

interface StarStatus {
  starred: boolean
  ghAvailable: boolean
}

// Local Radar reuses the existing star flow, which can star through a signed-in
// gh CLI and counts "Maybe later" in the same backoff as the star callout.
// In-cluster Radar shares one star.json across every viewer, so there it is a
// plain link that touches no server state.
function StarAsk({ mode, preview }: { mode: PollContext['mode']; preview: boolean }) {
  const local = mode === 'local' && !preview
  const [status, setStatus] = useState<StarStatus | null>(local ? null : { starred: false, ghAvailable: false })
  const [hidden, setHidden] = useState(false)
  const [justStarred, setJustStarred] = useState(false)

  useEffect(() => {
    if (!local) return
    fetch(apiUrl('/github/starred'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => setStatus(data ? { starred: !!data.starred, ghAvailable: !!data.ghAvailable } : { starred: false, ghAvailable: false }))
      .catch(() => setStatus({ starred: false, ghAvailable: false }))
  }, [local])

  if (justStarred) return <p className="text-sm text-theme-text-secondary">{POLL_COPY.starred}</p>
  if (!status || status.starred || hidden) return null

  const star = () => {
    if (local && status.ghAvailable) {
      fetch(apiUrl('/github/star'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() })
        .then((res) => (res.ok ? res.json() : null))
        .then((data) => {
          if (data?.starred) setJustStarred(true)
          else openExternal(GITHUB_URL)
        })
        .catch(() => openExternal(GITHUB_URL))
      return
    }
    openExternal(GITHUB_URL)
    setHidden(true)
  }
  const later = () => {
    if (local) {
      void fetch(apiUrl('/github/dismiss'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() }).catch(() => {})
    }
    setHidden(true)
  }

  return (
    <div className="card-inner">
      <p className="text-sm text-theme-text-secondary">{POLL_COPY.star}</p>
      <div className="mt-3 flex items-center gap-3">
        <button
          type="button"
          onClick={star}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium bg-yellow-500/15 text-yellow-600 dark:text-yellow-500 hover:bg-yellow-500/25 rounded-md transition-colors"
        >
          <Star className="w-3.5 h-3.5" />
          {POLL_COPY.starButton}
        </button>
        <button
          type="button"
          onClick={later}
          className="text-xs text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          {POLL_COPY.starLater}
        </button>
      </div>
    </div>
  )
}
