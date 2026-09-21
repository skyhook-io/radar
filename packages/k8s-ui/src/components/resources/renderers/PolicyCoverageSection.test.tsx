import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PolicyCoverageSection } from './PolicyCoverageSection'
import type { PolicyCoverageResponse, PolicyCoverageRule, PolicyCoverageSubject } from '../../../types/policy'

const vpol = (spec: any = {}) => ({ apiVersion: 'policies.kyverno.io/v1', kind: 'ValidatingPolicy', spec })
const mpol = (spec: any = {}) => ({ apiVersion: 'policies.kyverno.io/v1', kind: 'MutatingPolicy', spec })
const legacy = (spec: any) => ({ apiVersion: 'kyverno.io/v1', kind: 'ClusterPolicy', spec })

const counts = (c: Partial<PolicyCoverageResponse['counts']> = {}) =>
  ({ pass: 0, fail: 0, warn: 0, error: 0, skip: 0, ...c })

const subject = (name: string, result: string, namespace = 'app'): PolicyCoverageSubject =>
  ({ kind: 'Pod', namespace, name, result })

const rule = (r: Partial<PolicyCoverageRule> = {}): PolicyCoverageRule =>
  ({ rule: 'check', counts: counts(), subjects: [], total: 0, ...r })

const coverage = (o: Partial<PolicyCoverageResponse> = {}): PolicyCoverageResponse => ({
  evaluated: true,
  status: 'ready',
  liveUpdates: true,
  policy: 'p',
  counts: counts(),
  scopeNamespaces: 1,
  withheldNamespaces: 0,
  examined: 0,
  rules: [],
  ...o,
})

const render = (data: PolicyCoverageResponse, resource: any = vpol(), extra = {}) =>
  renderToString(<PolicyCoverageSection resource={resource} data={data} {...extra} />)

/**
 * The wording is the feature here, so these assert on what the section actually
 * renders. Kyverno reports the same `result: fail` for every family and does not
 * mean the same thing by it, and several of the sentences claim more than the
 * data behind them if they are built from the wrong number.
 */
