import { Badge } from '../../ui/Badge'
import type { CRDVersion } from '../../../utils/crd-schema'

export function CRDVersionLabel({ version, showStorage = true }: { version: CRDVersion; showStorage?: boolean }) {
  return <span className="inline-flex items-center gap-1 whitespace-nowrap" title={version.deprecationWarning}>
    <span className="font-mono">{version.name}</span>
    {showStorage && version.storage && <Badge tone="note" size="sm">storage</Badge>}
    {version.deprecated && <Badge severity="warning" size="sm">deprecated</Badge>}
    {!version.served && <Badge severity="neutral" size="sm">not served</Badge>}
  </span>
}

// The storage marker only tells versions apart; on a single served version it
// would repeat on nearly every row of the list.
export function CRDVersionsCell({ versions }: { versions: CRDVersion[] }) {
  const served = versions.filter(v => v.served)
  if (served.length === 0) return <span className="text-sm text-theme-text-tertiary">None served</span>
  return <div className="flex items-center gap-2 overflow-hidden text-sm text-theme-text-secondary">
    {served.map(v => <CRDVersionLabel key={v.name} version={v} showStorage={served.length > 1} />)}
  </div>
}
