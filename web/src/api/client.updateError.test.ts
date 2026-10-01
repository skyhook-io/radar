import { describe, expect, it } from 'vitest'
import { readErrorMessage } from './client'

describe('readErrorMessage', () => {
  it('uses the error field of a JSON body', async () => {
    const response = new Response(
      JSON.stringify({ error: 'failed to update resource: the object has been modified' }),
      { status: 409, headers: { 'Content-Type': 'application/json' } },
    )
    expect(await readErrorMessage(response)).toBe(
      'failed to update resource: the object has been modified',
    )
  })

  it('keeps a plain-text body from a proxy in front of Radar', async () => {
    const response = new Response('upstream connect error\n', {
      status: 502,
      headers: { 'Content-Type': 'text/plain' },
    })
    expect(await readErrorMessage(response)).toBe('upstream connect error')
  })

  it('caps a long plain-text body', async () => {
    const message = await readErrorMessage(
      new Response(`<html>${'x'.repeat(5000)}</html>`, { status: 502 }),
    )
    expect(message).toHaveLength(300)
    expect(message.endsWith('…')).toBe(true)
  })

  it('keeps a long JSON error whole', async () => {
    const error =
      'failed to update resource: Deployment.apps "bubble-relay" is invalid: spec.selector: Invalid value: v1.LabelSelector{MatchLabels:map[string]string{"app.kubernetes.io/name":"hubble-relay", "app.kubernetes.io/part-of":"cilium", "k8s-app":"hubble-relay"}, MatchExpressions:[]v1.LabelSelectorRequirement(nil)}: field is immutable'
    const response = new Response(JSON.stringify({ error }), { status: 422 })
    expect(await readErrorMessage(response)).toBe(error)
  })

  it('falls back to the status when the body is empty', async () => {
    expect(await readErrorMessage(new Response('', { status: 504 }))).toBe('HTTP 504')
  })
})
