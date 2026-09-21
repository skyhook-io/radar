import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { VeleroBSLRenderer } from './VeleroBSLRenderer'

const bsl = (phase: string) => ({
  metadata: { name: 'dr-replica', namespace: 'velero' },
  spec: { provider: 'aws', objectStorage: { bucket: 'dr' } },
  status: { phase },
})

const completed = (name: string, completedAt: string) => ({
  namespace: 'velero',
  name,
  phase: 'Completed',
  completed: completedAt,
})

/**
 * A storage location's phase is a fact about a bucket. What an operator opens the
 * page to learn is what that fact costs them, and the answer is the set of
 * backups they cannot restore from while it holds — every one of which Velero
 * still reports as Completed, accurately, because they did complete.
 */
describe('what a storage location holds', () => {
  it('says what an unavailable location is holding back', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Unavailable')}
        storedBackups={[completed('dr-nightly', '2026-08-14T01:00:00Z')]}
      />,
    )
    expect(html).toContain('not something you can restore from')
    expect(html).toContain('dr-nightly')
  })

  // The panel said "not something you can restore from" and then, four lines
  // later, "Most recent restorable point" — two claims about the same backups
  // that cannot both hold.
  it('does not call a point restorable on a location it just said was not', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Unavailable')}
        storedBackups={[completed('dr-nightly', '2026-08-14T01:00:00Z')]}
      />,
    )
    expect(html).toContain('not something you can restore from')
    expect(html).not.toContain('Most recent restorable point')
    expect(html).toContain('once this location is reachable again')
  })

  it('calls it a restorable point when the location can actually serve it', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[completed('nightly', '2026-08-14T01:00:00Z')]}
      />,
    )
    expect(html).toContain('Most recent restorable point')
  })

  it('agrees in number', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Unavailable')}
        storedBackups={[
          completed('a', '2026-08-14T01:00:00Z'),
          completed('b', '2026-08-13T01:00:00Z'),
        ]}
      />,
    )
    // Counts what `restorable` counts: an expired Completed backup is stored
    // here too, so "completed backups stored here" described a larger set.
    expect(html).toContain('2 backups here are complete and still inside their retention')
    expect(html).toContain('they are not something')
  })

  // The consequence belongs to a broken location. On a healthy one it would be
  // a warning about nothing.
  it('states no consequence when the location is available', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[completed('nightly', '2026-08-14T01:00:00Z')]}
      />,
    )
    expect(html).not.toContain('not something you can restore from')
    expect(html).toContain('nightly')
  })

  // Only a Completed backup is a restorable point; the rest are stored here but
  // are not something to go back to.
  it('does not count a failed backup as restorable', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Unavailable')}
        storedBackups={[
          completed('good', '2026-08-14T01:00:00Z'),
          { namespace: 'velero', name: 'bad', phase: 'Failed' },
        ]}
      />,
    )
    expect(html).toContain('1 backup here is complete and still inside its retention')
  })

  // The second thing a phase cannot see. Velero deletes expired backups, so one
  // still listed is either awaiting collection or waiting on a controller that
  // is not running — and this cluster's controller is deliberately stopped.
  it('does not offer an expired backup as a restore point', () => {
    const past = new Date(Date.now() - 86_400_000).toISOString()
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[{ ...completed('stale', '2026-01-01T00:00:00Z'), expiration: past }]}
      />,
    )
    expect(html).toContain('passed its retention')
    expect(html).not.toContain('Most recent restorable point')
  })

  it('still counts a backup inside its retention', () => {
    const future = new Date(Date.now() + 86_400_000).toISOString()
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Unavailable')}
        storedBackups={[{ ...completed('fresh', '2026-08-14T01:00:00Z'), expiration: future }]}
      />,
    )
    expect(html).toContain('1 backup here is complete and still inside its retention')
    expect(html).not.toContain('passed its retention')
  })

  // "not counted above" pointed at a line that only renders for an unhealthy
  // location, so on a healthy one it referred to nothing.
  it('states the expired count without leaning on a line that may not be there', () => {
    const past = new Date(Date.now() - 86_400_000).toISOString()
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[
          { ...completed('stale', '2026-01-01T00:00:00Z'), expiration: past },
          completed('fresh', '2026-08-14T01:00:00Z'),
        ]}
      />,
    )
    expect(html).toContain('not something to plan a recovery around')
    // Not "cannot be restored": Velero would still restore it until the
    // collector removes it, so the panel states the intent, not a false absence.
    expect(html).not.toContain('no longer a restore point')
    expect(html).not.toContain('not counted above')
  })

  // Every stored backup expired: the location holds things, and none of them
  // are any use.
  it('says so when nothing stored is restorable at all', () => {
    const past = new Date(Date.now() - 86_400_000).toISOString()
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[{ ...completed('stale', '2026-01-01T00:00:00Z'), expiration: past }]}
      />,
    )
    expect(html).toContain('Nothing stored here is still inside its retention')
  })

  // The distinction the whole feature rests on: not having looked is not the
  // same as having looked and found nothing.
  it('claims nothing while the lookup is unresolved', () => {
    const html = renderToString(<VeleroBSLRenderer data={bsl('Unavailable')} />)
    expect(html).not.toContain('No backup in this namespace names this location')
    expect(html).not.toContain('Stored Here')
  })

  it('says a location genuinely holds nothing when it does', () => {
    const html = renderToString(<VeleroBSLRenderer data={bsl('Available')} storedBackups={[]} />)
    expect(html).toContain('No backup in this namespace names this location')
  })
})

