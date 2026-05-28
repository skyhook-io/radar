import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { RoleBindingRenderer } from './RoleBindingRenderer'

function shaped(message: string, status: number) {
  return Object.assign(new Error(message), { status })
}

const binding = {
  metadata: { name: 'rb-1', namespace: 'dev' },
  roleRef: { kind: 'ClusterRole', name: 'view', apiGroup: 'rbac.authorization.k8s.io' },
  subjects: [
    { kind: 'ServiceAccount', name: 'alice', namespace: 'dev' },
  ],
}

describe('RoleBindingRenderer rules section', () => {
  it('renders rule rows when rules are present', () => {
    const html = renderToString(
      <RoleBindingRenderer
        data={binding}
        roleRules={[{ verbs: ['get', 'list'], resources: ['pods'], apiGroups: [''] }]}
      />,
    )
    expect(html).toContain('Rules Granted')
    expect(html).toContain('pods')
  })

  it('renders "no rules" when rules array is empty (loaded but role has none)', () => {
    const html = renderToString(<RoleBindingRenderer data={binding} roleRules={[]} />)
    expect(html).toContain('no rules')
  })

  it('renders the orphan-or-unavailable fallback when rules is null and no error', () => {
    const html = renderToString(<RoleBindingRenderer data={binding} roleRules={null} />)
    expect(html).toContain('Could not resolve referenced role')
    expect(html).toContain('orphan binding')
  })

  it('renders the Access denied message when rules is null and the fetch was 403', () => {
    const html = renderToString(
      <RoleBindingRenderer
        data={binding}
        roleRules={null}
        roleRulesError={shaped('forbidden', 403)}
      />,
    )
    expect(html).toContain('Access denied reading referenced role')
    expect(html).toContain('view')
    expect(html).not.toContain('orphan binding')
  })

  it('falls back to the orphan-or-unavailable message for non-403 errors (404, 500, network)', () => {
    const html = renderToString(
      <RoleBindingRenderer
        data={binding}
        roleRules={null}
        roleRulesError={shaped('not found', 404)}
      />,
    )
    expect(html).toContain('Could not resolve referenced role')
    expect(html).not.toContain('Access denied')
  })

  it('keeps showing cached rules when a refetch fails with 403 (stale data preserved)', () => {
    const html = renderToString(
      <RoleBindingRenderer
        data={binding}
        roleRules={[{ verbs: ['get'], resources: ['secrets'], apiGroups: [''] }]}
        roleRulesError={shaped('forbidden', 403)}
      />,
    )
    expect(html).toContain('secrets')
    expect(html).not.toContain('Access denied')
  })
})
