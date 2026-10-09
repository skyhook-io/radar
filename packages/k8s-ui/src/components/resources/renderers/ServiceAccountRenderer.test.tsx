// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { ServiceAccountRenderer } from './ServiceAccountRenderer'

it('navigates both declared Secret roles using the host resource-kind contract', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const container = document.createElement('div')
  const root = createRoot(container)
  const onNavigate = vi.fn()
  try {
    await act(async () => root.render(
      <ServiceAccountRenderer
        data={{ metadata: { name: 'runner', namespace: 'team' }, secrets: [{ name: 'token-association' }], imagePullSecrets: [{ name: 'registry' }] }}
        onNavigate={onNavigate}
      />,
    ))
    for (const name of ['token-association', 'registry']) {
      const button = [...container.querySelectorAll('button')].find((button) => button.textContent === name)
      expect(button).toBeDefined()
      await act(async () => button!.click())
      expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'secrets', namespace: 'team', name, group: undefined })
    }
  } finally {
    await act(async () => root.unmount())
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
  }
})

it('leaves secrets[] references to other namespaces, kinds or API groups as text', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const container = document.createElement('div')
  const root = createRoot(container)
  try {
    await act(async () => root.render(
      <ServiceAccountRenderer
        data={{
          metadata: { name: 'runner', namespace: 'team' },
          secrets: [
            { name: 'foreign', namespace: 'other' },
            { name: 'not-a-secret', kind: 'ConfigMap' },
            { name: 'custom-group', apiVersion: 'example.io/v1' },
            { name: 'local', namespace: 'team', kind: 'Secret', apiVersion: 'v1' },
          ],
        }}
        onNavigate={vi.fn()}
      />,
    ))
    const linked = [...container.querySelectorAll('button')].map((button) => button.textContent)
    expect(linked).toContain('local')
    for (const name of ['foreign', 'not-a-secret', 'custom-group']) {
      expect(linked).not.toContain(name)
      expect(container.textContent).toContain(name)
    }
  } finally {
    await act(async () => root.unmount())
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
  }
})
