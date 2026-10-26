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
  it.each(['agree', 'listed'] as const)('%s is Managed by even before the status fetch', state => {
    const html = render({ state, lead: a, direct: { object, recorders: [a] } })
    expect(html).toContain('Managed by')
    expect(html).toContain('its status lists this resource')
  })
  it.each([
    ['omitted', "its status doesn't list this resource"],
    ['unreadable', "Radar can't read its status"],
    ['remote', 'it deploys to another cluster'],
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
    expect(html).toContain('Application argocd/b: its status lists this resource')
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
    expect(html).toContain('Managed by')
    expect(html).toContain('Through Deployment demo/web')
    expect(html).toContain('its own label names Application argocd/a')
    expect(html).toContain('its own label names Application argocd/middle (copied from another resource)')
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