/**
 * Velero writes why a location is unavailable into `status.message`, and the
 * BSL object carries no conditions, so that field is the only place the reason
 * exists. A page opened from an unavailable-location problem has to show it -
 * the same way the sibling BackupRepository renderer leads with its own reason.
 */
describe('an unavailable location Velero gave a reason for', () => {
  const bslMsg = (phase: string, message?: string) => ({
    metadata: { name: 'dr-replica', namespace: 'velero' },
    spec: { provider: 'aws', objectStorage: { bucket: 'dr' } },
    status: { phase, ...(message !== undefined ? { message } : {}) },
  })

  it('leads with the reason Velero gave', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bslMsg('Unavailable', 'BackupStorageLocation is invalid: rpc error: code = Unknown desc = NoSuchBucket')}
      />,
    )
    expect(html).toContain('NoSuchBucket')
  })

  // Velero can set Unavailable with no message. Saying nothing would leave a red
  // badge and no consequence, so the page states what unavailable costs.
  it('names the consequence when Velero gave no reason', () => {
    const html = renderToString(<VeleroBSLRenderer data={bslMsg('Unavailable')} />)
    expect(html).toContain('cannot be written')
  })

  it('says nothing alarming when the location is available', () => {
    const html = renderToString(
      <VeleroBSLRenderer data={bslMsg('Available', 'stale message left on the object')} />,
    )
    expect(html).not.toContain('Storage Location Unavailable')
    expect(html).not.toContain('stale message left on the object')
    expect(html).not.toContain('cannot be written')
  })

  // A location Velero has not validated yet has no phase. It is not unavailable,
  // so it gets no alarm even if a message lingers from a prior state.
  it('raises no alarm on a location Velero has not reported on', () => {
    const html = renderToString(<VeleroBSLRenderer data={bslMsg('', 'old message')} />)
    expect(html).not.toContain('Storage Location Unavailable')
    expect(html).not.toContain('old message')
  })
})

/**
 * The list counts what is stored; the restorable point is derived from what
 * completed. On a healthy location those are the only two numbers on the page,
 * and when they disagree something has to say why — "Backups (2)" above a
 * single restorable point otherwise reads as one of them being lost, rather
 * than as one of them still running.
 *
 * The panel states how many ARE whole restore points rather than counting the
 * rest and naming them: the phases it would have to name include `Deleting`,
 * which already ran, and the *PartiallyFailed phases, which mean part of
 * the data made it. "Has not completed" is false for both.
 */
describe('a location holding runs that are not restore points', () => {
  const atPhase = (name: string, phase: string) => ({ namespace: 'velero', name, phase })

  it('reconciles the stored count with the restorable one', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[
          completed('nightly', '2026-08-14T01:00:00Z'),
          atPhase('stalled', 'InProgress'),
        ]}
      />,
    )
    expect(html).toContain('1 of 2 backups stored here is a complete restore point')
    // Both are stored, so both are listed — the sentence explains the list, it
    // does not filter it.
    expect(html).toContain('nightly')
    expect(html).toContain('stalled')
    // The completed one is still a restore point; the note must not read as
    // though nothing here is usable.
    expect(html).toContain('Most recent restorable point')
  })

  it('says nothing when every stored backup is a restore point', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[completed('nightly', '2026-08-14T01:00:00Z')]}
      />,
    )
    expect(html).not.toContain('complete restore point')
  })

  // A Deleting backup already ran and is now on its way out, and a
  // PartiallyFailed one finished with errors. Any wording that calls either
  // "not completed" is false — which is why the sentence counts the whole
  // restore points instead of characterising these.
  it('does not claim a deleting or partially failed backup never completed', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={[
          atPhase('on-its-way-out', 'Deleting'),
          atPhase('half-made-it', 'PartiallyFailed'),
        ]}
      />,
    )
    expect(html).not.toContain('not completed')
    expect(html).toContain('None of the 2 backups stored here is a complete restore point')
    expect(html).not.toContain('Most recent restorable point')
  })
})

