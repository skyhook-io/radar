import { formatBytes } from '@skyhook-io/k8s-ui/utils/format'
import { isPayloadProtocol, protocolName, type ProtocolData } from './trafficFilters'

export function TrafficData({ entries, compact = false, stacked = false, directional = false, explainOverlap = false }: {
  entries: ProtocolData[]
  compact?: boolean
  /** One figure per line, for narrow columns. */
  stacked?: boolean
  directional?: boolean
  explainOverlap?: boolean
}) {
  if (!entries.some(e => e.sent > 0 || e.recv > 0)) return <>—</>
  if (entries.length === 1) return <>{formatBytes(entries[0].sent + entries[0].recv)}</>
  const payloads = entries.filter(e => e.protocol !== null && isPayloadProtocol(e.protocol))
  const caption = explainOverlap && entries.some(e => e.protocol?.toLowerCase() === 'tcp') && payloads.length > 0
    ? `${payloads.map(e => protocolName(e.protocol!)).join('/')} body bytes and TCP connection bytes can describe the same calls; shown separately.`
    : null
  const figures = entries.map(e => `${protocolName(e.protocol ?? '')} ${directional
    ? `${formatBytes(e.sent)} sent · ${formatBytes(e.recv)} received`
    : formatBytes(e.sent + e.recv)}`)
  if (compact && !stacked) return <>{figures.join(' · ')}</>
  if (compact) return <>{figures.map((figure, index) => <div key={entries[index].protocol} className="whitespace-nowrap">{figure}</div>)}</>
  return <>
    {figures.map((figure, index) => <div key={entries[index].protocol}>{figure}</div>)}
    {caption && <div className="mt-1 text-theme-text-tertiary font-normal">{caption}</div>}
  </>
}
