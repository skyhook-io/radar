import { describe, expect, it } from 'vitest'
import {
  isBlockingYamlDiagnostic,
  parseFallbackYamlDiagnostics,
  parseYamlDocumentIdentities,
  shouldAutoTriggerYamlSuggestions,
} from './YamlEditor'

describe('parseYamlDocumentIdentities', () => {
  it('preserves ordered identities and document start lines', () => {
    const documents = parseYamlDocumentIdentities(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: api
`)
    expect(documents).toEqual([
      {
        index: 0,
        schemaIndex: 0,
        apiVersion: 'apps/v1',
        kind: 'Deployment',
        startLine: 1,
      },
      {
        index: 1,
        schemaIndex: 1,
        apiVersion: 'monitoring.coreos.com/v1',
        kind: 'ServiceMonitor',
        startLine: 5,
      },
    ])
  })

  it('keeps incomplete documents addressable without inventing an identity', () => {
    expect(parseYamlDocumentIdentities('apiVersion: v1\nkind:')).toEqual([
      { index: 0, schemaIndex: 0, apiVersion: 'v1', kind: '', startLine: 1 },
    ])
  })

  it('compacts empty document segments to match preview and apply indices', () => {
    expect(
      parseYamlDocumentIdentities(`apiVersion: apps/v1
kind: Deployment
---

---
apiVersion: v1
kind: ConfigMap
`),
    ).toEqual([
      {
        index: 0,
        schemaIndex: 0,
        apiVersion: 'apps/v1',
        kind: 'Deployment',
        startLine: 1,
      },
      {
        index: 1,
        schemaIndex: 2,
        apiVersion: 'v1',
        kind: 'ConfigMap',
        startLine: 5,
      },
    ])
  })
})

describe('isBlockingYamlDiagnostic', () => {
  it('blocks syntax failures when no schema source is present', () => {
    expect(isBlockingYamlDiagnostic('error')).toBe(true)
    expect(isBlockingYamlDiagnostic('warning', '0', 'Incorrect type. Expected integer.')).toBe(true)
  })

  it('keeps cluster schema diagnostics advisory', () => {
    expect(
      isBlockingYamlDiagnostic(
        'warning',
        '513',
        'Property contianers is not allowed.',
        'yaml-schema: Kubernetes cluster schema',
      ),
    ).toBe(false)
  })

  it('keeps deprecations visible but advisory', () => {
    expect(isBlockingYamlDiagnostic('warning', '2', 'Use spec.newField instead.')).toBe(false)
    expect(
      isBlockingYamlDiagnostic(
        'warning',
        undefined,
        '[radar-advisory:deprecated] Use spec.newField instead.',
      ),
    ).toBe(false)
  })
})

describe('parseFallbackYamlDiagnostics', () => {
  it('accepts valid multi-document YAML', () => {
    expect(
      parseFallbackYamlDiagnostics(`apiVersion: v1
kind: ConfigMap
---
apiVersion: apps/v1
kind: Deployment
`),
    ).toEqual([])
  })

  it('reports a blocking syntax error against the correct document', () => {
    const diagnostics = parseFallbackYamlDiagnostics(`apiVersion: v1
kind: ConfigMap
---
apiVersion: v1
kind: [`)

    expect(diagnostics).toHaveLength(1)
    expect(diagnostics[0]).toMatchObject({
      severity: 'error',
      line: 5,
      column: 8,
      documentIndex: 1,
      blocking: true,
    })
  })
})

describe('shouldAutoTriggerYamlSuggestions', () => {
  it('triggers after Enter creates an indented blank line', () => {
    expect(shouldAutoTriggerYamlSuggestions(['\n      '], '      ', 7)).toBe(true)
    expect(shouldAutoTriggerYamlSuggestions(['\r\n  '], '  ', 3)).toBe(true)
  })

  it('does not trigger for typing, pasted content, or a non-blank line', () => {
    expect(shouldAutoTriggerYamlSuggestions([' '], '      ', 7)).toBe(false)
    expect(shouldAutoTriggerYamlSuggestions(['\n      image:'], '      image:', 13)).toBe(false)
    expect(shouldAutoTriggerYamlSuggestions(['\n      '], '      ports:', 13)).toBe(false)
  })

  it('waits until the cursor is at the end of the new indentation', () => {
    expect(shouldAutoTriggerYamlSuggestions(['\n      '], '      ', 4)).toBe(false)
  })
})
