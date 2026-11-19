import { describe, expect, it } from 'vitest'
import { withKubectlTarget } from './shared'

describe('withKubectlTarget', () => {
  const revert = 'kubectl ate revert actor lit-0193 -a research'
  it('aims a command that changes an actor at the cluster on screen', () => {
    expect(withKubectlTarget(revert, { context: 'staging' })).toBe(`${revert} --context staging`)
    expect(withKubectlTarget(revert, { context: "it's prod" })).toBe(`${revert} --context 'it'\\''s prod'`)
  })
  it('asks for the context where Radar cannot know the reader’s', () => {
    expect(withKubectlTarget(revert, { context: null })).toBe(`${revert} --context <context>`)
  })
  it('leaves reads, commands that already name a context, and screens with no target as written', () => {
    expect(withKubectlTarget('kubectl ate logs actor a -a b', { context: 'staging' })).toBe('kubectl ate logs actor a -a b')
    expect(withKubectlTarget(`${revert} --context x`, { context: 'staging' })).toBe(`${revert} --context x`)
    expect(withKubectlTarget(revert, undefined)).toBe(revert)
  })
})
