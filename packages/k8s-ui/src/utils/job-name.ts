// A Job's name is copied into the job-name label on its Pods, so it has to fit
// a label value (63 characters) as well as the DNS-1123 subdomain rule the API
// server applies to Job names. Mirrors pkg/k8score's TriggerCronJobNamed.
const MAX_JOB_NAME = 63
const DNS1123_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
const MANUAL_SUFFIX = '-manual-'
const RANDOM_LENGTH = 5

/**
 * The room an Indexed Job's widest Pod hostname, `<job>-<index>`, needs beyond
 * the Job name; 0 for other completion modes.
 */
export function manualJobIndexRoom(jobSpec: { completionMode?: string; completions?: number } | undefined): number {
  if (jobSpec?.completionMode !== 'Indexed') return 0
  const completions = jobSpec.completions && jobSpec.completions > 0 ? jobSpec.completions : 1
  return String(completions - 1).length + 1
}

/**
 * Why `name` can't name a Job, or null when it can. `indexed`: the template is
 * Indexed, so the name becomes Pod hostnames, which are DNS labels.
 */
export function jobNameError(name: string, indexed = false): string | null {
  if (!name) return 'Enter a Job name.'
  if (name.length > MAX_JOB_NAME) return `At most ${MAX_JOB_NAME} characters: the name becomes the job-name label on the Job's Pods.`
  if (!DNS1123_SUBDOMAIN.test(name)) return "Use lowercase letters, digits, '-' and '.', starting and ending with a letter or digit."
  if (indexed && name.includes('.')) return "An Indexed Job's name becomes its Pods' hostnames, which cannot contain '.'."
  return null
}

/**
 * `<cronjob>-manual-xxxxx`, with the CronJob name cut so the whole fits 63
 * characters less `indexRoom` (see manualJobIndexRoom), and without dots when
 * there is index room.
 */
export function suggestManualJobName(cronJobName: string, indexRoom = 0): string {
  const name = indexRoom > 0 ? cronJobName.replace(/\./g, '-') : cronJobName
  const base = name.slice(0, MAX_JOB_NAME - indexRoom - MANUAL_SUFFIX.length - RANDOM_LENGTH).replace(/[-.]+$/, '')
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789'
  const random = Array.from(crypto.getRandomValues(new Uint8Array(RANDOM_LENGTH)), (b) => alphabet[b % alphabet.length]).join('')
  return `${base}${MANUAL_SUFFIX}${random}`
}
