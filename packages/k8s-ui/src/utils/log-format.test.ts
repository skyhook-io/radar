import { describe, expect, it } from 'vitest'
import { highlightJson, tokenizeJson, unescapeJsonStrings } from './log-format'

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

describe('JSON highlighting', () => {
  it('keeps primitive-looking words inside strings and preserves JSON token colors', () => {
    const tokens = tokenizeJson('{ "text" : "true null 123 \\"quoted\\"", "values": [-1.2e+3, true, false, null, ""] }')
    expect(tokens.filter((token) => token.type === 'string').map((token) => token.value)).toEqual(['"true null 123 \\"quoted\\""', '""'])
    expect(tokens.filter((token) => token.type === 'number').map((token) => token.value)).toEqual(['-1.2e+3'])
    expect(highlightJson('{"a":true,"b":null,"n":12}')).toBe('{<span style="color:#7cacf8">"a"</span>:<span style="color:#c678dd">true</span>,<span style="color:#7cacf8">"b"</span>:<span style="color:#808080">null</span>,<span style="color:#7cacf8">"n"</span>:<span style="color:#e5c07b">12</span>}')
  })

  it('escapes HTML in log values before inserting highlighting markup', () => {
    const html = highlightJson(JSON.stringify({ msg: '<script>alert("x")</script>&' }))
    expect(html).toContain('&lt;script&gt;')
    expect(html).toContain('&lt;/script&gt;&amp;')
    expect(html).not.toContain('<script>')
  })

  it('leaves a long unterminated string as plain text without rescanning escaped quotes', () => {
    const text = '"' + '\\"'.repeat(100_000)
    expect(tokenizeJson(text)).toEqual([{ type: 'text', value: text }])
    expect(highlightJson(text)).toBe(text)
  })
})
