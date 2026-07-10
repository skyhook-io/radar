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

const horizontalPodAutoscaler: APIResource = {
  group: 'autoscaling',
  version: 'v2',
  kind: 'HorizontalPodAutoscaler',
  name: 'horizontalpodautoscalers',
  namespaced: true,
  isCrd: false,
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

describe('ResourcesSidebar count visibility', () => {
  it('keeps resources visible when their count is unknown', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={() => {}}
        apiResources={[horizontalPodAutoscaler]}
        resourceCounts={{}}
      />
    )

    expect(html).toContain('HorizontalPodAutoscaler')
    expect(html).toContain('–')
    expect(html).toContain('aria-label="Count unavailable. Open to view resources."')
  })

  it('hides confirmed-empty resources by default', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={() => {}}
        apiResources={[horizontalPodAutoscaler]}
        resourceCounts={{ 'autoscaling/HorizontalPodAutoscaler': 0 }}
      />
    )

    expect(html).not.toContain('HorizontalPodAutoscaler')
    expect(html).toContain('Show')
  })

  it('keeps the selected confirmed-empty resource visible', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={{ name: 'horizontalpodautoscalers', kind: 'HorizontalPodAutoscaler', group: 'autoscaling' }}
        onSelectedKindChange={() => {}}
        apiResources={[horizontalPodAutoscaler]}
        resourceCounts={{ 'autoscaling/HorizontalPodAutoscaler': 0 }}
      />
    )

    expect(html).toContain('HorizontalPodAutoscaler')
    expect(html).toContain('>0</')
  })

  it('keeps pinned confirmed-empty resources visible in Favorites', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={() => {}}
        apiResources={[horizontalPodAutoscaler]}
        resourceCounts={{ 'autoscaling/HorizontalPodAutoscaler': 0 }}
        pinned={[{ name: 'horizontalpodautoscalers', kind: 'HorizontalPodAutoscaler', group: 'autoscaling' }]}
        isPinned={(name, group) => name === 'horizontalpodautoscalers' && group === 'autoscaling'}
      />
    )

    expect(html).toContain('Favorites')
    expect(html).toContain('HorizontalPodAutoscaler')
  })
})
