import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { PVCUsageBar } from './PVCUsageBar'

const state = vi.hoisted(() => ({ status: 'ambiguous_scope' }))
vi.mock('../../api/client', () => ({
  isForbiddenError: () => false,
  useAutoPromConnect: () => {},
  useCloudRole: () => ({ canAtLeast: () => false, isLoading: false }),
  usePrometheusStatus: () => ({ data: { connected: true } }),
  usePrometheusPVCUsage: () => ({ data: { status: state.status, hasData: false } }),
}))
vi.mock('../../context/NavCustomization', () => ({ useNavCustomization: () => ({ embedded: true }) }))
vi.mock('../../hooks/usePreviousIntegrationSettings', () => ({ usePreviousIntegrationSettings: () => ({}), previousSettingsNote: () => undefined }))

it('distinguishes mismatched volume-stat labels from multiple cluster identities', () => {
  state.status = 'ambiguous_scope'
  expect(renderToStaticMarkup(<PVCUsageBar namespace="demo" name="disk" />)).toContain('more than one cluster')
  state.status = 'scope_mismatch'
  const html = renderToStaticMarkup(<PVCUsageBar namespace="demo" name="disk" />)
  expect(html).toContain('could not match this volume’s stats to this cluster')
  expect(html).toContain('Used space is unknown')
  expect(html).not.toContain('more than one cluster')
})
