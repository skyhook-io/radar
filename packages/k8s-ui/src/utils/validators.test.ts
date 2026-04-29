import { describe, it, expect } from 'vitest'
import {
  validateRFC1123Label,
  validateRFC1123Subdomain,
  validateHelmReleaseName,
  validatePort,
} from './validators'

// Validation in user-facing forms is a silent-correctness-or-loud-error
// boundary: a regression here either lets bad input through to the API
// (helm install fails with a server-side 422 the user can't connect to
// their action) or rejects valid input (user is locked out of a
// legitimate name). Pin the canonical k8s / Helm rules so any drift
// fails the test instead of the user.

describe('validateRFC1123Label', () => {
  it('accepts simple lowercase labels', () => {
    expect(validateRFC1123Label('default').valid).toBe(true)
    expect(validateRFC1123Label('my-app').valid).toBe(true)
    expect(validateRFC1123Label('a').valid).toBe(true)
    expect(validateRFC1123Label('a1').valid).toBe(true)
    expect(validateRFC1123Label('1a').valid).toBe(true)
  })

  it('rejects empty input', () => {
    const r = validateRFC1123Label('')
    expect(r.valid).toBe(false)
    if (!r.valid) expect(r.error).toMatch(/empty/)
  })

  it('rejects uppercase characters (the SKY-821 bug 25 fixture)', () => {
    const r = validateRFC1123Label('INVALID_NAME!@#')
    expect(r.valid).toBe(false)
    if (!r.valid) expect(r.error).toMatch(/lowercase/)
  })

  it('rejects underscores, spaces, and other special characters', () => {
    expect(validateRFC1123Label('my_app').valid).toBe(false)
    expect(validateRFC1123Label('my app').valid).toBe(false)
    expect(validateRFC1123Label('my.app').valid).toBe(false)
    expect(validateRFC1123Label('my!app').valid).toBe(false)
  })

  it('rejects names with leading or trailing hyphens', () => {
    expect(validateRFC1123Label('-app').valid).toBe(false)
    expect(validateRFC1123Label('app-').valid).toBe(false)
  })

  it('rejects names longer than 63 characters', () => {
    const r = validateRFC1123Label('a'.repeat(64))
    expect(r.valid).toBe(false)
    if (!r.valid) expect(r.error).toMatch(/63/)
  })

  it('accepts a name exactly 63 characters long (boundary)', () => {
    expect(validateRFC1123Label('a'.repeat(63)).valid).toBe(true)
  })
})

describe('validateRFC1123Subdomain', () => {
  it('accepts dotted subdomains', () => {
    expect(validateRFC1123Subdomain('foo.bar').valid).toBe(true)
    expect(validateRFC1123Subdomain('foo.bar.baz').valid).toBe(true)
    expect(validateRFC1123Subdomain('foo').valid).toBe(true)
  })

  it('rejects names with consecutive dots or trailing dots', () => {
    expect(validateRFC1123Subdomain('foo..bar').valid).toBe(false)
    expect(validateRFC1123Subdomain('foo.').valid).toBe(false)
    expect(validateRFC1123Subdomain('.foo').valid).toBe(false)
  })

  it('rejects uppercase', () => {
    expect(validateRFC1123Subdomain('Foo.bar').valid).toBe(false)
  })

  it('rejects names longer than 253 characters', () => {
    const seg = 'a'.repeat(60)
    const tooLong = `${seg}.${seg}.${seg}.${seg}.${seg}` // 60*5 + 4 dots = 304
    const r = validateRFC1123Subdomain(tooLong)
    expect(r.valid).toBe(false)
    if (!r.valid) expect(r.error).toMatch(/253/)
  })
})

describe('validateHelmReleaseName', () => {
  it('rejects the SKY-821 bug 24 fixture verbatim', () => {
    const r = validateHelmReleaseName('Invalid Name With Spaces!')
    expect(r.valid).toBe(false)
  })

  it('accepts legal release names', () => {
    expect(validateHelmReleaseName('nginx').valid).toBe(true)
    expect(validateHelmReleaseName('my-nginx-1').valid).toBe(true)
  })

  it('rejects release names longer than 53 characters', () => {
    const r = validateHelmReleaseName('a'.repeat(54))
    expect(r.valid).toBe(false)
    if (!r.valid) expect(r.error).toMatch(/53/)
  })

  it('accepts a name exactly 53 characters long (boundary)', () => {
    expect(validateHelmReleaseName('a'.repeat(53)).valid).toBe(true)
  })
})

describe('validatePort', () => {
  it('accepts ports in [1, 65535]', () => {
    for (const p of [1, 80, 443, 8080, 65535]) {
      const r = validatePort(p)
      expect(r.valid).toBe(true)
      if (r.valid) expect(r.value).toBe(p)
    }
  })

  it('accepts string ports', () => {
    const r = validatePort('8080')
    expect(r.valid).toBe(true)
    if (r.valid) expect(r.value).toBe(8080)
  })

  it('rejects 0 (the SKY-821 bug 26 silently-rejected case)', () => {
    const r = validatePort('0')
    expect(r.valid).toBe(false)
    if (!r.valid) expect(r.error).toMatch(/0 is reserved|between 1/)
  })

  it('rejects negative numbers (the bug 26 "-1 reverts to default" case)', () => {
    const r = validatePort('-1')
    expect(r.valid).toBe(false)
  })

  it('rejects ports above 65535 (the bug 26 "70000" case)', () => {
    const r = validatePort('70000')
    expect(r.valid).toBe(false)
    if (!r.valid) expect(r.error).toMatch(/65535|between/)
  })

  it('rejects empty input', () => {
    expect(validatePort('').valid).toBe(false)
  })

  it('rejects non-integer formats (decimals, scientific, signed, padded)', () => {
    expect(validatePort('1.0').valid).toBe(false)
    expect(validatePort('1e3').valid).toBe(false)
    expect(validatePort('+1').valid).toBe(false)
    expect(validatePort(' 80 ').valid).toBe(false)
    expect(validatePort('80abc').valid).toBe(false)
  })

  it('does not silently truncate (the "70000 → 7000" maxlength bug)', () => {
    // The browser's number input was effectively dropping the 5th digit
    // on certain triple-select+type sequences. Our validator must NOT
    // accept the truncated value silently — it must reject the full
    // string the user typed.
    const r = validatePort('70000')
    expect(r.valid).toBe(false)
    // Make sure we don't accidentally massage the value into 7000 just
    // because that happens to be a valid port.
    if (!r.valid) {
      // No `value` field on the failure shape — guarantees we never
      // pretend the user typed something else.
      expect((r as unknown as { value?: number }).value).toBeUndefined()
    }
  })
})
