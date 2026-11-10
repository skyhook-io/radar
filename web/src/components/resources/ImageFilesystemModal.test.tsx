// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ImageFilesystemModal } from './ImageFilesystemModal'
import { ApiError } from '../../api/client'

const state = vi.hoisted(() => ({
  authEnabled: false,
  showMetadata: false,
  metadataImage: 'gcr.io/project/app:v1',
  errorStatus: 401,
  errorData: { registryType: 'google' } as Record<string, unknown>,
  refetch: (() => {}) as () => void,
}))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useIsAuthEnabled: () => state.authEnabled }))
vi.mock('../../api/client', () => ({
  ApiError: class extends Error {
    status = state.errorStatus
    data = state.errorData
  },
  useImageMetadata: () => state.showMetadata ? ({
    data: { image: state.metadataImage, digest: 'sha256:abc', platform: 'linux/amd64', totalSize: 1024, layerCount: 1, authMethod: 'google', cached: false },
    refetch: state.refetch,
  }) : ({ error: new ApiError('Authentication required', 401), refetch: state.refetch }),
}))

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
let root: ReturnType<typeof createRoot>

afterEach(async () => {
  await act(async () => root.unmount())
  state.showMetadata = false
  state.metadataImage = 'gcr.io/project/app:v1'
  state.errorStatus = 401
  state.errorData = { registryType: 'google' }
  state.refetch = () => {}
  vi.unstubAllGlobals()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

async function renderModal(props: { onSwitchToPodFiles?: () => void } = {}) {
  root = createRoot(document.createElement('div'))
  await act(async () => {
    root.render(<ImageFilesystemModal open onClose={() => {}} image="gcr.io/project/app:v1" namespace="team" podName="app" pullSecrets={[]} {...props} />)
  })
  return document.body.textContent || ''
}

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

it.each(['node_only', 'registry_unreachable'])('explains a %s image instead of a generic or sign-in error', async (reason) => {
  state.errorStatus = reason === 'node_only' ? 404 : 502
  state.errorData = { error: 'x', reason, registry: 'registry.example' }
  const refetch = vi.fn()
  state.refetch = refetch
  const text = await renderModal({ onSwitchToPodFiles: () => {} })
  expect(text).toContain('registry.example')
  expect(text).toContain('Browse live files from running pod below')
  expect(text).not.toContain('Failed to inspect image')
  expect(text).not.toContain('Authentication Required')
  const retry = [...document.body.querySelectorAll('button')].find((b) => b.textContent?.includes('Retry'))
  if (reason === 'node_only') {
    expect(retry).toBeUndefined()
  } else {
    await act(async () => retry?.click())
    expect(refetch).toHaveBeenCalledTimes(1)
  }
})

it('keeps the generic error box for an error without a known reason', async () => {
  state.errorStatus = 500
  state.errorData = { error: 'Failed to inspect image' }
  const text = await renderModal()
  expect(text).toContain('Failed to inspect image')
})

it('shows the pulled digest and downloads that exact image, not the tag', async () => {
  const digest = 'sha256:' + 'a'.repeat(64)
  state.showMetadata = true
  state.metadataImage = 'gcr.io/project/app@' + digest
  const fetchMock = vi.fn(async () => new Response(JSON.stringify({ image: state.metadataImage, root: { name: '/', path: '/', type: 'dir' }, totalFiles: 0, totalSize: 0 })))
  vi.stubGlobal('fetch', fetchMock)
  const text = await renderModal()
  expect(text).toContain(digest)
  const download = [...document.body.querySelectorAll('button')].find((b) => b.textContent === 'Download & View')
  await act(async () => download?.click())
  expect(fetchMock).toHaveBeenCalledTimes(1)
  const url = new URL(String((fetchMock.mock.calls[0] as unknown[])[0]), 'http://radar.test')
  expect(url.searchParams.get('image')).toBe(state.metadataImage)
})

it('shows no pulled digest when the server inspects the tag as given', async () => {
  state.showMetadata = true
  const text = await renderModal()
  expect(text).not.toContain('sha256:')
})
