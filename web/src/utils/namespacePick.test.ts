// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { NamespaceScope } from '../api/client'
import { applyTabPick, readTabPick, writeTabPick } from './namespacePick'

function scope(over: Partial<NamespaceScope> = {}): NamespaceScope {
  return {
    actives: [],
    context: 'ctx-a',
    kubeconfigNamespace: '',
    mode: 'cluster-wide',
    accessibleNamespaces: ['team-a', 'team-b', 'team-c'],
    deniedNamespaces: [],
    authoritative: true,
    canClearNamespace: true,
    cacheScoped: false,
    namespaceRescope: false,
    ...over,
  }
}

beforeEach(() => sessionStorage.clear())

describe('applyTabPick', () => {
  it('seeds a tab with no pick from the server and keeps it after the server pick moves', () => {
    expect(applyTabPick(scope({ actives: ['team-a'], mode: 'namespace' }), '/api').actives).toEqual(['team-a'])
    const later = applyTabPick(scope({ actives: ['team-b'], mode: 'namespace' }), '/api')
    expect(later.actives).toEqual(['team-a'])
    expect(later.mode).toBe('namespace')
  })

  it('keeps an explicit All over a narrowed server pick', () => {
    writeTabPick('/api', 'ctx-a', [])
    const seen = applyTabPick(scope({ actives: ['team-b'], mode: 'namespace' }), '/api')
    expect(seen.actives).toEqual([])
    expect(seen.mode).toBe('cluster-wide')
  })

  it('reports restricted when an unpicked tab has only a partial list', () => {
    writeTabPick('/api', 'ctx-a', [])
    expect(applyTabPick(scope({ authoritative: false, actives: ['team-a'] }), '/api').mode).toBe('restricted')
  })

  it('keeps one pick per Radar address', () => {
    writeTabPick('/api', 'ctx-a', ['team-a'])
    writeTabPick('/clusters/2/api', 'ctx-a', ['team-c'])
    expect(applyTabPick(scope({ actives: ['team-b'] }), '/api').actives).toEqual(['team-a'])
    expect(applyTabPick(scope({ actives: ['team-b'] }), '/clusters/2/api').actives).toEqual(['team-c'])
  })

  it('starts again from the server pick after a context switch, also when switching back', () => {
    writeTabPick('/api', 'ctx-a', ['team-a'])
    expect(applyTabPick(scope({ context: 'ctx-b', actives: ['team-b'] }), '/api').actives).toEqual(['team-b'])
    expect(applyTabPick(scope({ actives: ['team-c'] }), '/api').actives).toEqual(['team-c'])
  })

  it('drops namespaces a complete list or a completed access check no longer has, and keeps them after a failed lookup', () => {
    writeTabPick('/api', 'ctx-a', ['team-a', 'gone'])
    expect(applyTabPick(scope({ authoritative: false }), '/api').actives).toEqual(['team-a', 'gone'])
    expect(applyTabPick(scope({ accessibleNamespaces: [] }), '/api').actives).toEqual(['team-a', 'gone'])
    expect(applyTabPick(scope({ authoritative: false, accessChecked: true, accessibleNamespaces: ['team-b'] }), '/api').actives).toEqual([])
    writeTabPick('/api', 'ctx-a', ['team-a', 'gone'])
    expect(applyTabPick(scope(), '/api').actives).toEqual(['team-a'])
    expect(readTabPick('/api', 'ctx-a')).toEqual(['team-a'])
  })

  it('leaves the shared server pick alone under --namespace-scope and on servers without a context', () => {
    writeTabPick('/api', 'ctx-a', ['team-a'])
    expect(applyTabPick(scope({ cacheScoped: true, actives: ['team-b'] }), '/api').actives).toEqual(['team-b'])
    expect(applyTabPick(scope({ context: undefined, actives: ['team-b'] }), '/api').actives).toEqual(['team-b'])
  })
})

describe('writeTabPick', () => {
  it('keeps an accepted pick for the page when sessionStorage is full', () => {
    writeTabPick('/api', 'ctx-a', ['team-a'])
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('full', 'QuotaExceededError')
    })
    writeTabPick('/api', 'ctx-a', ['team-b'])
    expect(readTabPick('/api', 'ctx-a')).toEqual(['team-b'])
    setItem.mockRestore()
    writeTabPick('/api', 'ctx-a', ['team-c'])
    expect(sessionStorage.getItem('radar:namespace-pick:/api')).toContain('team-c')
    expect(readTabPick('/api', 'ctx-a')).toEqual(['team-c'])
  })
})