describe('what the section says a policy did', () => {
  it('states the consequence of enforcing without claiming what exists is blocked', () => {
    const html = render(
      coverage({ examined: 7, subjects: 7, subjectsFailing: 7, counts: counts({ fail: 7 }),
        rules: [rule({ counts: counts({ fail: 7 }), subjects: [subject('a', 'fail')], total: 7 })] }),
      vpol({ validationActions: ['Deny'] }),
    )
    expect(html).toContain('already exist')
    expect(html).toContain('will be rejected')
    expect(html).not.toMatch(/would be blocked|blocks these/)
  })

  it('frames an audit policy as a dry run, not a current block', () => {
    const html = render(
      coverage({ examined: 3, subjects: 3, subjectsFailing: 3, counts: counts({ fail: 3 }),
        rules: [rule({ counts: counts({ fail: 3 }), subjects: [subject('a', 'fail')], total: 3 })] }),
      vpol({ validationActions: ['Audit'] }),
    )
    expect(html).toContain('Nothing is rejected today')
  })

  // A mutating policy's fail carries "mutation is not applied" — nothing
  // violated anything, so calling it a failure misreports compliance.
  it('does not call an unapplied mutation a failure', () => {
    const html = render(
      coverage({ examined: 2, subjects: 2, counts: counts({ fail: 2 }),
        rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] }),
      mpol(),
    )
    expect(html).toContain('not mutated')
    expect(html).not.toContain('Fail')
  })

  // The subject of a generate result is the trigger, never the object produced.
  it('calls a generate rule’s pass a trigger, not a generated object', () => {
    const html = render(
      coverage({ examined: 1, subjects: 1, counts: counts({ pass: 1 }),
        rules: [rule({ rule: 'gen', counts: counts({ pass: 1 }), subjects: [subject('cm', 'pass')], total: 1 })] }),
      legacy({ rules: [{ name: 'gen', generate: {} }] }),
    )
    expect(html).toContain('triggered generation')
    expect(html).not.toContain('Generated')
  })

  // A legacy Enforce policy with `spec.admission: false` is left out of the
  // admission webhook, so it rejects nothing — the consequence sentence must not
  // promise a rejection it cannot make.
  it('does not promise rejection when admission evaluation is disabled', () => {
    const html = render(
      coverage({ examined: 2, subjects: 2, subjectsFailing: 2, counts: counts({ fail: 2 }),
        rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] }),
      legacy({ validationFailureAction: 'Enforce', admission: false,
        rules: [{ name: 'r', validate: { pattern: {} } }] }),
    )
    expect(html).toContain('Nothing is rejected today')
    expect(html).not.toContain('will be rejected')
  })

  // The policy already declares Enforce, so the remediation is to re-enable
  // admission — not to "switch this policy to block", which would point at the
  // wrong knob.
  it('names admission, not the action, as the reason nothing is rejected', () => {
    const html = render(
      coverage({ examined: 2, subjects: 2, subjectsFailing: 2, counts: counts({ fail: 2 }),
        rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] }),
      legacy({ validationFailureAction: 'Enforce', admission: false,
        rules: [{ name: 'r', validate: { pattern: {} } }] }),
    )
    expect(html).toContain('admission is disabled for this policy')
    expect(html).toContain('Enabling admission would reject the next update')
    expect(html).not.toContain('Switching this policy to block')
  })

  // The modern CEL family reaches the same non-blocking state via
  // `spec.evaluation.admission.enabled: false`, and the shared sentence must be
  // honest there too.
  it('names admission for a modern Deny policy with admission disabled', () => {
    const html = render(
      coverage({ examined: 2, subjects: 2, subjectsFailing: 2, counts: counts({ fail: 2 }),
        rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] }),
      vpol({ validationActions: ['Deny'], evaluation: { admission: { enabled: false } } }),
    )
    expect(html).toContain('admission is disabled for this policy')
    expect(html).not.toContain('Switching this policy to block')
  })

  // A JSON-mode policy is evaluated against payloads outside the cluster and
  // never joins admission. Both phrasings would be false: it rejects nothing
  // today, and neither switching the action nor enabling admission changes
  // that. The sentence is withheld rather than guessed at.
  it('states no admission consequence for a JSON-mode policy', () => {
    const failing = coverage({ examined: 2, subjects: 2, subjectsFailing: 2, counts: counts({ fail: 2 }),
      rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] })

    const admissionOff = render(failing, vpol({
      validationActions: ['Deny'],
      evaluation: { mode: 'JSON', admission: { enabled: false } },
    }))
    expect(admissionOff).not.toContain('admission is disabled for this policy')
    expect(admissionOff).not.toContain('Switching this policy to block')

    // Same for JSON mode with admission left at its default.
    const admissionDefault = render(failing, vpol({
      validationActions: ['Deny'],
      evaluation: { mode: 'JSON' },
    }))
    expect(admissionDefault).not.toContain('Switching this policy to block')
    expect(admissionDefault).not.toContain('will be rejected')
  })

  // `getKyvernoValidationActions` applies Kyverno's absent-means-Deny default,
  // which is right for the validating families and wrong for every other kind.
  // A mutating policy with admission off must not be described as a policy that
  // would reject once admission is re-enabled.
  it('does not read a Deny default off a mutating policy with admission disabled', () => {
    const html = render(
      coverage({ examined: 2, subjects: 2, subjectsFailing: 2, counts: counts({ fail: 2 }),
        rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] }),
      mpol({ evaluation: { admission: { enabled: false } } }),
    )
    expect(html).not.toContain('admission is disabled for this policy')
  })

  // An Audit policy genuinely never blocks, so its remediation IS to switch the
  // action — the admission-off wording must not leak onto it.
  it('still offers to switch the action for an audit policy', () => {
    const html = render(
      coverage({ examined: 2, subjects: 2, subjectsFailing: 2, counts: counts({ fail: 2 }),
        rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] }),
      vpol({ validationActions: ['Audit'] }),
    )
    expect(html).toContain('Switching this policy to block')
    expect(html).not.toContain('admission is disabled')
  })

  it('withholds the consequence when the policy’s rules disagree on blocking', () => {
    const html = render(
      coverage({ examined: 2, subjects: 2, subjectsFailing: 2, counts: counts({ fail: 2 }),
        rules: [rule({ counts: counts({ fail: 2 }), subjects: [subject('a', 'fail')], total: 2 })] }),
      legacy({ rules: [
        { name: 'a', validate: { failureAction: 'Enforce' } },
        { name: 'b', validate: { failureAction: 'Audit' } },
      ] }),
    )
    expect(html).not.toContain('will be rejected')
    expect(html).not.toContain('already exist')
  })
})

