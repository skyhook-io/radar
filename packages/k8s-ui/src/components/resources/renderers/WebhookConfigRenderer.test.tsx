// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { WebhookConfigRenderer } from './WebhookConfigRenderer'

describe('WebhookConfigRenderer backend navigation', () => {
  for (const isMutating of [false, true]) {
    it(`navigates the ${isMutating ? 'mutating' : 'validating'} backend in its declared namespace`, async () => {
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
      const container = document.createElement('div')
      const root = createRoot(container)
      const onNavigate = vi.fn()
      try {
        await act(async () => root.render(<WebhookConfigRenderer isMutating={isMutating} onNavigate={onNavigate} data={{ webhooks: [{ name: 'hook.example.com', failurePolicy: 'Ignore', clientConfig: { service: { namespace: 'backend', name: 'admission', port: 9443, path: '/validate' } } }] }} />))
        const button = [...container.querySelectorAll('button')].find(button => button.textContent === 'backend/admission')
        expect(button).toBeDefined()
        await act(async () => button!.click())
        expect(onNavigate).toHaveBeenCalledWith({ kind: 'services', namespace: 'backend', name: 'admission' })
        expect(container.textContent).toContain(':9443/validate')
        await act(async () => root.render(<WebhookConfigRenderer onNavigate={onNavigate} data={{ webhooks: [{ name: 'external.example.com', clientConfig: { url: 'https://external.example.com/validate' } }] }} />))
        expect(container.textContent).toContain('URL: https://external.example.com/validate')
        expect([...container.querySelectorAll('button')].some(button => button.textContent?.includes('external.example.com'))).toBe(false)
      } finally {
        await act(async () => root.unmount())
        Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
      }
    })
  }
})
