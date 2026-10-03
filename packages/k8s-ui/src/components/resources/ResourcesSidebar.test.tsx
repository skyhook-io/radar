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

describe('ResourcesSidebar category workspaces', () => {
  const cnpgCluster: APIResource = {
    group: 'postgresql.cnpg.io',
    version: 'v1',
    kind: 'Cluster',
    name: 'clusters',
    namespaced: true,
    isCrd: true,
    verbs: ['list'],
  }
  const objectStore: APIResource = {
    group: 'barmancloud.cnpg.io',
    version: 'v1',
    kind: 'ObjectStore',
    name: 'objectstores',
    namespaced: true,
    isCrd: true,
    verbs: ['list'],
  }

  it('renders destinations above the kinds, with the active object nested and no kind selected', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={() => {}}
        apiResources={[cnpgCluster, objectStore]}
        resourceCounts={{ 'postgresql.cnpg.io/Cluster': 2, 'barmancloud.cnpg.io/ObjectStore': 1 }}
        categoryWorkspaces={{
          CloudNativePG: {
            destinations: [
              { id: 'overview', label: 'Overview', count: 3, countTitle: '3 clusters need attention', active: true, child: { label: 'pg-orders' }, onSelect: () => {} },
            ],
            defaultKindsCollapsed: true,
            scopeNote: 'Counts for namespace payments',
          },
        }}
      />
    )
    expect(html).toContain('Workspace')
    expect(html).toContain('Overview')
    expect(html).toContain('pg-orders')
    expect(html).toContain('Counts for namespace payments')
    expect(html).toContain('Resource kinds')
    expect(html).toMatch(/aria-expanded="false"[^>]*>(?:(?!<\/button>).)*Resource kinds/)
    expect(html).not.toContain('selection-strong selection-text">Pod')
  })

  it('marks a count taken over partly readable data as a lower bound, and never shows its zero as none', () => {
    const render = (count: number) =>
      renderToString(
        <ResourcesSidebar
          selectedKind={null}
          onSelectedKindChange={() => {}}
          apiResources={[cnpgCluster]}
          resourceCounts={{ 'postgresql.cnpg.io/Cluster': 1 }}
          categoryWorkspaces={{ CloudNativePG: { destinations: [{ id: 'overview', label: 'Overview', count, countLowerBound: true, onSelect: () => {} }] } }}
        />,
      )
    expect(render(2)).toMatch(/≥(<!-- -->)?2/)
    expect(render(0)).toContain('–')
  })

  it('keeps a workspace category visible when it has no resources', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={null}
        onSelectedKindChange={() => {}}
        apiResources={[cnpgCluster]}
        resourceCounts={{ 'postgresql.cnpg.io/Cluster': 0 }}
        categoryWorkspaces={{ CloudNativePG: { destinations: [{ id: 'overview', label: 'Overview', onSelect: () => {} }] } }}
      />
    )
    expect(html).toContain('CloudNativePG')
    expect(html).toContain('Overview')
  })

  it('labels API groups when a workspace category spans several', () => {
    const html = renderToString(
      <ResourcesSidebar
        selectedKind={{ name: 'clusters', kind: 'Cluster', group: 'postgresql.cnpg.io' }}
        onSelectedKindChange={() => {}}
        apiResources={[cnpgCluster, objectStore]}
        resourceCounts={{ 'postgresql.cnpg.io/Cluster': 2, 'barmancloud.cnpg.io/ObjectStore': 1 }}
        categoryWorkspaces={{ CloudNativePG: { destinations: [{ id: 'overview', label: 'Overview', onSelect: () => {} }], defaultKindsCollapsed: true } }}
      />
    )
    expect(html).toContain('postgresql.cnpg.io')
    expect(html).toContain('barmancloud.cnpg.io')
  })
})
