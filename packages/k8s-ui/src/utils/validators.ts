// Pure validators for user-provided form inputs.
//
// Centralised here so the same rules apply to every form (Helm install
// release name, Audit Settings ignored namespaces, Port Forward local
// port, anything else that lands later). Reasons live next to the
// constraints so reviewers can sanity-check us against the underlying
// Kubernetes / Helm specs instead of folklore.

/**
 * Result shape for all validators. `valid: true` is success; otherwise
 * `error` is a short, user-readable explanation suitable for inline
 * form feedback (no leading capital, no trailing period — call sites
 * compose them into sentences).
 */
export type ValidationResult =
  | { valid: true }
  | { valid: false; error: string }

const RFC1123_LABEL_MAX = 63
// Kubernetes namespace / pod / service names are RFC 1123 labels:
// lowercase alphanumeric or '-', start and end alphanumeric, ≤63 chars.
// Source: k8s.io/apimachinery/pkg/util/validation.IsDNS1123Label.
const RFC1123_LABEL_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

const RFC1123_SUBDOMAIN_MAX = 253
// RFC 1123 subdomain = one or more labels joined by '.', total ≤253.
const RFC1123_SUBDOMAIN_RE =
  /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

const HELM_RELEASE_NAME_MAX = 53
// Helm release names: DNS subdomain plus the additional limit of 53
// chars so the generated resource names (`<release>-<chart>-<hash>`)
// fit under the 63-char label cap. Source:
// https://helm.sh/docs/chart_best_practices/conventions/#chart-names

/**
 * Validates a Kubernetes RFC 1123 label. Used for namespace names,
 * pod / service / configmap / secret names — anything Kubernetes
 * accepts as a single-segment identifier.
 */
export function validateRFC1123Label(input: string): ValidationResult {
  if (input.length === 0) {
    return { valid: false, error: 'must not be empty' }
  }
  if (input.length > RFC1123_LABEL_MAX) {
    return {
      valid: false,
      error: `must be at most ${RFC1123_LABEL_MAX} characters (got ${input.length})`,
    }
  }
  if (input !== input.toLowerCase()) {
    return {
      valid: false,
      error: 'must be lowercase (Kubernetes names are case-sensitive and only allow [a-z0-9-])',
    }
  }
  if (!RFC1123_LABEL_RE.test(input)) {
    return {
      valid: false,
      error: 'must contain only lowercase letters, numbers, and hyphens, and start/end with a letter or number',
    }
  }
  return { valid: true }
}

/**
 * Validates a Kubernetes RFC 1123 subdomain (labels joined by dots).
 * Used for Helm chart names, image registries — anything that allows
 * dotted segments. NOT for pod/namespace names; use the label form.
 */
export function validateRFC1123Subdomain(input: string): ValidationResult {
  if (input.length === 0) {
    return { valid: false, error: 'must not be empty' }
  }
  if (input.length > RFC1123_SUBDOMAIN_MAX) {
    return {
      valid: false,
      error: `must be at most ${RFC1123_SUBDOMAIN_MAX} characters (got ${input.length})`,
    }
  }
  if (input !== input.toLowerCase()) {
    return {
      valid: false,
      error: 'must be lowercase',
    }
  }
  if (!RFC1123_SUBDOMAIN_RE.test(input)) {
    return {
      valid: false,
      error: 'must contain only lowercase letters, numbers, hyphens, and dots, and each segment must start/end with a letter or number',
    }
  }
  // Per-label cap: K8s' IsDNS1123Subdomain enforces ≤ 63 chars
  // PER dot-separated label on top of the 253-char total. The
  // regex above doesn't catch that; without this check a single
  // 200-char label passes here and only fails server-side.
  for (const label of input.split('.')) {
    if (label.length > RFC1123_LABEL_MAX) {
      return {
        valid: false,
        error: `each dot-separated label must be at most ${RFC1123_LABEL_MAX} characters (got ${label.length} for "${label}")`,
      }
    }
  }
  return { valid: true }
}

/**
 * Validates a Helm release name. Same shape as an RFC 1123 label
 * (no dots) capped at 53 chars (Helm's hard limit; longer names
 * produce resources that exceed K8s' 63-char label limit).
 *
 * Why label and not subdomain: Helm itself permits dots in release
 * names, but most charts compose resource names as
 * `<release>-<chart>-<hash>` which must be valid DNS-1123 labels.
 * A dotted release name like `my.app` produces resources whose
 * names fail K8s label validation server-side. Reject the dot up
 * front so the user sees a clear inline error instead of a server
 * 422 after submit.
 */
export function validateHelmReleaseName(input: string): ValidationResult {
  if (input.length === 0) {
    return { valid: false, error: 'must not be empty' }
  }
  if (input.length > HELM_RELEASE_NAME_MAX) {
    return {
      valid: false,
      // No trailing period — call sites compose this into a sentence
      // and append their own.
      error: `must be at most ${HELM_RELEASE_NAME_MAX} characters (got ${input.length}); Helm caps release names so derived resource names fit under K8s' 63-char limit`,
    }
  }
  return validateRFC1123Label(input)
}

/**
 * Validates a TCP/UDP port number provided as a free-text input.
 *
 * Accepts:
 *   - a number-like string with no whitespace, no leading zeros, no '+'
 *   - in [1, 65535]
 *
 * Rejects '0' (reserved), negative numbers, fractional numbers,
 * scientific notation, and anything containing non-digit characters.
 *
 * Returns the parsed integer alongside the validation result so call
 * sites don't have to re-parse.
 */
export type PortValidationResult =
  | { valid: true; value: number }
  | { valid: false; error: string }

export function validatePort(input: string | number): PortValidationResult {
  // Coerce a number argument to its decimal string form so the same
  // strict rules (no fractional, no NaN) apply regardless of how the
  // value reached us. We then re-parse to integer.
  const raw = typeof input === 'number' ? String(input) : input
  if (raw === '' || raw == null) {
    return { valid: false, error: 'port is required' }
  }
  // Reject anything other than digits — '+1', '1.0', '1e3', ' 80 '
  // all fail here. Catching them as parse failures gives clearer
  // feedback than silently truncating via Number().
  if (!/^\d+$/.test(raw)) {
    return {
      valid: false,
      error: 'port must be a whole number between 1 and 65535',
    }
  }
  const n = Number(raw)
  if (!Number.isInteger(n) || n < 1 || n > 65535) {
    return {
      valid: false,
      error: 'port must be between 1 and 65535 (0 is reserved)',
    }
  }
  return { valid: true, value: n }
}
