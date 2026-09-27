import { useState } from 'react'
import { TimelineStrip, type ScrubberBucket, type ScrubberPreset } from '@skyhook-io/k8s-ui'

const MIN = 60_000
const HOUR = 60 * MIN
const NOW = Math.floor(Date.now() / (5 * MIN)) * 5 * MIN
const wrap = { width: 760 }

const presets: ScrubberPreset[] = [
  { label: '1h', ms: HOUR },
  { label: '6h', ms: 6 * HOUR },
  { label: '24h', ms: 24 * HOUR },
  { label: 'All', ms: 24 * HOUR },
]

// Deterministic background churn with a rollout at -4h and a CrashLoopBackOff storm at -90m.
function buckets(fromMs: number, toMs: number, stepMs = 5 * MIN): ScrubberBucket[] {
  const out: ScrubberBucket[] = []
  let i = 0
  for (let t = fromMs; t < toMs; t += stepMs, i++) {
    const agoMin = (NOW - t) / MIN
    let total = 3 + ((i * 37) % 7)
    let warnings = i % 11 === 0 ? 1 : 0
    if (agoMin <= 245 && agoMin >= 225) total += 28
    if (agoMin <= 95 && agoMin >= 60) {
      total += 22
      warnings += 14
    }
    out.push({ startMs: t, endMs: t + stepMs, total, warnings })
  }
  return out
}

function Stateful({
  selectionMs,
  lens,
  live = true,
  loading = false,
  historyFloorAgoMs,
}: {
  selectionMs: number
  lens?: boolean
  live?: boolean
  loading?: boolean
  historyFloorAgoMs?: number
}) {
  const domain = { fromMs: NOW - 24 * HOUR, toMs: NOW }
  const [selection, setSelection] = useState({ fromMs: NOW - selectionMs, toMs: NOW })
  const [lensRange, setLens] = useState({ fromMs: NOW - selectionMs / 4, toMs: NOW })
  const floor = historyFloorAgoMs ? NOW - historyFloorAgoMs : undefined
  const data = buckets(floor ?? selection.fromMs, selection.toMs)
  return (
    <div style={wrap}>
      <TimelineStrip
        buckets={loading ? [] : data}
        loading={loading}
        domain={domain}
        selection={selection}
        onSelectionChange={setSelection}
        maxSelectionMs={24 * HOUR}
        presets={presets}
        onPresetSelect={(p) => setSelection({ fromMs: NOW - p.ms, toMs: NOW })}
        historyUnavailableBeforeMs={floor}
        lens={lens ? lensRange : undefined}
        onLensChange={lens ? setLens : undefined}
        liveState={live ? { kind: 'live', latched: true } : { kind: 'frozen', asOfMs: NOW - 45 * MIN, newEventCount: 37 }}
        onLiveChipClick={() => {}}
      />
    </div>
  )
}

export function Live6h() {
  return <Stateful selectionMs={6 * HOUR} />
}

export function WithLens() {
  return <Stateful selectionMs={6 * HOUR} lens />
}

export function Frozen() {
  return <Stateful selectionMs={6 * HOUR} live={false} />
}

export function ShortRecording() {
  return <Stateful selectionMs={6 * HOUR} historyFloorAgoMs={2 * HOUR} />
}

export function Loading() {
  return <Stateful selectionMs={6 * HOUR} loading />
}
