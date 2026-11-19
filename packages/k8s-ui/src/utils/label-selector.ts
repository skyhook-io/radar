// A Kubernetes label selector (matchLabels + matchExpressions), evaluated as
// metav1.LabelSelectorAsSelector does for the four set-based operators.

export interface LabelSelectorRequirement { key?: string; operator?: string; values?: string[] }
export interface LabelSelector { matchLabels?: Record<string, string>; matchExpressions?: LabelSelectorRequirement[] }

const SUPPORTED = ['In', 'NotIn', 'Exists', 'DoesNotExist']

/** The first operator this matcher can't evaluate; callers must not guess a result for such a selector. */
export function unsupportedSelectorOperator(selector: LabelSelector): string | undefined {
  const term = (selector.matchExpressions ?? []).find(t => !SUPPORTED.includes(t?.operator ?? ''))
  return term ? String(term.operator ?? '') : undefined
}

export function matchesLabelSelector(selector: LabelSelector, labels: Record<string, string>): boolean {
  const has = (key: string) => Object.prototype.hasOwnProperty.call(labels, key)
  return Object.entries(selector.matchLabels ?? {}).every(([key, value]) => has(key) && labels[key] === value)
    && (selector.matchExpressions ?? []).every(({ key = '', operator, values = [] }) => {
      switch (operator) {
        case 'In': return has(key) && values.includes(labels[key])
        case 'NotIn': return !has(key) || !values.includes(labels[key])
        case 'Exists': return has(key)
        default: return !has(key)
      }
    })
}
