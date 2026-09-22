import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { KueueAdmissionResponse } from '@skyhook-io/k8s-ui/types/scheduling'
import { JobSetAdmission } from './JobSetAdmission'

const state = vi.hoisted(() => ({ data: undefined as KueueAdmissionResponse | undefined, query: vi.fn() }))
vi.mock('../../api/client', () => ({
  ApiError: class extends Error {},
  useKueueAdmission: (...args: unknown[]) => {
    state.query(...args)
    return { data: state.data, isLoading: false, refetch: vi.fn() }
  },
}))

beforeEach(() => {
  state.query.mockClear()
  state.data = { uid: 'old-uid', installed: true, total: 1, truncated: false, workloads: [{
    apiVersion: 'kueue.x-k8s.io/v1beta2', namespace: 'ml', name: 'old-workload', uid: 'workload-uid', generation: 1,
    createdAt: null, deleting: false, projection: 'unsupported', linksLimited: false,
  }] }
})

function render(uid: string) {
  return renderToStaticMarkup(<JobSetAdmission resource={{ metadata: { uid } }} namespace="ml" name="training" />)
}

describe('JobSet admission identity', () => {
  it('rejects old evidence after recreation under the same name', () => {
    expect(render('old-uid')).toContain('old-workload')
    const html = render('new-uid')
    expect(state.query).toHaveBeenLastCalledWith('ml', 'training', 'new-uid')
    expect(html).not.toContain('old-workload')
    expect(html).toContain('different JobSet instance')
  })

  it('accepts absence only for the displayed root instance', () => {
    state.data = { uid: 'old-uid', installed: false, total: 0, truncated: false, workloads: [] }
    expect(render('new-uid')).toContain('different JobSet instance')
    state.data.uid = 'new-uid'
    expect(render('new-uid')).toBe('')
  })
})
