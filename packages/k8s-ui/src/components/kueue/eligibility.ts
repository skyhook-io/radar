// Whether a namespace may submit Workloads to a ClusterQueue, from the
// ClusterQueue's namespaceSelector. Kueue reads a null selector as "no
// namespace" and an empty one ({}) as "every namespace", so only a selector
// with terms needs the namespace's labels.

export type NamespaceEligibility =
  | { state: 'eligible'; basis: 'all' | 'labels' }
  | { state: 'not_eligible'; basis: 'none' | 'labels' }
  /** The labels are needed and weren't read, or the selector can't be evaluated. */
  | { state: 'unknown'; why: 'labels_unread' | 'unsupported_operator'; operator?: string }

interface Requirement { key?: string; operator?: string; values?: string[] }
export interface LabelSelector { matchLabels?: Record<string, string>; matchExpressions?: Requirement[] }

export function selectorHasTerms(selector: LabelSelector | null | undefined): boolean {
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
  const unsupported = (selector.matchExpressions ?? []).find(term => !['In', 'NotIn', 'Exists', 'DoesNotExist'].includes(term?.operator ?? ''))
  if (unsupported) return { state: 'unknown', why: 'unsupported_operator', operator: String(unsupported?.operator ?? '') }
  if (!labels) return { state: 'unknown', why: 'labels_unread' }
  const has = (key: string) => Object.prototype.hasOwnProperty.call(labels, key)
  const matches = Object.entries(selector.matchLabels ?? {}).every(([key, value]) => has(key) && labels[key] === value)
    && (selector.matchExpressions ?? []).every(({ key = '', operator, values = [] }) => {
      switch (operator) {
        case 'In': return has(key) && values.includes(labels[key])
        case 'NotIn': return !has(key) || !values.includes(labels[key])
        case 'Exists': return has(key)
        default: return !has(key)
      }
    })
  return matches ? { state: 'eligible', basis: 'labels' } : { state: 'not_eligible', basis: 'labels' }
}