describe('counts describe the cluster, lists describe the view', () => {
  it('separates checks from resources when one resource was checked twice', () => {
    const html = render(coverage({
      examined: 2, subjects: 1, counts: counts({ pass: 1, fail: 1 }),
      rules: [rule({ counts: counts({ fail: 1 }), subjects: [subject('only-one', 'fail')], total: 1 })],
    }))
    expect(html).toContain('2 checks on 1 resource')
    expect(html).not.toContain('2 resources examined')
  })

  it('says resources when each was checked once', () => {
    const html = render(coverage({
      examined: 34, subjects: 34, counts: counts({ pass: 34 }),
      rules: [rule({ counts: counts({ pass: 34 }), subjects: [subject('a', 'pass')], total: 34 })],
    }))
    expect(html).toContain('34 resources examined')
  })

  it('claims "all" passing only when nothing is outside the view', () => {
    const all = render(coverage({
      examined: 3, subjects: 3, counts: counts({ pass: 3 }),
      rules: [rule({ counts: counts({ pass: 3 }), subjects: [subject('a', 'pass')], total: 3 })],
    }))
    expect(all).toContain('All 3 resources passing')

    const filtered = render(coverage({
      examined: 34, subjects: 34, counts: counts({ pass: 34 }),
      rules: [rule({ counts: counts({ pass: 34 }), subjects: [subject('a', 'pass')], total: 34, hiddenByFilter: 27 })],
    }))
    expect(filtered).toContain('7 resources passing in view')
    expect(filtered).not.toContain('All 34')
    expect(filtered).toContain('27 hidden by your namespace filter')
  })

  // Only non-passing subjects are listed, so a hidden pass leaves nothing to
  // claim — "All 0 resources passing" is not a statement worth making.
  it('makes no passing claim when the filter hid the only subject', () => {
    const html = render(coverage({
      examined: 1, subjects: 1, counts: counts({ pass: 1 }),
      rules: [rule({ counts: counts({ pass: 1 }), subjects: [], total: 1, hiddenByFilter: 1 })],
    }))
    expect(html).not.toContain('All 0')
    expect(html).toContain('1 hidden by your namespace filter')
  })

  // The counts exclude what a family or a namespace held back, so "All" would be
  // quantifying over a set the page itself goes on to say was short.
  it('does not claim all passed when a report family was withheld', () => {
    const html = render(coverage({
      examined: 5, subjects: 5, counts: counts({ pass: 5 }),
      withheldByFamily: 2, unreadableFamilies: ['openreports.io'],
      rules: [rule({ counts: counts({ pass: 5 }), subjects: [], total: 5 })],
    }))
    expect(html).toContain('5 resources passing')
    expect(html).not.toContain('All 5 resources passing')
  })

  it('keeps the plain claim when nothing was held back', () => {
    const html = render(coverage({
      examined: 5, subjects: 5, counts: counts({ pass: 5 }),
      rules: [rule({ counts: counts({ pass: 5 }), subjects: [], total: 5 })],
    }))
    expect(html).toContain('All 5 resources passing')
  })

  it('names the hidden rows that were failing', () => {
    const html = render(coverage({
      examined: 1, subjects: 1, counts: counts({ fail: 1 }),
      rules: [rule({ counts: counts({ fail: 1 }), subjects: [], total: 1, hiddenByFilter: 1, hiddenNotable: 1 })],
    }))
    expect(html).toContain('1 of them failing')
  })

  // Kyverno files engine errors under a rule of their own. Calling those hidden
  // rows by the family's fail word made the footers claim more failures than the
  // summary above them had counted.
  it('does not call a hidden engine error a failure of the rule', () => {
    const html = render(coverage({
      examined: 18, subjects: 18, counts: counts({ error: 18 }),
      rules: [rule({
        rule: 'evaluation', counts: counts({ error: 18 }), subjects: [], total: 18,
        hiddenByFilter: 14, hiddenNotable: 14,
      })],
    }), mpol())
    expect(html).toContain('14 of them could not be evaluated')
    expect(html).not.toContain('14 of them not mutated')
  })

  it('still uses the family word when the hidden rows really did fail', () => {
    const html = render(coverage({
      examined: 4, subjects: 4, counts: counts({ fail: 3, error: 1 }),
      rules: [rule({
        counts: counts({ fail: 3, error: 1 }), subjects: [], total: 4,
        hiddenByFilter: 3, hiddenNotable: 3,
      })],
    }), mpol())
    expect(html).toContain('3 of them not mutated')
  })
})

