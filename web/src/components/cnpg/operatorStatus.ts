import type { CNPGOperatorVerdict } from '../../api/cnpg'

export interface CNPGOperatorBannerModel {
  /** The operator watching these namespaces is not leading, so their CNPG status may be stale. */
  stale: { reasons: string[]; namespaces: string[] } | null
  /** Why the admission webhook rejects CNPG writes, when it does. */
  rejects: string | null
}

function sentence(s: string): string {
  const t = s.trim()
  if (!t) return t
  return t[0].toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? '' : '.')
}

/**
 * What the fleet and cluster pages say about the operator for the namespaces
 * they show. Only an observed "not leading" or "webhook rejects" raises
 * anything: an unknown or unwatched state stays quiet here and the Operator
 * screen explains it.
 */
export function cnpgOperatorBannerModel(verdicts: Record<string, CNPGOperatorVerdict> | undefined, namespaces: string[]): CNPGOperatorBannerModel | null {
  if (!verdicts) return null
  const staleNs: string[] = []
  const reasons = new Set<string>()
  let rejects: string | null = null
  for (const ns of [...new Set(namespaces)].sort()) {
    const v = verdicts[ns]
    if (!v) continue
    if (v.state === 'notReconciling') {
      staleNs.push(ns)
      for (const r of v.reasons ?? []) reasons.add(sentence(r))
    }
    if (v.webhookRejects === true && v.webhookReason) rejects = sentence(v.webhookReason)
  }
  if (staleNs.length === 0 && !rejects) return null
  return { stale: staleNs.length > 0 ? { reasons: [...reasons], namespaces: staleNs } : null, rejects }
}

/** A dialog's note about the operator: a warning when it is observed down, a note when unknown. */
export function cnpgOperatorActionNote(v: CNPGOperatorVerdict | undefined): { tone: 'warning' | 'info'; text: string } | null {
  if (!v) return null
  if (v.state === 'notReconciling') {
    const why = (v.reasons ?? []).map(sentence).join(' ')
    return {
      tone: 'warning',
      text: `The CloudNativePG operator is not reconciling this cluster. ${why} The API server may accept this change, but nothing acts on it until an operator instance leads again.`.replace(/\s+/g, ' ').trim(),
    }
  }
  if (v.state === 'unknown' && v.unknown) {
    return { tone: 'info', text: `Radar could not confirm the operator is reconciling: ${v.unknown}.` }
  }
  return null
}
