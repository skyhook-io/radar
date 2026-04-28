// Some Kubernetes controllers serialize timestamps using Go's default
// `time.Time.String()` format (e.g. "2026-07-27 08:27:41.123456789 +0000 UTC")
// rather than RFC 3339. This happens whenever the field is typed as `string`
// (or a `map[string]string` value) instead of `metav1.Time`, since JSON
// serialization can't apply the standard RFC 3339 marshaler.
//
// ECMAScript only requires `Date(...)` to parse the ISO 8601 subset, so Safari
// rejects this format and returns Invalid Date. V8 (Chrome/Node) is lenient
// and accepts it, which makes the bug invisible in test runners.
//
// Known affected schema: CloudNativePG `Cluster.status.certificates.expirations`
// (issue #554). Reuse this helper when integrating any other CRD whose
// timestamps come through as raw strings.

const GO_TIME_PATTERN =
  /^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(\.\d+)?\s+([+-]\d{2})(\d{2})\b/

export function parseGoTimeString(s: string): Date {
  const m = s.match(GO_TIME_PATTERN)
  if (m) {
    const [, date, time, frac, tzHour, tzMin] = m
    const ms = frac ? frac.slice(0, 4) : ''
    return new Date(`${date}T${time}${ms}${tzHour}:${tzMin}`)
  }
  return new Date(s)
}
