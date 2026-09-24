import roundJSON from './round-2026-q4.json'

export type QuestionKind = 'single' | 'multi' | 'text'

export interface PollOption {
  id: string
  label: string
  /** Only offered when this integration is present. */
  requires?: 'argocd' | 'prometheus'
  /** Where the thank-you screen sends someone who ticked this. */
  link?: { path: string; label: string }
}

export interface PollCondition {
  question: string
  anyOf: string[]
  /** What to record in place of a question skipped by this condition. */
  record?: string
}

export interface PollQuestion {
  id: string
  kind: QuestionKind
  title: string
  description?: string
  hint?: string
  max?: number
  mode?: 'local' | 'in-cluster'
  block?: string
  textOption?: string
  textAlways?: boolean
  textLabel?: string
  textPlaceholder?: string
  exclusive?: string
  showIf?: PollCondition
  skipIf?: PollCondition
  options: PollOption[]
}

export interface PollRound {
  id: string
  block: string
  startsAt: string
  endsAt: string
  questions: PollQuestion[]
}

export interface PollAnswer {
  choices?: string[]
  text?: string
}

export type PollAnswers = Record<string, PollAnswer>

// The file is shared with the Go server, which validates answers against it.
export const CURRENT_ROUND = roundJSON as PollRound

export const POLL_COPY = {
  cardTitle: 'Got two minutes to help us improve Radar OSS?',
  cardBody: 'A few questions about how you use it. Anonymous.',
  cardAccept: 'Sure',
  cardLater: 'Not now',
  cardNever: "Don't ask again",
  intro: 'Every question is optional. Nothing is sent until you press Send, and Radar never sends cluster data.',
  otherPlaceholder: 'Tell us more',
  emailLabel: 'Email (optional)',
  emailHelp: "Leave it if you'd like a reply, or to hear when something you asked for ships. Your answers stay anonymous without it.",
  callLabel: "I'm open to a 20-minute call about how I use Radar",
  callNeedsEmail: 'Add an email so we can set up the call.',
  send: 'Send answers',
  sending: 'Sending…',
  thanks: 'Thanks. This goes straight into what we build next.',
  alreadyCloud: 'Good to hear. Thanks for using both.',
  selfManaged: "You can also run Radar Cloud yourself, inside your own infrastructure. It's part of Enterprise, with a 14-day trial.",
  investigations:
    "You mentioned setting up an agent is what's in the way. Radar Cloud runs investigations for you: a root cause with evidence, no CLI or API key. Every plan includes a monthly investigation budget, including the free one.",
  rightFit:
    "From your answers, Radar OSS is the right fit for the way you work. That is exactly what it is for. If you ever want Radar to keep watching after you close the laptop, that's Radar Cloud, and three clusters are free.",
  seeHow: 'See how it works',
  noThanks: 'No thanks',
  star: 'If Radar saves you time, a star on GitHub helps other people find it.',
  starButton: 'Star Radar on GitHub',
  starLater: 'Maybe later',
  starred: 'Thanks for the star.',
  sendFailed: "Couldn't send your answers. Try again in a moment.",
  closed: 'This poll has closed. Thanks for taking the time.',
  contactFailed: "Your answers arrived, but your email didn't go through.",
  contactRetry: 'Send the email again',
  tryAgain: 'Try again',
  skip: 'Skip',
} as const
