import { describe, expect, it } from 'vitest'
import { normalizeURLForComparison, stripTrailingSlashes } from './url-path'

describe('stripTrailingSlashes', () => {
  it.each([
    ['', ''],
    ['/', ''],
    ['////', ''],
    ['/api///', '/api'],
    ['https://store.example//bucket///', 'https://store.example//bucket'],
    ['https://store.example//bucket?prefix=/', 'https://store.example//bucket?prefix='],
    ['/api//nested', '/api//nested'],
  ])('preserves the existing path normalization for %s', (value, expected) => {
    expect(stripTrailingSlashes(value)).toBe(expected)
  })

  it('handles long slash runs without scanning each possible start', () => {
    const slashes = '/'.repeat(100_000)
    expect(stripTrailingSlashes(`prefix${slashes}`)).toBe('prefix')
    expect(stripTrailingSlashes(`prefix${slashes}suffix`)).toBe(`prefix${slashes}suffix`)
  })
})

it('compares equivalent origins while retaining declared path semantics', () => {
  expect(normalizeURLForComparison('https://STORE.example:443/prefix/')).toBe(normalizeURLForComparison('https://store.example/prefix'))
  expect(normalizeURLForComparison('http://STORE.example:080/')).toBe(normalizeURLForComparison('http://store.example'))
  expect(normalizeURLForComparison('s3://BUCKET/path/')).toBe(normalizeURLForComparison('s3://bucket/path'))
  expect(normalizeURLForComparison('https://store.example/Prefix')).not.toBe(normalizeURLForComparison('https://store.example/prefix'))
  expect(normalizeURLForComparison('https://store.example/a/../b')).not.toBe(normalizeURLForComparison('https://store.example/b'))
  expect(normalizeURLForComparison('https://store.example:8443/path')).not.toBe(normalizeURLForComparison('https://store.example/path'))
  expect(normalizeURLForComparison('')).toBe('')
  expect(normalizeURLForComparison('invalid')).toBeNull()
  expect(normalizeURLForComparison('https://host\\prefix')).toBeNull()
  expect(normalizeURLForComparison('https://user:password@store.example')).toBeNull()
})
