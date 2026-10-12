import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CronValue, TimeValue, cronHint } from './ScheduleValue'

const render = (node: React.ReactElement) => renderToString(node)

/**
 * The expression stays on screen verbatim — it is the data operators copy and
 * diff. What is under test is the annotation around it, and above all the cases
 * where there must not be one.
 */
describe('CronValue', () => {
  it('keeps the expression exactly as written', () => {
    expect(render(<CronValue cron="0 2 * * *" />)).toContain('0 2 * * *')
  })

  // The tooltip renders into a portal on hover, so the markup carries the
  // affordance rather than the wording. Without the affordance it is a secret.
  it('marks a schedule it understands as worth hovering', () => {
    const html = render(<CronValue cron="0 2 * * *" />)
    expect(html).toContain('cursor-help')
    expect(html).toContain('border-dotted')
  })

  it('reads the shapes these policies actually use', () => {
    expect(cronHint('0 2 * * *')).toBe('Daily at 2:00')
    expect(cronHint('0 */6 * * *')).toBe('Every 6 hours')
    expect(cronHint('* * * * *')).toBe('Every minute')
    expect(cronHint('30 3 * * 1-5')).toBe('Weekdays at 3:30')
  })

  it('offers no reading it cannot stand behind', () => {
    expect(cronHint('0 2 1 * *')).toBeUndefined()
    expect(cronHint('0 0 2 * * *', 'seconds')).toBeUndefined()
    expect(cronHint('0 2 * * *', 'seconds')).toBeUndefined()
    expect(cronHint('')).toBeUndefined()
  })

  // A tooltip that repeats the value teaches the reader that the underline is
  // not worth hovering, which costs more than the one it would have explained.
  it('stays a plain value when it cannot describe the schedule', () => {
    const monthly = render(<CronValue cron="0 2 1 * *" />)
    expect(monthly).toContain('0 2 1 * *')
    expect(monthly).not.toContain('cursor-help')
  })

  // CloudNativePG counts seconds first. Read as five fields, `0 0 2 * * *`
  // describes midnight rather than 2am — a backup window stated an hour wrong.
  it('never reads a seconds-first expression as if it were five fields', () => {
    const html = render(<CronValue cron="0 0 2 * * *" dialect="seconds" />)
    expect(html).toContain('0 0 2 * * *')
    expect(html).not.toContain('cursor-help')
    expect(html).not.toContain('Daily at')
  })

  // The same guard from the other direction: even a five-field expression is
  // left alone once the field is declared seconds-first, so a hand-written
  // value cannot pick up the wrong dialect's meaning.
  it('leaves a five-field expression alone in a seconds-first field', () => {
    const html = render(<CronValue cron="0 2 * * *" dialect="seconds" />)
    expect(html).not.toContain('Daily at 2:00')
  })

  it('renders nothing surprising for an empty schedule', () => {
    expect(render(<CronValue cron="" />)).not.toContain('cursor-help')
  })
})

describe('TimeValue', () => {
  it('answers "is this recent" rather than printing a machine timestamp', () => {
    const html = render(<TimeValue timestamp={new Date(Date.now() - 6 * 3600_000).toISOString()} />)
    expect(html).toContain('cursor-help')
    // The RFC3339 string is what the hover carries, not what the row shows.
    expect(html).not.toMatch(/\dT\d\d:\d\d:\d\dZ/)
  })

  // A bare "6h" under "Started" or "Finished" reads as a duration.
  it('says the moment was in the past', () => {
    expect(render(<TimeValue timestamp={new Date(Date.now() - 6 * 3600_000).toISOString()} />)).toContain('6h ago')
  })

  it('says so when the thing never happened', () => {
    expect(render(<TimeValue timestamp={undefined} fallback="Never" />)).toContain('Never')
  })
})
