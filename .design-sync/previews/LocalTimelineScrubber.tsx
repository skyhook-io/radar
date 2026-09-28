import { useState } from 'react'
import { LocalTimelineScrubber } from '@skyhook-io/k8s-ui'

const MIN = 60_000
const HOUR = 60 * MIN
const NOW = Math.floor(Date.now() / MIN) * MIN
const frame = { width: 820 } as const

const workloads = ['checkout-api', 'payments-worker', 'frontend', 'redis', 'orders-db']

// Six hours of informer churn: a rollout at -3h and a CrashLoopBackOff storm at -50m.
function ringEvents(spanMs = 6 * HOUR) {
  const events: any[] = []
  let i = 0
  for (let t = NOW - spanMs; t <= NOW; t += 90_000, i++) {
    const agoMin = (NOW - t) / MIN
    const burst = agoMin <= 185 && agoMin >= 170 ? 6 : agoMin <= 55 && agoMin >= 35 ? 5 : 1
    for (let k = 0; k < burst + (i % 3 === 0 ? 1 : 0); k++) {
      const storm = agoMin <= 55 && agoMin >= 35 && k % 2 === 0
      const name = workloads[(i + k) % workloads.length]
      events.push({
        id: `ev-${i}-${k}`,
        timestamp: new Date(t + k * 7000).toISOString(),
        source: storm ? 'k8s_event' : 'informer',
        kind: storm ? 'Pod' : 'Deployment',
        namespace: 'shop',
        name: storm ? `${name}-7c9d8b5f4-x2kqp` : name,
        eventType: storm ? 'Warning' : 'update',
        reason: storm ? 'BackOff' : undefined,
        message: storm ? 'Back-off restarting failed container' : undefined,
      })
    }
  }
  return events
}

const EVENTS = ringEvents()

function Stateful({
  windowMs,
  withLens,
  liveState,
  loading,
  events = EVENTS,
}: {
  windowMs: number
  withLens?: boolean
  liveState?: { kind: 'live'; latched: boolean } | { kind: 'frozen'; asOfMs: number }
  loading?: boolean
  events?: any[]
}) {
  const toMs = liveState?.kind === 'frozen' ? liveState.asOfMs : NOW
  const [selection, setSelection] = useState({ fromMs: toMs - windowMs, toMs })
  const [lens, setLens] = useState({ fromMs: toMs - 60 * MIN, toMs: toMs - 30 * MIN })
  return (
    <div style={frame}>
      <LocalTimelineScrubber
        events={events}
        loading={loading}
        selection={selection}
        onSelectionChange={setSelection}
        lens={withLens ? lens : undefined}
        onLensChange={withLens ? setLens : undefined}
        lensResizable={withLens}
        liveState={liveState}
        onLiveChipClick={() => {}}
      />
    </div>
  )
}

export function LiveLastSixHours() {
  return <Stateful windowMs={6 * HOUR} liveState={{ kind: 'live', latched: true }} />
}

export function LensOnCrashLoopStorm() {
  return <Stateful windowMs={3 * HOUR} withLens liveState={{ kind: 'live', latched: false }} />
}

export function FrozenWithNewerEvents() {
  return <Stateful windowMs={2 * HOUR} liveState={{ kind: 'frozen', asOfMs: NOW - 40 * MIN }} />
}

export function ShortRingJustStarted() {
  return (
    <Stateful
      windowMs={HOUR}
      liveState={{ kind: 'live', latched: true }}
      events={EVENTS.filter((e) => new Date(e.timestamp).getTime() > NOW - 20 * MIN)}
    />
  )
}

export function Loading() {
  return <Stateful windowMs={HOUR} loading events={[]} liveState={{ kind: 'live', latched: true }} />
}
