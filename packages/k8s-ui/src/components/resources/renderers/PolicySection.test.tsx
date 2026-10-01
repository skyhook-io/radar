import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PolicySection } from './PolicySection'
import { RadarUpgradeContext } from '../../ui/RadarUpgradeNote'
import type { PolicyResourceResponse } from '../../../types/policy'

const base: PolicyResourceResponse = {
  evaluated: true,
  status: 'ready',
  liveUpdates: true,
  counts: { pass: 0, fail: 0, warn: 0, error: 0, skip: 0 },
  findings: [],
}
const resp = (over: Partial<PolicyResourceResponse>): PolicyResourceResponse => ({ ...base, ...over })


// The unhappy paths carry this section. An empty finding list means several
// unrelated things, and rendering them all as blank space would tell an
// operator they are compliant in the cases where nothing was checked.
describe('PolicySection — what an empty result means', () => {
  it('says which rules failed when there are violations', () => {
    const html = renderToString(<PolicySection data={resp({
      counts: { pass: 2, fail: 1, warn: 0, error: 0, skip: 0 },
      findings: [{ policy: 'require-run-as-nonroot', result: 'fail', message: 'must set runAsNonRoot' }],
    })} />)
    expect(html).toContain('require-run-as-nonroot')
    expect(html).toContain('must set runAsNonRoot')
    // The passing checks are counted, not listed — otherwise two failures drown
    // in a wall of green on a heavily-policed workload.
    expect(html).toContain('2 other checks passing')
  })

  it('distinguishes "everything passed" from "nothing was checked"', () => {
    const passing = renderToString(<PolicySection data={resp({
      counts: { pass: 5, fail: 0, warn: 0, error: 0, skip: 0 },
    })} />)
    expect(passing).toContain('All 5 checks passing')

    const unchecked = renderToString(<PolicySection data={resp({})} />)
    expect(unchecked).toContain('No policy applies')
    expect(unchecked).not.toContain('passing')
  })

  it('never claims compliance when the results could not be read', () => {
    const html = renderToString(<PolicySection data={resp({
      evaluated: false, status: 'deferred', reasonCode: 'rbac_denied',
    })} />)
    expect(html).toContain('has not been checked')
    expect(html).toContain('rbac_denied')
    expect(html).not.toContain('passing')
  })

  it('says results are still loading during warmup rather than reporting clean', () => {
    const html = renderToString(<PolicySection data={resp({ evaluated: false, status: 'warmup' })} />)
    expect(html).toContain('still loading')
  })

  it('renders nothing at all when no policy engine is installed', () => {
    // A disclaimer on every cluster without a policy engine is pure noise.
    expect(renderToString(<PolicySection data={resp({ evaluated: false, status: 'not_installed' })} />)).toBe('')
    expect(renderToString(<PolicySection data={null} />)).toBe('')
  })
})

describe('PolicySection — partial coverage', () => {
  it('admits when some report families were unreadable, even while passing', () => {
    // "All checks passing" plus an unreadable family is a claim of coverage we
    // do not have. The note alone does not undo it — "All" is the word that gets
    // read, and the note is the small print under a green shield.
    const html = renderToString(<PolicySection data={resp({
      counts: { pass: 3, fail: 0, warn: 0, error: 0, skip: 0 },
      deniedGroups: ['openreports.io'],
    })} />)
    expect(html).toContain('3 checks passing')
    expect(html).not.toContain('All 3 checks passing')
    expect(html).toContain('may be incomplete')
    expect(html).toContain('openreports.io')
  })

  // Staleness dates the answer, it does not shrink it: "as of the last sync, all
  // passed" is still a true sentence, so this one keeps the word.
  it('still says all passed when the only caveat is a frozen index', () => {
    const html = renderToString(<PolicySection data={resp({
      counts: { pass: 3, fail: 0, warn: 0, error: 0, skip: 0 }, liveUpdates: false,
    })} />)
    expect(html).toContain('All 3 checks passing')
    expect(html).toContain('not updating live')
  })

  it('admits when the results are frozen', () => {
    const html = renderToString(<PolicySection data={resp({
      counts: { pass: 1, fail: 0, warn: 0, error: 0, skip: 0 }, liveUpdates: false,
    })} />)
    expect(html).toContain('not updating live')
  })
})

