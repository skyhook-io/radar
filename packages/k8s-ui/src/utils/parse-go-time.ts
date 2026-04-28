// Some Kubernetes controllers serialize timestamps using Go's default
// `time.Time.String()` format (e.g. "2026-07-27 08:27:41.123456789 +0000 UTC")
// when the field is typed as `string` instead of `metav1.Time`. ECMAScript
// only requires `Date(...)` to parse the ISO 8601 subset, so Safari rejects
// this format while V8 (Chrome/Node) accepts it leniently — which makes the
// bug invisible in test runners. Known affected schema: CloudNativePG
// `Cluster.status.certificates.expirations`.

const GO_TIME_PATTERN =
  /^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(\.\d+)?\s+([+-]\d{2})(\d{2})\b/

export function parseGoTimeString(s: string): Date {
  const m = s.match(GO_TIME_PATTERN)
  if (m) {
    const [, date, time, frac, tzHour, tzMin] = m
    // Go's ".999999999" format strips trailing zeros, so nanos=100_000_000
    // emits ".1". The ISO 8601 simplified profile requires exactly ".sss";
    // pad to 3 digits before truncating so Safari accepts the result.
    const ms = frac ? '.' + (frac.slice(1) + '000').slice(0, 3) : ''
    return new Date(`${date}T${time}${ms}${tzHour}:${tzMin}`)
  }
  return new Date(s)
}
