import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { VeleroRunMessages, veleroMessageRows, veleroMessagesLabel } from './VeleroRunMessages'

/**
 * "Warnings: 2" was a dead end. The number is on the Backup; the text is in a
 * results file in object storage that only Velero's controller can hand out a
 * link to — so the page could tell an operator something was wrong and nothing
 * about what.
 *
 * The two failures are the interesting part and both are ordinary: the
 * controller is not running, or the storage the link points at is somewhere this
 * process cannot reach. Neither may be swallowed, because a button that does
 * nothing is worse than the bare number it replaced.
 */
describe('the messages behind a run’s counts', () => {
  it('offers to fetch them when a run reported any', () => {
    const html = renderToString(<VeleroRunMessages errors={0} warnings={2} onFetch={() => {}} />)
    // The button says what it shows.
    expect(html).toContain('Show 2 warnings')
  })

  it('says nothing at all for a clean run', () => {
    // No button, no explanation, no row: a run that reported nothing has nothing
    // to explain, and an empty section would imply otherwise.
    expect(renderToString(<VeleroRunMessages errors={0} warnings={0} onFetch={() => {}} />)).toBe('')
  })

  it('names each message’s scope alongside it', () => {
    const html = renderToString(
      <VeleroRunMessages
        errors={0}
        warnings={1}
        messages={{
          errors: [],
          warnings: [{ scope: 'demo-app', message: 'could not restore ConfigMap' }],
        }}
      />,
    )
    expect(html).toContain('demo-app')
    expect(html).toContain('could not restore ConfigMap')
    // Scope and message are separate spans with only a margin between them, so
    // without a real separator a screen reader reads "demo-appcould not
    // restore" — correct on screen, wrong to anyone listening to it.
    expect(html).not.toMatch(/demo-app<\/span><span[^>]*>could not restore/)
    // The offer is gone once they are on screen.
    expect(html).not.toContain('Show 1 warning')
  })

  // The note itself is the host's to build — a denial and a fault read
  // differently, and only the host knows which it got. This asserts the slot is
  // rendered, not what it says.
  it('shows why the fetch failed instead of an empty list', () => {
    const html = renderToString(
      <VeleroRunMessages
        errors={0}
        warnings={2}
        lookupNote={<span>Velero did not answer — check that it is running.</span>}
        onFetch={() => {}}
      />,
    )
    expect(html).toContain('check that it is running')
    // And the offer stays, so a failed fetch is retryable.
    expect(html).toContain('Show 2 warnings')
  })

  // The counts come from the CR and the messages from a file written separately.
  // They can disagree, and saying so is better than rendering nothing under a
  // non-zero number and letting it read as a broken button.
  it('says so when the counts and the file disagree', () => {
    const html = renderToString(
      <VeleroRunMessages errors={0} warnings={2} messages={{ errors: [], warnings: [] }} />,
    )
    expect(html).toContain('results file it returned holds none')
  })

  // The counts sit two lines above this list. They come from the object and the
  // messages come from a file written separately, so a run can report two and
  // return one — and a list of one under "Warnings: 2" is a number the reader
  // cannot reconcile with what is beside it.
  it('says so when it returned fewer than the count promised', () => {
    const html = renderToString(
      <VeleroRunMessages
        errors={0}
        warnings={2}
        messages={{ errors: [], warnings: [{ scope: 'velero', message: 'the only one' }] }}
      />,
    )
    expect(html).toContain('reported 2 on this run and returned 1')
  })

  // Truncation is a shortfall this already explains on its own line; saying it
  // twice, in two different vocabularies, reads as two different problems.
  it('does not also report a mismatch when the list was capped', () => {
    const html = renderToString(
      <VeleroRunMessages
        errors={0}
        warnings={9000}
        messages={{
          errors: [],
          warnings: [{ scope: 'velero', message: 'one of very many' }],
          truncated: true,
        }}
      />,
    )
    expect(html).not.toContain('and returned')
    expect(html).toContain('Only the first')
  })

  it('admits when the list was capped', () => {
    const html = renderToString(
      <VeleroRunMessages
        errors={0}
        warnings={9000}
        messages={{
          errors: [],
          warnings: [{ scope: 'velero', message: 'one of very many' }],
          truncated: true,
        }}
      />,
    )
    expect(html).toContain('Only the first')
  })
})

