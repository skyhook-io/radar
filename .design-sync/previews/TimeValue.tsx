import { TimeValue, Property, PropertyList } from '@skyhook-io/k8s-ui'

const box = { width: 380 } as const
const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR

export function InPropertyList() {
  return (
    <div style={box}>
      <PropertyList>
        <Property label="Last Schedule" value={<TimeValue timestamp={ago(14 * MIN)} />} />
        <Property label="Last Successful" value={<TimeValue timestamp={ago(6 * HOUR)} />} />
        <Property label="Created" value={<TimeValue timestamp={ago(23 * DAY)} />} />
      </PropertyList>
    </div>
  )
}

export function NeverHappened() {
  return (
    <div style={box}>
      <PropertyList>
        <Property label="Last Execution" value={<TimeValue fallback="Never" />} />
        <Property label="Completion Time" value={<TimeValue />} />
      </PropertyList>
    </div>
  )
}

export function InlineInSentence() {
  return (
    <p style={{ width: 380, margin: 0, fontSize: 12 }} className="text-theme-text-secondary">
      Most recent restorable point: <TimeValue timestamp={ago(3 * HOUR)} /> ago
    </p>
  )
}
