import { describe, expect, it } from 'vitest'
import { withKubectlTarget } from './shared'

describe('withKubectlTarget', () => {
  const revert = 'kubectl ate revert actor lit-0193 -a research'
  it('aims a command that changes an actor at the cluster on screen', () => {
    expect(withKubectlTarget(revert, { context: 'staging' })).toBe(`${revert} --context staging`)
    expect(withKubectlTarget(revert, { context: "it's prod" })).toBe(`${revert} --context 'it'\\''s prod'`)
  })
  it('names the kubeconfig file and a direct endpoint Radar reads through', () => {
    expect(withKubectlTarget(revert, { context: 'dev', kubeconfig: '/home/me/.kube/staging', endpoint: '127.0.0.1:18443' }))
      .toBe(`${revert} --kubeconfig /home/me/.kube/staging --context dev --endpoint 127.0.0.1:18443`)
  })
  it('asks for the context where Radar cannot know the reader’s', () => {
    expect(withKubectlTarget(revert, { context: null })).toBe(`${revert} --context <context>`)
  })
  it('names an installation’s own endpoint, so the right context can’t reach the default Substrate instead', () => {
    expect(withKubectlTarget(revert, { context: null, endpoint: 'substrate.internal:8443' })).toBe(`${revert} --context <context> --endpoint substrate.internal:8443`)
  })
  it('leaves reads, commands that already name a context, and screens with no target as written', () => {
    expect(withKubectlTarget('kubectl ate logs actor a -a b', { context: 'staging' })).toBe('kubectl ate logs actor a -a b')
    expect(withKubectlTarget(`${revert} --context x`, { context: 'staging' })).toBe(`${revert} --context x`)
    expect(withKubectlTarget(revert, undefined)).toBe(revert)
  })
})
