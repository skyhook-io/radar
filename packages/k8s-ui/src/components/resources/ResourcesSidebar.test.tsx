import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { APIResource } from '../../types'
import { rawCRDGroupTitle, resourceMatchesSidebarFilter, ResourcesSidebar } from './ResourcesSidebar'

const sqlInstance: APIResource = {
  group: 'sql.cnrm.cloud.google.com',
  version: 'v1beta1',
  kind: 'SQLInstance',
  name: 'sqlinstances',
  namespaced: true,
  isCrd: true,
  verbs: ['list'],
}

describe('ResourcesSidebar CRD group labels', () => {
  it('keeps raw API groups available for filtering and hover recovery', () => {
    expect(resourceMatchesSidebarFilter(sqlInstance, 'cnrm')).toBe(true)
    expect(resourceMatchesSidebarFilter(sqlInstance, 'google')).toBe(true)
    expect(rawCRDGroupTitle([sqlInstance])).toBe('API group: sql.cnrm.cloud.google.com')
  })

  it('renders the raw API group in the category title while keeping the friendly label visible', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={() => {}}
        apiResources={[sqlInstance]}
        resourceCounts={{ 'sql.cnrm.cloud.google.com/SQLInstance': 1 }}
      />
    )

    expect(html).toContain('Config Connector')
    expect(html).toContain('title="API group: sql.cnrm.cloud.google.com"')
  })
})
