// Whether a namespace may submit Workloads to a ClusterQueue, from the
// ClusterQueue's namespaceSelector. Kueue reads a null selector as "no
// namespace" and an empty one ({}) as "every namespace", so only a selector
// with terms needs the namespace's labels.

export type NamespaceEligibility =
  | { state: 'eligible'; basis: 'all' | 'labels' }
  | { state: 'not_eligible'; basis: 'none' | 'labels' }
  /** The labels are needed and weren't read, or the selector can't be evaluated. */
  | { state: 'unknown'; why: 'labels_unread' | 'unsupported_operator'; operator?: string }

import { matchesLabelSelector, unsupportedSelectorOperator, type LabelSelector } from '../../utils/label-selector'

export type { LabelSelector }

function selectorHasTerms(selector: LabelSelector | null | undefined): boolean {
  return !!selector && (Object.keys(selector.matchLabels ?? {}).length > 0 || (selector.matchExpressions?.length ?? 0) > 0)
}

/** Evaluating the selector would use the namespace's labels (it has terms, all with supported operators). */
export function needsNamespaceLabels(selector: LabelSelector | null | undefined): boolean {
  const result = namespaceEligibility(selector, undefined)
  return result.state === 'unknown' && result.why === 'labels_unread'
}

/** labels is undefined when the namespace's labels weren't read. */
export function namespaceEligibility(selector: LabelSelector | null | undefined, labels: Record<string, string> | undefined): NamespaceEligibility {
  if (selector == null) return { state: 'not_eligible', basis: 'none' }
  if (!selectorHasTerms(selector)) return { state: 'eligible', basis: 'all' }
  const unsupported = unsupportedSelectorOperator(selector)
  if (unsupported !== undefined) return { state: 'unknown', why: 'unsupported_operator', operator: unsupported }
  if (!labels) return { state: 'unknown', why: 'labels_unread' }
  return matchesLabelSelector(selector, labels) ? { state: 'eligible', basis: 'labels' } : { state: 'not_eligible', basis: 'labels' }
}
