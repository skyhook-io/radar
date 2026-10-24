// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ImageFilesystemModal } from './ImageFilesystemModal'
import { ApiError } from '../../api/client'

const state = vi.hoisted(() => ({ authEnabled: false, showMetadata: false }))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useIsAuthEnabled: () => state.authEnabled }))
vi.mock('../../api/client', () => ({
  ApiError: class extends Error {
    status = 401
    data = { registryType: 'google' }
  },
  useImageMetadata: () => state.showMetadata ? ({
    data: { image: 'gcr.io/project/app:v1', digest: 'sha256:abc', platform: 'linux/amd64', totalSize: 1024, layerCount: 1, authMethod: 'google', cached: false },
    refetch: vi.fn(),
  }) : ({ error: new ApiError('Authentication required', 401), refetch: vi.fn() }),
}))

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
let root: ReturnType<typeof createRoot>

afterEach(async () => {
  await act(async () => root.unmount())
  state.showMetadata = false
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
    expect(text).toContain("Private images need exec access to the Pod, or read access to its imagePullSecrets. Exec access also lets Radar use its own registry credentials.")
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

it.each([true, false])('shows download confirmation for authEnabled=%s', async (authEnabled) => {
  state.authEnabled = authEnabled
  state.showMetadata = true
  root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(<ImageFilesystemModal open onClose={() => {}} image="gcr.io/project/app:v1" namespace="team" podName="app" pullSecrets={[]} />)
  })
  const text = document.body.textContent || ''
  expect(text).toContain('Download Image Layers?')
  expect(text).toContain('Download & View')
  if (authEnabled) {
    expect(text).not.toContain('Authentication Command')
    expect(text).not.toContain('gcloud')
    expect(text).not.toContain('Run this command')
  } else {
    expect(text).toContain('Authentication Command')
    expect(text).toContain('gcloud')
  }
})
