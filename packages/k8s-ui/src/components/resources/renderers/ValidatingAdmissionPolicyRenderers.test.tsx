// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import {
  getBindingParameterReference,
  getTypeCheckingState,
  getValidationActions,
  ValidatingAdmissionPolicyBindingRenderer,
  ValidatingAdmissionPolicyRenderer,
} from './ValidatingAdmissionPolicyRenderers'

const policy = {
  apiVersion: 'admissionregistration.k8s.io/v1',
  kind: 'ValidatingAdmissionPolicy',
  metadata: { name: 'require-owner', generation: 5 },
  spec: {
    failurePolicy: 'Ignore',
    paramKind: { apiVersion: 'rules.example.io/v1', kind: 'AdmissionRules' },
    matchConstraints: {
      matchPolicy: 'Exact',
      namespaceSelector: { matchLabels: { environment: 'production' } },
      objectSelector: { matchExpressions: [{ key: 'managed', operator: 'Exists' }] },
      resourceRules: [{ operations: ['CREATE', 'UPDATE'], apiGroups: ['apps'], apiVersions: ['v1'], resources: ['deployments'], resourceNames: ['production-web'], scope: 'Namespaced' }],
      excludeResourceRules: [{ operations: ['UPDATE'], apiGroups: ['apps'], apiVersions: ['v1'], resources: ['deployments/status'], resourceNames: ['legacy-web'] }],
    },
    validations: [{
      expression: "object.metadata.labels['owner'] != ''",
      message: 'owner is required',
      messageExpression: "'owner missing on ' + object.metadata.name",
      reason: 'Invalid',
    }],
    variables: [{ name: 'owner', expression: "object.metadata.labels['owner']" }],
    auditAnnotations: [{ key: 'owner', valueExpression: 'variables.owner' }],
    matchConditions: [{ name: 'not-breakglass', expression: "!has(object.metadata.labels['breakglass'])" }],
  },
  status: {
    observedGeneration: 4,
    typeChecking: { expressionWarnings: [{ fieldRef: 'spec.validations[0].expression', warning: 'undefined field' }] },
    conditions: [{ type: 'Accepted', status: 'True', reason: 'Accepted' }],
  },
}

describe('ValidatingAdmissionPolicy extraction', () => {
  it('distinguishes current, stale, unknown-freshness, absent, non-numeric, and warning-free type checking', () => {
    expect(getTypeCheckingState({ metadata: { generation: 3 }, status: { observedGeneration: 3, typeChecking: { expressionWarnings: [{ warning: 'bad CEL' }] } } }))
      .toMatchObject({ state: 'current', observedGeneration: 3, warnings: [{ warning: 'bad CEL' }] })
    expect(getTypeCheckingState({ metadata: { generation: 4 }, status: { observedGeneration: 3, typeChecking: { expressionWarnings: [] } } }))
      .toMatchObject({ state: 'stale', observedGeneration: 3, warnings: [] })
    expect(getTypeCheckingState({ metadata: { generation: 1 } })).toEqual({ state: 'unreported', warnings: [] })
    expect(getTypeCheckingState({ metadata: { generation: 7 }, status: { observedGeneration: 7 } }))
      .toEqual({ state: 'unreported', warnings: [] })
    expect(getTypeCheckingState({ metadata: { generation: 7 }, status: { observedGeneration: 7, typeChecking: [] } }))
      .toEqual({ state: 'unreported', warnings: [] })
    expect(getTypeCheckingState({ metadata: { generation: 1 }, status: { observedGeneration: '1', typeChecking: { expressionWarnings: [] } } }))
      .toEqual({ state: 'unreported', warnings: [] })
    expect(getTypeCheckingState({ metadata: { generation: 1 }, status: { typeChecking: { expressionWarnings: [{ warning: 'bad CEL' }] } } }))
      .toEqual({ state: 'unknown', warnings: [{ warning: 'bad CEL' }] })
    expect(getTypeCheckingState({ metadata: { generation: 7 }, status: { observedGeneration: 7, typeChecking: { expressionWarnings: [] } } }))
      .toMatchObject({ state: 'current', warnings: [] })
  })

  it('requires finite generations for a reported type-checking result', () => {
    for (const [generation, observedGeneration] of [[Infinity, 3], [3, Number.NaN]]) {
      expect(getTypeCheckingState({ metadata: { generation }, status: { observedGeneration, typeChecking: { expressionWarnings: [] } } }))
        .toEqual({ state: 'unreported', warnings: [] })
    }
  })

  it('treats a present type-checking result with omitted warnings as zero warnings', () => {
    expect(getTypeCheckingState({ metadata: { generation: 3 }, status: { observedGeneration: 3, typeChecking: {} } }))
      .toMatchObject({ state: 'current', observedGeneration: 3, generation: 3, warnings: [] })
  })

  it('does not report malformed type-checking warnings as zero warnings', () => {
    expect(getTypeCheckingState({ metadata: { generation: 3 }, status: { observedGeneration: 3, typeChecking: { expressionWarnings: 'not-an-array' } } }))
      .toEqual({ state: 'unreported', warnings: [] })
  })

  it('treats a greater observed generation as non-current type-check evidence', () => {
    expect(getTypeCheckingState({ metadata: { generation: 3 }, status: { observedGeneration: 4, typeChecking: { expressionWarnings: [] } } }))
      .toMatchObject({ state: 'stale', observedGeneration: 4, generation: 3, warnings: [] })
  })

  it('preserves declared actions and parameter display modes without inferring outcomes', () => {
    expect(getValidationActions({ spec: { validationActions: ['Deny', 'Warn', 'Audit'] } }))
      .toEqual(['Deny', 'Warn', 'Audit'])
    expect(getBindingParameterReference({ spec: { paramRef: { name: 'strict', namespace: 'rules', parameterNotFoundAction: 'Deny' } } }))
      .toEqual({ mode: 'name', name: 'strict', namespace: 'rules', parameterNotFoundAction: 'Deny' })
    expect(getBindingParameterReference({ spec: { paramRef: { selector: { matchLabels: { tier: 'strict' } }, namespace: 'policy-params', parameterNotFoundAction: 'Allow' } } }))
      .toEqual({ mode: 'selector', selector: { matchLabels: { tier: 'strict' } }, namespace: 'policy-params', parameterNotFoundAction: 'Allow' })
    expect(getBindingParameterReference({ spec: {} })).toEqual({ mode: 'none' })
  })
})