/**
 * A location Velero has not validated yet has no phase at all. Treating that as
 * "not healthy, therefore unreachable" turns missing data into a claim about the
 * bucket — and this panel is read to decide whether a restore is possible.
 */
describe('a location Velero has not reported on', () => {
  const noPhase = {
    metadata: { name: 'fresh', namespace: 'velero' },
    spec: { provider: 'aws', objectStorage: { bucket: 'b' } },
    status: {},
  }

  it('does not say backups here cannot be restored from', () => {
    const html = renderToString(
      <VeleroBSLRenderer data={noPhase} storedBackups={[completed('nightly', '2026-08-14T01:00:00Z')]} />,
    )
    expect(html).not.toContain('not something you can restore from')
    expect(html).toContain('has not reported a phase')
  })

  it('does not imply the location is unreachable', () => {
    const html = renderToString(
      <VeleroBSLRenderer data={noPhase} storedBackups={[completed('nightly', '2026-08-14T01:00:00Z')]} />,
    )
    expect(html).not.toContain('reachable again')
  })
})

/**
 * These sentences take counts, and a count of one is the common case on a small
 * cluster. Both read as broken English at n=1 — "whether these is restorable",
 * "None of the 1 backups" — which is the shape of a sentence assembled from
 * fragments without anyone reading the result.
 */
describe('the sentences at a count of one', () => {
  const oneOf = (phase: string, backups: Array<{ namespace: string; name: string; phase: string }>) =>
    renderToString(
      <VeleroBSLRenderer
        data={{ metadata: { name: 'loc', namespace: 'velero' }, spec: {}, status: { phase } }}
        storedBackups={backups}
      />,
    )

  it('reads correctly when a single backup is not a restore point', () => {
    const html = oneOf('Available', [{ namespace: 'velero', name: 'only', phase: 'InProgress' }])
    expect(html).toContain('The backup stored here is not a complete restore point')
    expect(html).not.toContain('None of the 1')
  })

  it('reads correctly when a single backup sits on an unvalidated location', () => {
    const html = oneOf('', [{ namespace: 'velero', name: 'only', phase: 'Completed' }])
    expect(html).toContain('whether the backup still inside its retention can be restored from')
    // The plural form must not leak into the singular sentence.
    expect(html).not.toContain('backups still inside their retention')
  })
})

/**
 * Velero keeps every backup until its TTL expires, so an hourly schedule with a
 * long retention leaves thousands in one location. The server sends a page; the
 * panel has to say so, and its sentences have to count the location rather than
 * the page — otherwise "1 of 200" describes a cap, not a cluster.
 */
describe('a location holding more backups than the list shows', () => {
  const page = Array.from({ length: 200 }, (_, i) => ({
    namespace: 'velero',
    name: `nightly-${i}`,
    phase: i === 0 ? 'Completed' : 'Failed',
    completed: i === 0 ? '2026-08-14T01:00:00Z' : undefined,
  }))

  const html = () =>
    renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={page}
        storedTotal={2000}
        listTruncated
      />,
    )

  it('says the list is a page', () => {
    expect(html()).toContain('Showing the 200 most recent of 2000')
  })

  it('counts the location, not the page', () => {
    // 1 restorable out of 2000 held — not out of the 200 that fit.
    expect(html()).toContain('1 of 2000 backups stored here')
  })
})

/**
 * Velero keeps every backup until its TTL expires, so an hourly schedule with a
 * long retention leaves thousands in one location and the server sends a page.
 * The counts describe the location and the list is a page of it — deriving a
 * count from the page and printing it beside the location's total makes the two
 * numbers disagree on exactly the locations big enough to need this panel.
 */
describe('a location whose list is a page', () => {
  const page = Array.from({ length: 3 }, (_, i) => ({
    namespace: 'velero',
    name: `nightly-${i}`,
    phase: 'Failed',
  }))

  it('reports what the location holds, not what the page shows', () => {
    const html = renderToString(
      <VeleroBSLRenderer
        data={bsl('Available')}
        storedBackups={page}
        storedTotal={700}
        restorableTotal={412}
        expiredTotal={285}
      />,
    )
    expect(html).toContain('412 of 700')
    expect(html).toContain('285 stored')
    // The page holds three failed backups. Counting it would have said none of
    // the 700 is a restore point, on a location holding 412.
    expect(html).not.toContain('None of the 700')
  })

  // A host that does not wire the lookup still gets a truthful panel from what
  // it does have.
  it('falls back to the page when the server counts are not wired', () => {
    const html = renderToString(
      <VeleroBSLRenderer data={bsl('Available')} storedBackups={[completed('only-one', '2026-08-16T01:00:00Z')]} />,
    )
    expect(html).toContain('only-one')
  })
})
