import { useMemo, useState } from 'react'
import { clsx } from 'clsx'
import { NetworkPolicyAppliesTo, NetworkPolicyRulesView } from './NetworkPolicyRulesView'
import { NetworkPolicyGraphView } from './NetworkPolicyGraphView'
import { summarizeNetworkPolicy } from '../../../utils/network-policy-summary'

interface NetworkPolicyDiagramProps {
  spec: any
  staged?: boolean
  namespace?: string
}

type View = 'rules' | 'graph'

const VIEW_KEY = 'radar.networkPolicy.view'

// Storage can throw (private mode, embedded hosts, SSR); the view then just isn't remembered.
function readView(): View {
  try {
    return localStorage.getItem(VIEW_KEY) === 'graph' ? 'graph' : 'rules'
  } catch {
    return 'rules'
  }
}

function writeView(view: View) {
  try {
    localStorage.setItem(VIEW_KEY, view)
  } catch {
    // not remembered
  }
}

/** A core NetworkPolicy as who it applies to, then its rules as cards or as a graph. */
export function NetworkPolicyDiagram({ spec, staged = false, namespace }: NetworkPolicyDiagramProps) {
  const summary = useMemo(() => summarizeNetworkPolicy(spec, namespace), [spec, namespace])
  const [view, setView] = useState<View>(readView)
  const choose = (next: View) => {
    setView(next)
    writeView(next)
  }

  return (
    <div className="space-y-3 min-w-0">
      <div className="flex items-start justify-between gap-3">
        <NetworkPolicyAppliesTo summary={summary} />
        <div className="flex shrink-0 rounded-md border border-theme-border p-0.5" role="group" aria-label="Policy view">
          {(['rules', 'graph'] as const).map((option) => (
            <button
              key={option}
              type="button"
              aria-pressed={view === option}
              onClick={() => choose(option)}
              className={clsx(
                'rounded px-2 py-0.5 text-[11px] transition-colors',
                view === option ? 'bg-theme-hover text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-primary',
              )}
            >
              {option === 'rules' ? 'Rules' : 'Graph'}
            </button>
          ))}
        </div>
      </div>
      {view === 'rules' ? <NetworkPolicyRulesView summary={summary} staged={staged} /> : <NetworkPolicyGraphView summary={summary} staged={staged} />}
    </div>
  )
}