describe('a bounded list is not a dead end', () => {
  const many = (n: number, result: string) =>
    Array.from({ length: n }, (_, i) => subject(`s${i}`, result))

  it('offers to load the rest when the cap removed listable rows', () => {
    const html = render(
      coverage({ examined: 251, subjects: 251, counts: counts({ fail: 251 }),
        rules: [rule({ counts: counts({ fail: 251 }), subjects: many(200, 'fail'), total: 251, truncated: true })] }),
      vpol(), { onLoadMore: () => {} },
    )
    expect(html).toContain('only the first 200 are available here')
    expect(html).toContain('Load the rest')
    expect(html).toContain('Show 200 of 251')
  })

  // A rule where everything passes is capped just as readily, and its list
  // carries the same offer. Only the label is assertable here: the rows and the
  // control sit behind a disclosure this renderer opens on click, and the repo
  // tests renderers with renderToString, which cannot open one.
  it('summarises a capped passing list as a disclosure rather than a total', () => {
    const html = render(
      coverage({ examined: 251, subjects: 251, counts: counts({ pass: 251 }),
        rules: [rule({ counts: counts({ pass: 251 }), subjects: many(199, 'pass'), total: 251, truncated: true })] }),
      vpol(), { onLoadMore: () => {} },
    )
    expect(html).toContain('All 251 resources passing')
    expect(html).toContain('<button')
  })

  // Rows the filter withheld are not the cap's doing, and no higher limit
  // returns them.
  it('does not blame the cap for what the namespace filter withheld', () => {
    const html = render(coverage({
      examined: 1, subjects: 1, counts: counts({ fail: 1 }),
      rules: [rule({ counts: counts({ fail: 1 }), subjects: [], total: 1, hiddenByFilter: 1, hiddenNotable: 1 })],
    }), vpol(), { onLoadMore: () => {} })
    expect(html).not.toContain('Load the rest')
    expect(html).not.toContain('only the first')
  })

  it('does not announce a cap that only removed passing rows', () => {
    const html = render(
      coverage({ examined: 251, subjects: 251, counts: counts({ pass: 250, fail: 1 }),
        rules: [rule({ counts: counts({ pass: 250, fail: 1 }), subjects: [subject('bad', 'fail')], total: 251, truncated: true })] }),
      vpol(), { onLoadMore: () => {} },
    )
    expect(html).not.toContain('only the first 1 are available')
  })
})

