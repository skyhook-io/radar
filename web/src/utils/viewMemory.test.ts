import { describe, expect, it } from 'vitest'
import { createViewMemory } from './viewMemory'

describe('createViewMemory', () => {
  it('remembers the last resource kind list for the Resources section', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?filters=namespace:a&namespaces=x')
    memory.record('resources', '/resources/services', '?search=web')

    expect(memory.sectionPath('resources')).toBe('/resources/services?search=web')
  })

  it('never remembers the global namespace pick, the investigation focus or an open drawer', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?namespaces=a,b&ai-run=r1&resource=a/web&tab=yaml&full=1&problems=failed')

    expect(memory.sectionPath('resources')).toBe('/resources/pods?problems=failed')
  })

  it('returns to the list, not to a detail view opened on top of it', () => {
    const memory = createViewMemory()
    memory.record('helm', '/helm', '?release=default/web&releaseStorage=secret&q=web')
    memory.record('applications', '/applications', '?workload=apps/Deployment/a/web&run=r1&sort=name')
    memory.record('cnpg', '/cnpg', '?drawer=cluster:a/db&cluster=db')
    memory.record('timeline', '/timeline', '?event=e1&filter=warnings')

    expect(memory.sectionPath('helm')).toBe('/helm?q=web')
    expect(memory.sectionPath('applications')).toBe('/applications?sort=name')
    expect(memory.sectionPath('cnpg')).toBe('/cnpg?cluster=db')
    expect(memory.sectionPath('timeline')).toBe('/timeline?filter=warnings')
  })

  it('remembers section list routes but not their detail pages', () => {
    const memory = createViewMemory()
    memory.record('gitops', '/gitops', '?health=degraded')
    memory.record('gitops', '/gitops/argo/argocd/app', '?tab=tree')
    memory.record('home', '/', '')

    expect(memory.sectionPath('gitops')).toBe('/gitops?health=degraded')
    expect(memory.sectionPath('home')).toBe('/')
  })

  it('records a cleared list as no filters, so Clear filters also clears the memory', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?filters=namespace:a')
    memory.record('resources', '/resources/pods', '?namespaces=x')

    expect(memory.sectionPath('resources')).toBe('/resources/pods')
  })

  it('forgets everything on clear', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?filters=namespace:a')
    memory.clear()

    expect(memory.sectionPath('resources')).toBeUndefined()
  })
})
