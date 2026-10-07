import { describe, expect, it } from 'vitest'
import { unescapeJsonStrings } from './log-format'

describe('unescapeJsonStrings', () => {
  it('expands whitespace inside strings while leaving escapes outside them untouched', () => {
    expect(unescapeJsonStrings('{"msg":"first\\r\\nsecond\\nthird\\tend"}')).toBe('{"msg":"first\r\nsecond\nthird\tend"}')
    expect(unescapeJsonStrings('outside\\n {"msg":"inside\\n"} outside\\t')).toBe('outside\\n {"msg":"inside\n"} outside\\t')
  })

  it('keeps escaped quotes and backslashes in their original string', () => {
    expect(unescapeJsonStrings('{"msg":"say \\"hello\\"\\nnext","path":"C:\\\\docs"}')).toBe('{"msg":"say \\"hello\\"\nnext","path":"C:\\\\docs"}')
    expect(unescapeJsonStrings(JSON.stringify({ empty: '', nested: ['a\nb', 'c\td'], number: 42, yes: true }))).toBe('{"empty":"","nested":["a\nb","c\td"],"number":42,"yes":true}')
  })

  it('stops once at an unterminated string with many escaped quotes', () => {
    const text = '"' + '\\"'.repeat(100_000) + '\\n'
    expect(unescapeJsonStrings(text)).toBe(text)
  })
})
