import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { alignedTimeAxisTicks, datesNeedYear, formatFullTimestamp, formatMetricValue, formatTimeAxisLabels, formatTimestamp, labelWidthEm } from './format'

describe('formatMetricValue', () => {
  it('scales unitless values with SI prefixes instead of a runaway k', () => {
    expect(formatMetricValue(19_027_100, '')).toBe('19.0M')
    expect(formatMetricValue(38_054_300, '')).toBe('38.1M')
    expect(formatMetricValue(57_081, '')).toBe('57.1k')
    expect(formatMetricValue(4_200_000_000, '')).toBe('4.20G')
    expect(formatMetricValue(812, '')).toBe('812')
  })

  it('formats seconds and negative values', () => {
    expect(formatMetricValue(0.25, 'seconds')).toBe('250ms')
    expect(formatMetricValue(90, 'seconds')).toBe('1.5m')
    expect(formatMetricValue(-3.5, '')).toBe('-3.50')
    expect(formatMetricValue(-2048, 'bytes')).toBe('-2.0 KiB')
  })
})

describe('time axis labels', () => {
  // Day boundaries depend on the local zone; pin one with a DST change. The
  // expected strings come from the same Intl calls, so any locale passes.
  let previousTZ: string | undefined
  beforeAll(() => {
    previousTZ = process.env.TZ
    process.env.TZ = 'America/New_York'
  })
  afterAll(() => {
    if (previousTZ === undefined) delete process.env.TZ
    else process.env.TZ = previousTZ
  })

  const at = (month: number, day: number, hour: number, minute = 0) => new Date(2026, month - 1, day, hour, minute).getTime() / 1000
  const day = (unix: number) => new Date(unix * 1000).toLocaleDateString([], { month: 'short', day: 'numeric' })
  const ticks = (start: number, end: number, intervals: number) =>
    Array.from({ length: intervals + 1 }, (_, i) => start + ((end - start) / intervals) * i)

  describe('formatTimeAxisLabels (evenly spaced ticks)', () => {
    it('keeps time-only labels for a window of a day or less, even across midnight', () => {
      const sameDay = ticks(at(10, 21, 8), at(10, 21, 14), 6)
      expect(formatTimeAxisLabels(sameDay)).toEqual(sameDay.map(formatTimestamp))
      const overMidnight = ticks(at(10, 21, 20), at(10, 22, 2), 6)
      expect(formatTimeAxisLabels(overMidnight)).toEqual(overMidnight.map(formatTimestamp))
      const fullDay = ticks(at(10, 21, 14, 48), at(10, 22, 14, 48), 6)
      expect(formatTimeAxisLabels(fullDay, 1)).toEqual(fullDay.map(formatTimestamp))
    })

    it('names the day where a new one starts and the time elsewhere', () => {
      const twoDays = ticks(at(10, 21, 14, 48), at(10, 23, 14, 48), 6)
      const labels = formatTimeAxisLabels(twoDays)
      expect(labels[0]).toBe(`${day(twoDays[0])} ${formatTimestamp(twoDays[0])}`)
      expect(labels[1]).toBe(formatTimestamp(twoDays[1]))
      expect(labels[2]).toBe(`${day(twoDays[2])} ${formatTimestamp(twoDays[2])}`)
      expect(labels.filter((label) => label.includes(day(twoDays[0])))).toHaveLength(1)
    })

    it('keeps the time on two end ticks just over a day apart', () => {
      const edges = [at(10, 21, 14, 48), at(10, 22, 14, 49)]
      expect(formatTimeAxisLabels(edges)).toEqual(edges.map((ts) => `${day(ts)} ${formatTimestamp(ts)}`))
    })

    it('falls back to the day at day starts when date and time would not fit', () => {
      const twoDays = ticks(at(10, 21, 14, 48), at(10, 23, 14, 48), 4)
      expect(formatTimeAxisLabels(twoDays, 5)).toEqual([day(twoDays[0]), day(twoDays[1]), '', day(twoDays[3]), ''])
    })

    it('never repeats a bare day across a DST fall-back', () => {
      // 24h steps from 00:30 land on 23:30 after clocks go back on Nov 1.
      const window = ticks(at(10, 31, 0, 30), at(10, 31, 0, 30) + 3 * 86_400, 3)
      expect(formatTimeAxisLabels(window)).toEqual([
        `${day(window[0])} ${formatTimestamp(window[0])}`,
        `${day(window[1])} ${formatTimestamp(window[1])}`,
        formatTimestamp(window[2]),
        `${day(window[3])} ${formatTimestamp(window[3])}`,
      ])
      expect(day(window[1])).toBe(day(window[2]))
    })
  })

  describe('alignedTimeAxisTicks', () => {
    const local = (ts: number) => {
      const d = new Date(ts * 1000)
      return [d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes()]
    }

    it('leaves a window of a day or less to evenly spaced ticks', () => {
      expect(alignedTimeAxisTicks(at(10, 21, 14, 48), at(10, 22, 14, 48), 7)).toBeNull()
    })

    it('puts a 7-day window on consecutive local midnights, labelled with the day', () => {
      const result = alignedTimeAxisTicks(at(10, 21, 2, 48), at(10, 28, 2, 48), 7)!
      expect(result.map(({ ts }) => local(ts))).toEqual([22, 23, 24, 25, 26, 27, 28].map((d) => [10, d, 0, 0]))
      expect(result.map(({ label }) => label)).toEqual(result.map(({ ts }) => day(ts)))
    })

    it('steps by whole days when daily ticks would not fit, and keeps them as the window slides', () => {
      const eleven = alignedTimeAxisTicks(at(10, 17, 1, 48), at(10, 28, 1, 48), 7)!
      expect(eleven.length).toBeLessThanOrEqual(7)
      expect(eleven.every(({ ts }) => local(ts)[2] === 0 && local(ts)[3] === 0)).toBe(true)
      const slid = alignedTimeAxisTicks(at(10, 17, 7, 48), at(10, 28, 7, 48), 7)!
      const shared = (list: { ts: number }[]) => list.map(({ ts }) => ts).filter((ts) => ts >= at(10, 17, 7, 48) && ts <= at(10, 28, 1, 48))
      expect(shared(slid)).toEqual(shared(eleven))
    })

    it('adds clock-hour ticks with time labels on a short multi-day window', () => {
      const result = alignedTimeAxisTicks(at(10, 21, 14, 48), at(10, 23, 2, 48), 7)!
      expect(result.map(({ ts }) => local(ts))).toEqual([
        [10, 21, 18, 0],
        [10, 22, 0, 0],
        [10, 22, 6, 0],
        [10, 22, 12, 0],
        [10, 22, 18, 0],
        [10, 23, 0, 0],
      ])
      expect(result.map(({ label }) => label)).toEqual([
        formatTimestamp(result[0].ts),
        day(result[1].ts),
        formatTimestamp(result[2].ts),
        formatTimestamp(result[3].ts),
        formatTimestamp(result[4].ts),
        day(result[5].ts),
      ])
    })

    it('keeps local midnights across a DST change', () => {
      const result = alignedTimeAxisTicks(at(10, 30, 12), at(11, 3, 12), 7)!
      expect(result.map(({ ts }) => local(ts))).toEqual([31, 1, 2, 3].map((d, i) => [i === 0 ? 10 : 11, d, 0, 0]))
      expect(result[2].ts - result[1].ts).toBe(25 * 3600)
    })

    it('keeps later midnights where DST skips one', () => {
      // Chile springs forward at midnight on Sep 6 2026; that day starts at 01:00.
      process.env.TZ = 'America/Santiago'
      try {
        const result = alignedTimeAxisTicks(at(9, 4, 12), at(9, 11, 0, 30), 7)!
        expect(result.map(({ ts }) => local(ts))).toEqual([
          [9, 5, 0, 0], [9, 6, 1, 0], [9, 7, 0, 0], [9, 8, 0, 0], [9, 9, 0, 0], [9, 10, 0, 0], [9, 11, 0, 0],
        ])
        expect(result[1].label).toBe(day(result[1].ts))
      } finally {
        process.env.TZ = 'America/New_York'
      }
    })

    it('takes a coarser step when the caller says labels collide, and gives up below two ticks', () => {
      const daily = alignedTimeAxisTicks(at(10, 21, 2, 48), at(10, 28, 2, 48), 7, (ticks) => ticks.length <= 4)!
      expect(daily.length).toBeLessThanOrEqual(4)
      expect(alignedTimeAxisTicks(at(10, 21, 2, 48), at(10, 28, 2, 48), 7, () => false)).toBeNull()
      expect(alignedTimeAxisTicks(at(10, 21, 2, 48), at(10, 22, 12), 1)).toBeNull()
    })
  })

  it('names the year on axes and tooltips spanning most of a year or more', () => {
    const twoYears = ticks(at(10, 21, 12), at(10, 21, 12) + 730 * 86_400, 6)
    const withYear = (ts: number) => new Date(ts * 1000).toLocaleDateString([], { year: 'numeric', month: 'short', day: 'numeric' })
    expect(formatTimeAxisLabels(twoYears, 6)).toEqual(twoYears.map(withYear))
    expect(new Set(formatTimeAxisLabels(twoYears, 6)).size).toBe(twoYears.length)
    expect(datesNeedYear(730 * 86_400)).toBe(true)
    expect(datesNeedYear(30 * 86_400)).toBe(false)
    expect(formatFullTimestamp(twoYears[0], true)).toContain(String(new Date(twoYears[0] * 1000).toLocaleDateString([], { year: 'numeric' })))
    expect(formatTimeAxisLabels(ticks(at(10, 21, 2, 48), at(10, 28, 2, 48), 6), 6).some((label) => label.includes('2026'))).toBe(false)
  })

  it('estimates wide characters at a full em', () => {
    expect(labelWidthEm('Oct 21')).toBeCloseTo(3.6)
    expect(labelWidthEm('10月21日')).toBeCloseTo(4 * 0.6 + 2 * 1)
  })

  it('formats tooltips with the full date and time', () => {
    const ts = at(10, 21, 14, 5)
    const label = formatFullTimestamp(ts)
    expect(label).toContain(day(ts))
    expect(label).toContain(new Date(ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }))
  })
})
