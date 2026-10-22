// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ImageFilesystemModal } from './ImageFilesystemModal'
import { ApiError } from '../../api/client'

const state = vi.hoisted(() => ({ authEnabled: false }))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useIsAuthEnabled: () => state.authEnabled }))
vi.mock('../../api/client', () => ({
  ApiError: class extends Error {
    status = 401
    data = { registryType: 'google' }
  },
  useImageMetadata: () => ({ error: new ApiError('Authentication required', 401), refetch: vi.fn() }),
}))

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
let root: ReturnType<typeof createRoot>

afterEach(async () => {
  await act(async () => root.unmount())
})

it.each([true, false])('shows private-image guidance for authEnabled=%s', async (authEnabled) => {
  state.authEnabled = authEnabled
  root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(<ImageFilesystemModal open onClose={() => {}} image="gcr.io/project/app:v1" namespace="team" podName="app" pullSecrets={[]} />)
  })
  const text = document.body.textContent || ''
  expect(text).toContain('Authentication Required')
  if (authEnabled) {
    expect(text).toContain("Private images are read with the Pod's imagePullSecrets, and only if you can read those Secrets or exec into the Pod.")
    expect(text).not.toContain('~/.docker/config.json')
    expect(text).not.toContain('Run this command to authenticate')
    expect(text).not.toContain('gcloud')
  } else {
    expect(text).toContain('~/.docker/config.json')
    expect(text).toContain('Run this command to authenticate')
    expect(text).toContain('gcloud')
    expect(text).not.toContain("Private images are read with the Pod's imagePullSecrets")
  }
})