/**
 * An error and a warning were separated by an icon and a colour, and by nothing
 * else — so to anyone not seeing them, the distinction that decides whether to
 * act simply was not there.
 */
describe('telling an error from a warning without seeing it', () => {
  const both = {
    errors: [{ scope: 'velero', message: 'the failure' }],
    warnings: [{ scope: 'demo-app', message: 'the caution' }],
  }

  it('names the severity in text', () => {
    const html = renderToString(<VeleroRunMessages errors={1} warnings={1} messages={both} />)
    expect(html).toContain('Error: ')
    expect(html).toContain('Warning: ')
  })

  it('announces the region so a late arrival is not silent', () => {
    const html = renderToString(
      <VeleroRunMessages errors={1} warnings={0} messages={{ errors: both.errors, warnings: [] }} />,
    )
    expect(html).toContain('aria-live')
  })

  it('says on the button what it will show', () => {
    expect(veleroMessagesLabel(9, 3)).toBe('Show 9 errors and 3 warnings')
    expect(veleroMessagesLabel(1, 0)).toBe('Show 1 error')
    expect(veleroMessagesLabel(0, 1)).toBe('Show 1 warning')
    // The server returns at most 500, so the label promises no more.
    expect(veleroMessagesLabel(1200, 0)).toBe('Show the first 500 of 1200 messages')
  })

  // A partial failure records one error per failed item: forty PVCs is forty
  // lines saying the same thing.
  it('folds messages that differ only in one object’s name, per scope', () => {
    const pvc = (n: number) => ({ scope: 'payments', message: `error executing custom action (groupResource=persistentvolumeclaims, namespace=payments, name=data-${n}): snapshot quota exceeded` })
    const rows = veleroMessageRows({
      errors: [...Array.from({ length: 40 }, (_, i) => pvc(i)), { scope: 'cluster', message: 'failed to list CSIDrivers' }],
      warnings: [{ scope: 'payments', message: 'volume data skipped: hostPath' }],
    })
    expect(rows).toHaveLength(3)
    expect(rows[0]).toMatchObject({ scope: 'payments', severity: 'error', text: 'error executing custom action (groupResource=persistentvolumeclaims, namespace=payments, name=data-…): snapshot quota exceeded' })
    expect(rows[0].variants).toHaveLength(40)
    expect(rows[0].variants[7]).toBe('7')
    expect(rows.map((r) => r.severity)).toEqual(['error', 'warning', 'error'])
  })

  it('shows the first rows, then offers the rest', () => {
    const errors = Array.from({ length: 150 }, (_, i) => ({ scope: `ns-${i % 15}`, message: `unique failure number ${i} in item ${i * 7}` }))
    const html = renderToString(<VeleroRunMessages errors={150} warnings={0} messages={{ errors, warnings: [] }} />).replace(/<!-- -->/g, '')
    expect(html).toContain('Show all 150')
    expect(html.match(/unique failure number/g)).toHaveLength(10)
    expect(html).toContain('Namespace ns-0 · 10 errors')
  })

  // Two different reasons are two rows, however alike the rest of the text.
  it('never folds messages that differ in a reason rather than a name', () => {
    const rows = veleroMessageRows({ errors: [{ scope: 'velero', message: 'backup failed: Unauthorized' }, { scope: 'velero', message: 'backup failed: Forbidden' }], warnings: [] })
    expect(rows.map((r) => r.text)).toEqual(['backup failed: Unauthorized', 'backup failed: Forbidden'])
    const same = veleroMessageRows({ errors: [{ scope: 'velero', message: 'backup failed: Forbidden' }, { scope: 'velero', message: 'backup failed: Forbidden' }], warnings: [] })
    expect(same).toHaveLength(1)
    expect(same[0].variants).toHaveLength(2)
  })
})

