// A Job's name is copied into the job-name label on its Pods, so it has to fit
// a label value (63 characters) as well as the DNS-1123 subdomain rule the API
// server applies to Job names.
const MAX_JOB_NAME = 63
const DNS1123_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
const MANUAL_SUFFIX = '-manual-'
const RANDOM_LENGTH = 5

/** Why `name` can't name a Job, or null when it can. */
export function jobNameError(name: string): string | null {
  if (!name) return 'Enter a Job name.'
  if (name.length > MAX_JOB_NAME) return `At most ${MAX_JOB_NAME} characters: the name becomes the job-name label on the Job's Pods.`
  if (!DNS1123_SUBDOMAIN.test(name)) return "Use lowercase letters, digits, '-' and '.', starting and ending with a letter or digit."
  return null
}

/** `<cronjob>-manual-xxxxx`, with the CronJob name cut so the whole fits 63 characters. */
export function suggestManualJobName(cronJobName: string): string {
  const base = cronJobName.slice(0, MAX_JOB_NAME - MANUAL_SUFFIX.length - RANDOM_LENGTH).replace(/[-.]+$/, '')
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789'
  const random = Array.from(crypto.getRandomValues(new Uint8Array(RANDOM_LENGTH)), (b) => alphabet[b % alphabet.length]).join('')
  return `${base}${MANUAL_SUFFIX}${random}`
}
