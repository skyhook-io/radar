import { FreshnessControl } from '@skyhook-io/k8s-ui'

const cell = { display: 'flex', justifyContent: 'flex-end', width: 320, padding: 10 } as const
const min = (n: number) => Date.now() - n * 60_000
const sec = (n: number) => Date.now() - n * 1_000
const noop = () => {}

export function AutoUpdating() {
  return (
    <div style={cell}>
      <FreshnessControl mode="auto" dataUpdatedAt={sec(12)} connectionState="connected" />
    </div>
  )
}

export function Snapshot() {
  return (
    <div style={cell}>
      <FreshnessControl mode="snapshot" dataUpdatedAt={min(5)} onRefresh={noop} connectionState="connected" />
    </div>
  )
}

export function Paused() {
  return (
    <div style={cell}>
      <FreshnessControl mode="auto" dataUpdatedAt={sec(40)} paused connectionState="connected" />
    </div>
  )
}

export function Reconnecting() {
  return (
    <div style={cell}>
      <FreshnessControl mode="auto" dataUpdatedAt={min(8)} connectionState="disconnected" onRefresh={noop} />
    </div>
  )
}
