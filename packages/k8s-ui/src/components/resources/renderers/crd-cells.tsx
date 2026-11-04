import { Badge } from '../../ui/Badge'
import { Tooltip } from '../../ui/Tooltip'
import type { CRDVersion } from '../../../utils/crd-schema'

export function CRDVersionLabel({ version, showStorage = true }: { version: CRDVersion; showStorage?: boolean }) {
  return <span className="inline-flex items-center gap-1 whitespace-nowrap">
    <span className="font-mono">{version.name}</span>
    {showStorage && version.storage && <Tooltip content="Objects are saved in this version; the API server converts them to the others on read">
      <Badge tone="note" size="sm">storage</Badge>
    </Tooltip>}
    {version.deprecated && <Tooltip content={version.deprecationWarning || 'Requests for this version return a deprecation warning'}>
      <Badge severity="warning" size="sm">deprecated</Badge>
    </Tooltip>}
    {!version.served && <Tooltip content="The API server doesn't serve this version, so clients can't read or write it">
      <Badge severity="neutral" size="sm">not served</Badge>
    </Tooltip>}
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