describe('an engine error is not a verdict', () => {
  const allErrors = coverage({
    examined: 1, subjects: 1, subjectsFailing: 0, counts: counts({ error: 1 }),
    rules: [rule({ rule: 'evaluation', counts: counts({ error: 1 }), subjects: [subject('a', 'error')], total: 1 })],
  })

  it('does not promise rejection for rules that never ran', () => {
    const html = render(allErrors, vpol({ validationActions: ['Deny'] }))
    expect(html).not.toContain('will be rejected')
    expect(html).toContain('could not be evaluated')
  })

  // The policy has to declare Ignore to reach this wording at all: an absent
  // failurePolicy means Fail per the CRD, so the default is the fail-closed
  // sentence, not this one.
  it('agrees in number', () => {
    const ignores = vpol({ failurePolicy: 'Ignore' })
    expect(render(allErrors, ignores)).toContain('1 could not be evaluated and is being allowed through unchecked')
    const two = coverage({
      examined: 2, subjects: 2, counts: counts({ error: 2 }),
      rules: [rule({ rule: 'evaluation', counts: counts({ error: 2 }), subjects: [subject('a', 'error')], total: 2 })],
    })
    expect(render(two, ignores)).toContain('2 could not be evaluated and are being allowed through unchecked')
  })

  it('says a fail-closed policy rejects on the error instead', () => {
    const html = render(allErrors, vpol({ failurePolicy: 'Fail', validationActions: ['Deny'] }))
    expect(html).toContain('fails closed')
    expect(html).toContain('that admission is')
  })

  // `evaluation` is Kyverno's own marker, not a rule the policy declares.
  it('does not present the engine’s marker as a rule the policy contains', () => {
    const html = render(allErrors)
    expect(html).toContain('Engine errors')
    expect(html).not.toMatch(/>evaluation</)
  })
})

describe('absence is only claimed when the whole answer arrived', () => {
  it('says nothing has been checked when nothing was withheld', () => {
    expect(render(coverage())).toContain('Nothing has been checked against this policy yet')
  })

  it('does not claim that when results were withheld', () => {
    const html = render(coverage({ withheldNamespaces: 2 }))
    expect(html).not.toContain('Nothing has been checked')
    expect(html).toContain('No results to show here')
    expect(html).toContain('2 namespaces are not shown')
  })

  it('attributes a family the caller cannot read to their permissions', () => {
    const html = render(coverage({ withheldByFamily: 3, unreadableFamilies: ['openreports.io'] }))
    expect(html).toContain('openreports.io')
    expect(html).toContain('3 results were')
  })

  it('names another engine rather than presenting its findings as this policy’s', () => {
    const html = render(coverage({
      examined: 1, subjects: 1, counts: counts({ fail: 1 }), engines: ['kyverno', 'trivy'],
      rules: [rule({ counts: counts({ fail: 1 }), subjects: [subject('a', 'fail')], total: 1 })],
    }))
    expect(html).toContain('trivy')
  })
})

/**
 * An engine error is either rejected or waved through, and which one is the
 * whole point of the sentence. `spec.failurePolicy` defaults to Fail per the
 * published CRD and most policies never set it, so reading absent as anything
 * else states the opposite of what the cluster does.
 */
describe('what an engine error costs', () => {
  const errored = coverage({
    examined: 2, subjects: 2, counts: counts({ error: 2 }),
    rules: [rule({ counts: counts({ error: 2 }), subjects: [], total: 2 })],
  })

  it('treats an unset failurePolicy as fail-closed, like the CRD does', () => {
    const html = render(errored, vpol({}))
    expect(html).toContain('rejected for an engine error')
    expect(html).not.toContain('allowed through unchecked')
  })

  it('says so plainly when the policy declares Ignore', () => {
    const html = render(errored, vpol({ failurePolicy: 'Ignore' }))
    expect(html).toContain('allowed through unchecked')
  })

  // The legacy family keeps the field somewhere else entirely.
  it('honours a legacy policy that opted out via webhookConfiguration', () => {
    const html = render(errored, legacy({ webhookConfiguration: { failurePolicy: 'Ignore' }, rules: [] }))
    expect(html).toContain('allowed through unchecked')
    expect(html).not.toContain('fails closed')
  })

  it('agrees with an explicit Fail', () => {
    expect(render(errored, vpol({ failurePolicy: 'Fail' }))).toContain('rejected for an engine error')
  })
})
