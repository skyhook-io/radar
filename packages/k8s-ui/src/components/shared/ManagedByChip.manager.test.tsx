import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { ManagerAnswer, ManagerClaim } from '../../types/core'
import { ManagedByChip } from './ManagedByChip'

vi.mock('../ui/Tooltip', () => ({ Tooltip: ({ children, content }: any) => <span>{children}<span>{content}</span></span> }))
const a = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'a' }
const b = { ...a, name: 'b' }
const object = { kind: 'Pod', namespace: 'demo', name: 'web-1' }
const claim: ManagerClaim = { manager: a, source: 'argo-instance-label', check: 'omitted' }
const render = (manager: ManagerAnswer) => renderToStaticMarkup(<ManagedByChip manager={manager} />).replaceAll('&#x27;', "'")

describe('manager answer chip', () => {
  // Only a list and the metadata agreeing say a manager manages it; a list
  // alone can name an object its manager doesn't sync.
  it.each([['agree', 'Managed by'], ['listed', 'Listed by']] as const)('%s says %s even before the status fetch', (state, prefix) => {
    const html = render({ state, lead: a, direct: { object, recorders: [a] } })
    expect(html).toContain(prefix)
    expect(html).toContain('Application argocd/a lists this resource in its status.')
  })
  it('agree says the tracking metadata names the listing manager, once', () => {
    const html = render({ state: 'agree', lead: a, direct: { object, recorders: [a], claims: [{ ...claim, manager: { ...a, namespace: '' }, check: 'recorded' }] } })
    expect(html).toContain("Application argocd/a lists this resource in its status, and this resource's tracking metadata names it.")
    expect(html.match(/Application/g)?.length).toBe(1)
  })
  it.each([
    ['omitted', "its status doesn't list this resource"],
    ['unreadable', "Radar can't read its status"],
    ['remote', "Radar can't confirm it deploys to this cluster"],
    ['missing', 'no such Application here'],
    ['withheld', "you can't view it"],
    ['unresolved', "the label doesn't name its namespace"],
    ['unchecked', "Radar couldn't check its status"],
  ] as const)('claimed explains %s', (check, reason) => {
    const html = render({ state: 'claimed', lead: a, direct: { object, claims: [{ ...claim, check }] } })
    expect(html).toContain('Tracked by')
    expect(html).toContain(reason)
  })
  it('disagree names the claim and recorder with a warning', () => {
    const html = render({ state: 'disagree', direct: { object, claims: [claim], recorders: [b] } })
    expect(html).toContain('Tracked by')
    expect(html).toContain("Application argocd/a: its status doesn't list this resource")
    expect(html).toContain('Application argocd/b lists this resource in its status')
    expect(html.indexOf('argocd/a: its status')).toBeLessThan(html.indexOf('argocd/b lists'))
    expect(html).toMatch(/rounded-full[^\"]*bg-amber/)
  })
  it('shared lists multiple names with the lead first', () => {
    const html = render({ state: 'shared', lead: b, direct: { object, recorders: [a, b] } })
    expect(html).toContain('Listed by')
    expect(html).toContain('argocd/b and argocd/a')
  })
  it('does not present the lead’s sync status as the status of several managers', () => {
    const html = renderToStaticMarkup(<ManagedByChip manager={{ state: 'shared', lead: b, direct: { object, recorders: [a, b] } }} status={{ sync: 'Synced', health: 'Healthy', suspended: false }} />)
    expect(html).toContain('Listed by')
    expect(html).not.toContain('Synced')
  })
  it('several claims without a lead stay plural', () => {
    const html = render({ state: 'claimed', direct: { object, claims: [claim, { ...claim, manager: b }] } })
    expect(html).toContain('Tracked by')
    expect(html).toContain('argocd/a and argocd/b')
  })
  it.each(['none', 'unknown'] as const)('%s has no chip', state => {
    expect(render({ state, direct: { object } })).toBe('')
  })
  it('via composes the wording with the parent and keeps direct and between claims', () => {
    const html = render({ state: 'listed', lead: b, direct: { object, claims: [claim] },
      between: [{ object: { ...object, kind: 'ReplicaSet', name: 'web' }, claims: [{ ...claim, manager: { ...a, name: 'middle' }, copied: true }] }],
      via: { object: { kind: 'Deployment', group: 'apps', namespace: 'demo', name: 'web' }, recorders: [b] } })
    expect(html).toContain('Listed by')
    expect(html).toContain('Through Deployment demo/web')
    expect(html).toContain('its own label names Application argocd/a')
    expect(html).not.toContain('argocd/middle')
  })
})


it('Listed by names only recorders and explains an outside claim in the tooltip', () => {
  const c = { ...a, name: 'outside' }
  const manager: ManagerAnswer = { state: 'shared', lead: b, direct: { object, recorders: [a, b], claims: [{ ...claim, manager: c }] } }
  const html = render(manager)
  expect(html).toContain('argocd/b and argocd/a')
  expect(html).not.toContain('argocd/b and argocd/a and argocd/outside')
  expect(html).toContain("Application argocd/outside: its status doesn't list this resource")
})

it('says on the chip itself that the answer comes through an ancestor', () => {
  const object = { kind: 'Pod', namespace: 'demo', name: 'web-1' }
  const lead = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'a' }
  const html = renderToStaticMarkup(<ManagedByChip manager={{ state: 'listed', lead, direct: { object }, via: { object: { kind: 'Deployment', group: 'apps', namespace: 'demo', name: 'web' }, recorders: [lead] } }} />)
  expect(html).toMatch(/<span class="shrink-0 text-theme-text-tertiary">via Deployment<\/span>/)
})

describe('manager answer chip wording', () => {
  it("says up front that a claim isn't confirmed", () => {
    expect(render({ state: 'claimed', lead: a, direct: { object, claims: [claim] } })).toContain("labels or annotations name Application argocd/a; Radar hasn't confirmed it manages it")
  })
  // A claim the walk passed shows even when no ancestor answered.
  it('names claims on the way without via', () => {
    const html = render({ state: 'claimed', lead: a, direct: { object, claims: [claim] },
      between: [{ object: { ...object, kind: 'ReplicaSet', name: 'web' }, claims: [{ ...claim, manager: { ...a, name: 'rs-app' } }] }] })
    expect(html).toContain('ReplicaSet demo/web: its own label names Application argocd/rs-app')
  })
  // The manager's Synced reads as this resource's state beside a list alone.
  it.each([['agree', true], ['listed', false]] as const)('%s shows the manager status: %s', (state, shown) => {
    const html = renderToStaticMarkup(<ManagedByChip manager={{ state, lead: a, direct: { object, recorders: [a] } }} status={{ sync: 'Synced', health: 'Healthy' } as never} />)
    expect(html.includes('Synced')).toBe(shown)
  })
})