describe('ValidatingAdmissionPolicy renderers', () => {
  it('renders resource names separately for included and excluded rules', () => {
    const html = renderToStaticMarkup(<ValidatingAdmissionPolicyRenderer data={policy} />)
    const included = html.indexOf('Included resource rules')
    const includedName = html.indexOf('Resource Names: production-web')
    const excluded = html.indexOf('Excluded resource rules')
    const excludedName = html.indexOf('Resource Names: legacy-web')
    expect(included).toBeGreaterThanOrEqual(0)
    expect(includedName).toBeGreaterThan(included)
    expect(excluded).toBeGreaterThan(includedName)
    expect(excludedName).toBeGreaterThan(excluded)
  })

  it('labels rule fields when values overlap across Kubernetes rule dimensions', () => {
    const html = renderToStaticMarkup(<ValidatingAdmissionPolicyRenderer data={{
      metadata: { name: 'wildcard' },
      spec: { matchConstraints: { resourceRules: [{ operations: ['*'], apiGroups: ['*'], apiVersions: ['*'], resources: ['*'] }] } },
    }} />)
    for (const text of ['Operation: *', 'API Group: *', 'API Version: *', 'Resource: *']) {
      expect(html).toContain(text)
    }
  })

  it('renders policy configuration and stale type-check evidence without claiming enforcement', () => {
    const html = renderToStaticMarkup(<ValidatingAdmissionPolicyRenderer data={policy} bindings={<div>Readable binding lookup</div>} />)
    for (const text of [
      'Failure Policy', 'Ignore', 'AdmissionRules', 'Match Constraints', 'CREATE', 'deployments',
      'owner is required', 'Message Expression', 'Invalid', 'Variables', 'Audit Annotations',
      'not-breakglass', 'Type-check Warnings', 'undefined field', 'observed generation 4', 'Readable binding lookup',
    ]) expect(html).toContain(text)
    expect(html).not.toMatch(/effective|protected|healthy/i)
  })

  it('renders default failure semantics and never turns absent status into an all-clear claim', () => {
    const html = renderToStaticMarkup(<ValidatingAdmissionPolicyRenderer data={{
      apiVersion: policy.apiVersion,
      metadata: { name: 'minimal', generation: 1 },
      spec: { validations: [] },
    }} />)
    expect(html).toContain('Fail')
    expect(html).toContain('default')
    expect(html).toContain('Type-checking has not been reported')
    expect(html).not.toMatch(/valid|healthy|no warnings/i)
  })

  it('renders reported warnings when their freshness is unavailable', () => {
    const html = renderToStaticMarkup(<ValidatingAdmissionPolicyRenderer data={{
      metadata: { name: 'unknown-freshness' },
      spec: {},
      status: { typeChecking: { expressionWarnings: [{ warning: 'unknown field' }] } },
    }} />)
    expect(html).toContain('unknown field')
    expect(html).toContain('freshness could not be determined')
    expect(html).not.toContain('Type-checking has not been reported')
  })

  it('renders binding actions, declarative parameter reference, and intersection scope guidance', () => {
    const html = renderToStaticMarkup(<ValidatingAdmissionPolicyBindingRenderer data={{
      apiVersion: policy.apiVersion,
      metadata: { name: 'require-owner-prod' },
      spec: {
        policyName: 'require-owner',
        validationActions: ['Deny', 'Warn', 'Audit'],
        paramRef: { name: 'strict', namespace: 'policy-params', parameterNotFoundAction: 'Deny' },
        matchResources: { namespaceSelector: { matchLabels: { environment: 'production' } } },
      },
    }} onNavigate={() => {}} />)
    for (const text of ['require-owner', 'Deny', 'Warn', 'Audit', 'strict', 'policy-params', 'Parameter Not Found', 'intersection']) {
      expect(html).toContain(text)
    }
    expect(html).toContain('Parameter availability is not checked')
    expect(html).not.toContain('>strict</button>')
  })

  it('navigates only the declared policy with exact group and cluster scope', async () => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    const onNavigate = vi.fn()
    const element = document.createElement('div')
    const root = createRoot(element)
    try {
      await act(async () => root.render(<ValidatingAdmissionPolicyBindingRenderer data={{
        apiVersion: policy.apiVersion,
        metadata: { name: 'binding' },
        spec: {
          policyName: 'require-owner',
          validationActions: ['Deny'],
          paramRef: { name: 'strict', namespace: 'policy-params' },
        },
      }} onNavigate={onNavigate} />))
      const policyButton = [...element.querySelectorAll('button')].find(button => button.textContent === 'require-owner')
      expect(policyButton).toBeDefined()
      expect([...element.querySelectorAll('button')].some(button => button.textContent === 'strict')).toBe(false)
      await act(async () => policyButton!.click())
      expect(onNavigate).toHaveBeenCalledWith({
        kind: 'validatingadmissionpolicies',
        namespace: '',
        name: 'require-owner',
        group: 'admissionregistration.k8s.io',
      })
    } finally {
      await act(async () => root.unmount())
    }
  })
})
