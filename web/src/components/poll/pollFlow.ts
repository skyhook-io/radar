import { CURRENT_ROUND, type PollAnswers, type PollCondition, type PollQuestion, type PollRound } from './pollDefinition'

export interface PollContext {
  mode: 'local' | 'in-cluster'
  /** Integrations present when the poll opened, frozen for its lifetime. */
  available: { argocd: boolean; prometheus: boolean }
}

function met(c: PollCondition, answers: PollAnswers): boolean {
  const chosen = answers[c.question]?.choices ?? []
  return chosen.some((id) => c.anyOf.includes(id))
}

/** Whether this person is asked `q`, given what they've answered so far. */
export function isVisible(q: PollQuestion, ctx: PollContext, answers: PollAnswers, round: PollRound = CURRENT_ROUND): boolean {
  if (q.block && q.block !== round.block) return false
  if (q.mode && q.mode !== ctx.mode) return false
  if (q.showIf && !met(q.showIf, answers)) return false
  if (q.skipIf && met(q.skipIf, answers)) return false
  return true
}

/** The questions this person will see, in order. Recomputed after each answer. */
export function visibleQuestions(ctx: PollContext, answers: PollAnswers, round: PollRound = CURRENT_ROUND): PollQuestion[] {
  return round.questions.filter((q) => isVisible(q, ctx, answers, round))
}

/** Options offered for `q`: detection-gated ones only when their integration was found. */
export function offeredOptions(q: PollQuestion, ctx: PollContext) {
  return q.options.filter((o) => !o.requires || ctx.available[o.requires])
}

/**
 * Drops answers to questions that are no longer asked, and to options that
 * are no longer offered. Going Back and changing Q5 must not send the
 * follow-ups of the branch the person left.
 */
export function pruneAnswers(ctx: PollContext, answers: PollAnswers, round: PollRound = CURRENT_ROUND): PollAnswers {
  const out: PollAnswers = {}
  for (const q of round.questions) {
    if (!isVisible(q, ctx, out, round)) continue
    const a = answers[q.id]
    if (!a) continue
    const allowed = new Set(offeredOptions(q, ctx).map((o) => o.id))
    const choices = (a.choices ?? []).filter((id) => allowed.has(id))
    const keepText = q.kind === 'text' || q.textAlways || (q.textOption !== undefined && choices.includes(q.textOption))
    const text = keepText ? a.text?.trim() ?? '' : ''
    if (choices.length || text) out[q.id] = { ...(choices.length ? { choices } : {}), ...(text ? { text } : {}) }
  }
  return out
}

/**
 * Applies a click on an option, enforcing the question's own rules: one
 * choice for single, the max for multi, and an exclusive "None of these".
 */
export function toggleChoice(q: PollQuestion, current: string[] | undefined, id: string): string[] {
  const chosen = current ?? []
  if (q.kind === 'single') return chosen[0] === id ? [] : [id]
  if (chosen.includes(id)) return chosen.filter((c) => c !== id)
  if (q.exclusive && id === q.exclusive) return [id]
  const without = q.exclusive ? chosen.filter((c) => c !== q.exclusive) : chosen
  if (q.max && without.length >= q.max) return without
  return [...without, id]
}

export type ClosingLine = 'already_cloud' | 'self_managed' | 'investigations' | 'right_fit' | null

/**
 * The one line under the thank-you. The first match wins. Lines that point at
 * Radar Cloud need the Cloud dialog to be available here; without it they
 * are skipped rather than shown with a button that opens nothing.
 */
export function closingLine(answers: PollAnswers, cloudAvailable: boolean): ClosingLine {
  const q9 = answers.q9?.choices?.[0]
  if (q9 === 'already' || answers.q5_agent?.choices?.includes('radar_cloud')) return 'already_cloud'
  if (cloudAvailable && (q9 === 'no_hosted' || q9 === 'airgapped')) return 'self_managed'
  const blockers = answers.q5_blocker?.choices ?? []
  if (cloudAvailable && (blockers.includes('no_agent') || blockers.includes('no_api'))) return 'investigations'
  const q4 = answers.q4?.choices ?? []
  const q2 = answers.q2?.choices?.[0]
  if (q4.length === 1 && q4[0] === 'none' && (q2 === 'one' || q2 === 'two_three')) return 'right_fit'
  return null
}

/** Links for the Q8 features this person said they didn't know about. */
export function discoveryLinks(answers: PollAnswers, round: PollRound = CURRENT_ROUND) {
  const q8 = round.questions.find((q) => q.id === 'q8')
  const picked = new Set(answers.q8?.choices ?? [])
  return (q8?.options ?? []).filter((o) => picked.has(o.id) && o.link).map((o) => o.link!)
}

export function newSubmissionId(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('').replace(/^(.{8})(.{4})(.{4})(.{4})/, '$1-$2-$3-$4-')
}
