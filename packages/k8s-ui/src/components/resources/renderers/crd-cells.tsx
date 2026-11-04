import { Badge } from '../../ui/Badge'
import { Tooltip } from '../../ui/Tooltip'
import { compareKubeAwareVersions, type CRDVersion } from '../../../utils/crd-schema'

export function CRDVersionLabel({ version, showStorage = true }: { version: CRDVersion; showStorage?: boolean }) {
  return <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap">
    <span className="font-mono">{version.name}</span>
    {showStorage && version.storage && <Tooltip content="Objects are saved in this version; the API server converts them to the others on read">
      <Badge tone="structural" size="sm">storage</Badge>
    </Tooltip>}
    {version.deprecated && <Tooltip content={version.deprecationWarning || 'Requests for this version return a deprecation warning'}>
      <Badge severity="warning" size="sm">deprecated</Badge>
    </Tooltip>}
    {!version.served && <Tooltip content="The API server doesn't serve this version, so clients can't read or write it">
      <Badge severity="neutral" size="sm">not served</Badge>
    </Tooltip>}
  </span>
}

// Rendered widths (px) measured in the list: a version name character, the
// deprecated badge with its gap, the gap between tokens, the "+N" count, and the
// Served Versions column's content width (w-64 less its padding).
const VERSION_CHAR_PX = 8.5
const DEPRECATED_BADGE_PX = 80
const TOKEN_GAP_PX = 8
const COUNT_PX = 24
const VERSIONS_CELL_CONTENT_PX = 224

function versionTokenWidth(version: CRDVersion): number {
  return version.name.length * VERSION_CHAR_PX + (version.deprecated ? DEPRECATED_BADGE_PX : 0)
}

// One line per row and never a partial version: storage is left to the drawer,
// and versions that would not fit whole collapse into a count.
export function CRDVersionsCell({ versions }: { versions: CRDVersion[] }) {
  const served = versions.filter(v => v.served).sort((a, b) => compareKubeAwareVersions(a.name, b.name))
  if (served.length === 0) return <span className="text-sm text-theme-text-tertiary">None served</span>
  let used = 0
  let shown = 0
  for (const version of served.slice(0, 2)) {
    const width = versionTokenWidth(version) + (shown ? TOKEN_GAP_PX : 0)
    const hiddenAfter = served.length - shown - 1
    const budget = VERSIONS_CELL_CONTENT_PX - (hiddenAfter > 0 ? COUNT_PX + TOKEN_GAP_PX : 0)
    if (shown && used + width > budget) break
    used += width
    shown++
  }
  const rest = served.slice(shown)
  return <div className="flex items-center gap-2 whitespace-nowrap text-sm text-theme-text-secondary">
    {served.slice(0, shown).map(v => <CRDVersionLabel key={v.name} version={v} showStorage={false} />)}
    {rest.length > 0 && <Tooltip wrapperClassName="shrink-0 whitespace-nowrap" content={rest.map(v => [v.name, v.storage && 'storage', v.deprecated && (v.deprecationWarning ? `deprecated: ${v.deprecationWarning}` : 'deprecated')].filter(Boolean).join(' · ')).join('; ')}>
      <span className="shrink-0 whitespace-nowrap">+{rest.length}</span>
    </Tooltip>}
  </div>
}