it('keeps quoted failure reasons separate but folds structured object identities', () => {
  const rows = (a: string, b: string) => veleroMessageRows({ errors: [a, b].map((message) => ({ scope: 'velero', message })), warnings: [] })
  expect(rows('request failed: "Forbidden"', 'request failed: "Unauthorized"')).toHaveLength(2)
  expect(rows('request failed: "Forbidden."', 'request failed: "Unauthorized."')).toHaveLength(2)
  expect(rows('request failed: 401', 'request failed: 403')).toHaveLength(2)
  expect(rows('request failed: "Forbidden:"', 'request failed: "Unauthorized:"')).toHaveLength(2)
  expect(rows('cannot read name=x', 'cannot read name=y')).toHaveLength(1)
  expect(rows('cannot read ns/first', 'cannot read ns/second')).toHaveLength(1)
  expect(rows('cannot read persistentvolumeclaims/first', 'cannot read persistentvolumeclaims/second')).toHaveLength(1)
  // A bare word, quoted or not, has no syntax saying it names an object.
  expect(rows('cannot read "data-pg-0"', 'cannot read "data-pg-1"')).toHaveLength(2)
})

it('keeps lowercase and hyphenated quoted reasons, and digit-bearing error codes, apart', () => {
  const rows = (a: string, b: string) => veleroMessageRows({ errors: [a, b].map((message) => ({ scope: 'velero', message })), warnings: [] })
  expect(rows('request failed: "access-denied"', 'request failed: "quota-exceeded"')).toHaveLength(2)
  expect(rows('request failed: access-denied', 'request failed: quota-exceeded')).toHaveLength(2)
  expect(rows('request failed: "E1001"', 'request failed: "E1002"')).toHaveLength(2)
  expect(rows('upload failed: err-404', 'upload failed: err-500')).toHaveLength(2)
  // The same reason on two objects still folds, by the object.
  const folded = rows('request failed for name=a: "access-denied"', 'request failed for name=b: "access-denied"')
  expect(folded).toHaveLength(1)
  expect(folded[0].variants).toEqual(['a', 'b'])
})

it('bounds the folding index for many distinct messages just under the cutoffs', () => {
  // Each message is distinct, just under the per-message cutoffs, with more identities than are indexed.
  const words = (n: number) => Array.from({ length: 380 }, (_, i) => `w${(n * 7 + i) % 97}`).join(' ')
  const messages = Array.from({ length: 100 }, (_, n) => `${words(n)} name=a${n} name=b${n} ns/c${n} ns/d${n} ns/e${n} ns/f${n}`.slice(0, 4000))
  const stats = { keys: 0, chars: 0 }
  const rows = veleroMessageRows({ errors: messages.map((message) => ({ scope: 'velero', message })), warnings: [] }, stats)
  expect(rows).toHaveLength(100)
  expect(rows.map((r) => r.text)).toEqual(messages)
  // At most four positions a message, and never past the index budget.
  expect(stats.keys).toBeLessThanOrEqual(4 * messages.length)
  expect(stats.chars).toBeLessThanOrEqual(512 * 1024 + 4096)
})

it('folds exact repeats of any length', () => {
  const long = 'word '.repeat(10000)
  const rows = veleroMessageRows({ errors: [long, long, 'other'].map((message) => ({ scope: 'velero', message })), warnings: [] })
  expect(rows).toHaveLength(2)
  expect(rows[0]).toMatchObject({ text: long })
  expect(rows[0].variants).toHaveLength(2)
})

it('bounds folding work for long messages, keeping their text verbatim', () => {
  for (const common of ['word '.repeat(1000), 'x'.repeat(4096), '界'.repeat(1500)]) {
    const messages = [common + 'name=first', common + 'name=second']
    const rows = veleroMessageRows({ errors: messages.map((message) => ({ scope: 'velero', message })), warnings: [] })
    expect(rows.map((row) => row.text)).toEqual(messages)
    expect(rows.every((row) => row.variants.length === 0)).toBe(true)
  }
  const message = 'word '.repeat(10000)
  expect(veleroMessageRows({ errors: [{ scope: 'velero', message }], warnings: [] })[0].text).toBe(message)
})
