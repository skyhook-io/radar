// Axis helpers shared by the chart's full and compact layouts. Pure so the
// tick choices can be tested without rendering.

export interface ChartLayout {
  width: number
  height: number
  marginLeft: number
  marginRight: number
  marginTop: number
  marginBottom: number
  fontSize: number
  /** Number of Y intervals between the lowest and highest tick. */
  yIntervals: number
  /** Number of X intervals between the first and last tick. */
  xIntervals: number
}

// The compact layout trades tick density for legible text: a 400-unit
// viewBox scaled into a ~280px pane keeps 14-unit text near 10px, where the
// full layout's 11-unit text in a 1000-unit viewBox would be 3px.
export function chartLayout(compact: boolean, dashboard = false): ChartLayout {
  if (dashboard && !compact) return { width: 600, height: 240, marginLeft: 90, marginRight: 24, marginTop: 12, marginBottom: 28, fontSize: 14, yIntervals: 2, xIntervals: 2 }
  return compact
    ? { width: 400, height: 260, marginLeft: 60, marginRight: 14, marginTop: 12, marginBottom: 28, fontSize: 14, yIntervals: 1, xIntervals: 1 }
    : { width: 1000, height: 300, marginLeft: 84, marginRight: 40, marginTop: 10, marginBottom: 30, fontSize: 11, yIntervals: 4, xIntervals: 6 }
}

export function isCountUnit(unit: string): boolean {
  return unit === 'count' || unit === 'restarts'
}

/**
 * Top of an integer axis: the smallest 1/2/5×10ⁿ step that reaches `max`
 * within `intervals` ticks, rounded up to a whole step. `max` 0 or 1 gives 1,
 * 18 gives 20 (step 5), 3 gives 3 (step 1).
 */
export function integerAxisTop(max: number, intervals: number): number {
  const target = Math.max(1, Math.ceil(max))
  const steps = Math.max(1, intervals)
  let magnitude = 1
  for (;;) {
    for (const base of [1, 2, 5]) {
      const step = base * magnitude
      if (step * steps >= target) return step * Math.ceil(target / step)
    }
    magnitude *= 10
  }
}

/** Tick values from yMin to yMax; integer axes never produce fractional ticks. */
export function yAxisValues(yMin: number, yMax: number, intervals: number, integer: boolean): number[] {
  if (integer && yMin === 0) {
    const step = yMax / intervals
    const wholeStep = Number.isInteger(step) ? step : Math.max(1, Math.ceil(step))
    const values: number[] = []
    for (let value = 0; value <= yMax; value += wholeStep) values.push(value)
    if (values[values.length - 1] !== yMax) values.push(yMax)
    return values
  }
  return Array.from({ length: intervals + 1 }, (_, i) => yMin + ((yMax - yMin) / intervals) * i)
}