describe('PolicySection — request states', () => {
  it('shows loading rather than an empty result', () => {
    expect(renderToString(<PolicySection data={null} loading />)).toContain('Loading policy results')
  })

  it('treats a permission denial as an expected state, not a red failure', () => {
    const denied = Object.assign(new Error('forbidden'), { status: 403 })
    const html = renderToString(<PolicySection data={null} error={denied} />)
    expect(html).toContain('don’t have permission')
    expect(html).not.toContain('text-red')
  })

  it('keeps a genuine fault loud', () => {
    const html = renderToString(<PolicySection data={null} error={new Error('connection reset')} />)
    expect(html).toContain('connection reset')
    expect(html).toContain('text-red')
  })
})

/**
 * Findings the caller may not read are dropped from `findings` AND from
 * `counts`, so the screen has nothing left to reveal that anything is missing.
 * A resource whose only failure came from a report family this identity cannot
 * read then renders as passing everything — an all-clear on a security surface,
 * derived from an answer that was never fully asked.
 */
describe('when results were withheld from this caller', () => {
  const withheld: PolicyResourceResponse = {
    ...base,
    counts: { pass: 3, fail: 0, warn: 0, error: 0, skip: 0 },
    findings: [],
    withheldByFamily: 1,
  }

  it('does not claim everything passed', () => {
    const html = renderToString(<PolicySection data={withheld} />)
    expect(html).not.toContain('All 3 checks passing')
    expect(html).toContain('3 checks passing')
  })

  it('says what is missing and why', () => {
    const html = renderToString(<PolicySection data={withheld} />)
    expect(html).toContain('not shown here')
    expect(html).toContain('permission')
  })

  it('agrees in number', () => {
    const html = renderToString(
      <PolicySection data={{ ...withheld, withheldByFamily: 2 }} />,
    )
    expect(html).toContain('2 results are not shown')
  })

  // The ordinary case must keep its plain claim — the note only appears when
  // something was actually held back.
  it('still claims all-clear when nothing was withheld', () => {
    const html = renderToString(
      <PolicySection data={{ ...base, counts: { ...base.counts, pass: 3 } }} />,
    )
    expect(html).toContain('All 3 checks passing')
    expect(html).not.toContain('not shown here')
  })
})

// An older in-cluster Radar behind a newer frontend (Radar Hub) has no policy
// endpoint. That is an expected state, so it gets an upgrade prompt, not red.
describe('PolicySection — connected Radar predates policy results', () => {
  const unsupported = Object.assign(new Error('Policy results need Radar v1.10.0 or newer'), {
    radarUpgrade: { feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: 'v1.7.2' },
  })

  it('prompts an upgrade instead of reporting a failure', () => {
    const html = renderToString(<PolicySection data={null} error={unsupported} />)
    expect(html).toContain('Policy results')
    expect(html).toContain('need Radar v1.10 or newer. You&#x27;re on v1.7.2.')
    expect(html).not.toContain('Could not load policy results')
    expect(html).not.toContain('text-red-400')
  })

  it('hands the upgrade to the host when it offers a flow', () => {
    const html = renderToString(
      <RadarUpgradeContext.Provider value={{ onRequestUpgrade: () => undefined }}>
        <PolicySection data={null} error={unsupported} />
      </RadarUpgradeContext.Provider>,
    )
    expect(html).toContain('<button')
    expect(html).toContain('Upgrade Radar')
  })

  it('links to instructions when the host has no flow', () => {
    const html = renderToString(
      <RadarUpgradeContext.Provider value={{ upgradeHref: 'https://radarhq.io/docs/configuration/in-cluster' }}>
        <PolicySection data={null} error={unsupported} />
      </RadarUpgradeContext.Provider>,
    )
    expect(html).toContain('href="https://radarhq.io/docs/configuration/in-cluster"')
    expect(html).toContain('How to upgrade')
  })

  it('keeps a genuine failure red', () => {
    const html = renderToString(<PolicySection data={null} error={new Error('HTTP 502 (Bad Gateway)')} />)
    expect(html).toContain('Could not load policy results: HTTP 502 (Bad Gateway)')
  })
})
