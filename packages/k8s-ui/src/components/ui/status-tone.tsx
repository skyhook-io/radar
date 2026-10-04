import { type HealthLevel } from '../resources/resource-utils';
import { TONE_TEXT_CLASS } from './severity-tone';

// StatusDot + mapHealthToTone are typed helpers over the canonical OSS
// status vocabulary defined in `packages/k8s-ui/src/theme/components.css`.
// The `.status-*` CSS classes (.status-healthy / .status-degraded /
// .status-alert / .status-unhealthy / .status-neutral / .status-unknown)
// are the source of truth for color; this file just adds dot-color
// tokens and a tone-normalization helper.
//
// For pill-shaped status badges, use the canonical pattern directly:
//
//   <span className={`badge ${healthColors[tone]}`}>...</span>
//
// (no typed component wrapper — that pattern is widely used across OSS
// already and adding a wrapper would split the call sites without
// fitting the count-badge / button-pill / icon-pill variants OSS uses).
//
// Vocabulary (escalating urgency):
//   healthy   — running / live / passing  (emerald)
//   degraded  — warning / slow / advisory (amber)
//   alert     — high-severity but not foundationally broken (orange)
//   unhealthy — critical / failing / blocked (red)
//   neutral   — informational / scope counter (sky)
//   unknown   — terminal / unknown / no-action-needed (slate)
//
// `alert` is the intermediate tier between degraded and unhealthy. Used
// when the data carries a 3-step severity gradient (Problems pages,
// Audit findings, Cert expiry buckets) — without it, "high" and either
// "critical" or "medium" collapse into the same color.

// `StatusTone` is intentionally a name for `HealthLevel` at call sites
// where the concern is presentation (which color/dot to render) rather
// than the underlying health state — `tone: StatusTone` reads cleaner
// than `tone: HealthLevel`. The two are structurally identical today;
// keeping a separate name preserves the option to diverge later (e.g.
// a future `brand` tone that isn't a health state).
export type StatusTone = HealthLevel;

// Solid dot colors for StatusDot (the .status-* CSS classes are pill
// backgrounds, too soft for a tiny solid indicator). Kept in lockstep
// with the CSS palette so tone names stay aligned.
const DOT_CLASS: Record<StatusTone, string> = {
  healthy: 'bg-emerald-500',
  degraded: 'bg-amber-500',
  alert: 'bg-orange-500',
  unhealthy: 'bg-rose-500',
  neutral: 'bg-sky-500',
  unknown: 'bg-slate-400',
};

/** Solid fill for a tone, e.g. a bar or meter segment. */
export function toneFillClass(tone: StatusTone): string {
  return DOT_CLASS[tone]
}

// Text for a value read in a tone. Healthy reads as plain primary text: only
// what needs a look is coloured.
const TEXT_CLASS: Record<StatusTone, string> = {
  healthy: 'text-theme-text-primary',
  degraded: TONE_TEXT_CLASS.amber,
  alert: TONE_TEXT_CLASS.orange,
  unhealthy: TONE_TEXT_CLASS.red,
  unknown: 'text-theme-text-tertiary',
  neutral: 'text-theme-text-secondary',
}

export function toneTextClass(tone: StatusTone): string {
  return TEXT_CLASS[tone]
}

// Unknown ranks above healthy: a combined reading never looks calmer than a
// part that could not be read.
const TONE_RANK: Record<StatusTone, number> = { healthy: 0, neutral: 0, unknown: 1, degraded: 2, alert: 3, unhealthy: 4 }

export function worseTone(a: StatusTone, b: StatusTone): StatusTone {
  return TONE_RANK[b] > TONE_RANK[a] ? b : a
}

// Normalize the variety of severity / health vocabularies that flow in
// from APIs (Problems, Audit, multi-cluster aggregation endpoints) onto
// a single tone. Inputs are case-insensitive. Returns 'unknown' for
// unrecognized values.
export function mapHealthToTone(input: string): StatusTone {
  switch (input.toLowerCase()) {
    case 'healthy':
    case 'ok':
    case 'success':
    case 'passing':
      return 'healthy';
    case 'degraded':
    case 'warning':
    case 'warn':
    case 'medium':
      return 'degraded';
    case 'high':
    case 'alert':
      return 'alert';
    case 'unhealthy':
    case 'danger':
    case 'critical':
    case 'error':
    case 'failed':
      return 'unhealthy';
    case 'neutral':
    case 'info':
      return 'neutral';
    default:
      return 'unknown';
  }
}

export interface StatusDotProps {
  tone: StatusTone;
  /** Dot diameter. Default 'sm'. */
  size?: 'xs' | 'sm' | 'md';
  className?: string;
}

const DOT_SIZE: Record<NonNullable<StatusDotProps['size']>, string> = {
  xs: 'h-1 w-1',
  sm: 'h-1.5 w-1.5',
  md: 'h-2 w-2',
};

export function StatusDot({ tone, size = 'sm', className = '' }: StatusDotProps) {
  return (
    <span
      className={`inline-block rounded-full ${DOT_SIZE[size]} ${DOT_CLASS[tone]} ${className}`}
      aria-hidden
    />
  );
}
