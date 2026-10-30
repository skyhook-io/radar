import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ValidatingAdmissionPolicyBindingRenderer,
  ValidatingAdmissionPolicyRenderer,
} from './ValidatingAdmissionPolicyRenderer'

const mock = vi.hoisted(() => ({
  query: {} as any,
  reads: [] as any[],
  policyQuery: {} as any,
  policyReads: [] as any[],
}))

vi.mock('../../../api/client', () => ({
  useResources: (...args: any[]) => {
    mock.reads.push(args)
    return mock.query
  },
  useResourceWithRelationships: (...args: any[]) => {
    mock.policyReads.push(args)
    return mock.policyQuery
  },
}))

const policy = {
  apiVersion: 'admissionregistration.k8s.io/v1',
  kind: 'ValidatingAdmissionPolicy',
  metadata: { name: 'require-owner', generation: 1 },
  spec: { validations: [{ expression: 'true' }] },
}

const binding = (name: string, policyName: string) => ({
  apiVersion: 'admissionregistration.k8s.io/v1',
  kind: 'ValidatingAdmissionPolicyBinding',
  metadata: { name },
  spec: { policyName, validationActions: ['Deny'] },
})

const render = () => renderToStaticMarkup(<ValidatingAdmissionPolicyRenderer data={policy} onNavigate={() => {}} />)

beforeEach(() => {
  mock.query = {}
  mock.reads = []
  mock.policyQuery = {}
  mock.policyReads = []
})

describe('ValidatingAdmissionPolicy reverse binding lookup', () => {
  it('lists only readable bindings that name this policy', () => {
    mock.query = { data: [binding('owner-prod', 'require-owner'), binding('image-prod', 'require-images')], coverage: { status: 'complete' }, isLoading: false }
    const html = render()
    expect(mock.reads).toEqual([[
      'validatingadmissionpolicybindings',
      undefined,
      'admissionregistration.k8s.io',
      { enabled: true, refetchInterval: 30000 },
    ]])
    expect(html).toContain('Bindings (1)')
    expect(html).toContain('owner-prod')
    expect(html).not.toContain('image-prod')
  })

  it('reports no binding only when the list response establishes complete coverage', () => {
    mock.query = { data: [], coverage: { status: 'complete' }, isLoading: false }
    expect(render()).toContain('No readable bindings select this policy')
    expect(render()).toContain('this policy has no effect')

    mock.query = { data: [], coverage: { status: 'unknown' }, isLoading: false }
    const unknown = render()
    expect(unknown).toContain('Binding availability could not be established')
    expect(unknown).not.toContain('this policy has no effect')
    expect(unknown).not.toContain('Bindings (0)')

    mock.query = { isLoading: true }
    const loading = render()
    expect(loading).toContain('Looking for readable bindings')
    expect(loading).not.toContain('No readable bindings')
  })

  it('distinguishes forbidden from other list failures and never reports either as no bindings', () => {
    mock.query = { error: { status: 403, message: 'Forbidden' }, isLoading: false }
    const denied = render()
    expect(denied).toContain('don’t have permission')
    expect(denied).not.toContain('No readable bindings')
    expect(denied).not.toContain('Bindings (0)')

    mock.query = { error: new Error('binding list unavailable'), isLoading: false }
    const failed = render()
    expect(failed).toContain('Could not check')
    expect(failed).toContain('binding list unavailable')
    expect(failed).not.toContain('No readable bindings')
  })

  it('marks a non-empty partial binding list as incomplete', () => {
    mock.query = { data: [binding('owner-prod', 'require-owner')], coverage: { status: 'partial' }, isLoading: false }
    const partial = render()
    expect(partial).toContain('Bindings (1+)')
    expect(partial).toContain('This list may be incomplete')
  })

  it('keeps the binding wrapper declarative and free of parameter discovery reads', () => {
    mock.policyQuery = { data: { resource: policy } }
    const html = renderToStaticMarkup(<ValidatingAdmissionPolicyBindingRenderer data={{
      ...binding('owner-prod', 'require-owner'),
      spec: {
        policyName: 'require-owner',
        validationActions: ['Warn'],
        paramRef: { selector: { matchLabels: { tier: 'strict' } }, parameterNotFoundAction: 'Allow' },
      },
    }} onNavigate={() => {}} />)
    expect(html).toContain('tier')
    expect(html).toContain('strict')
    expect(mock.reads).toEqual([])
    expect(mock.policyReads).toEqual([[
      'validatingadmissionpolicies',
      '',
      'require-owner',
      'admissionregistration.k8s.io',
    ]])
  })

  it('keeps policy loading, missing, denied, and failed reference checks distinct', () => {
    const renderBinding = () => renderToStaticMarkup(<ValidatingAdmissionPolicyBindingRenderer
      data={binding('owner-prod', 'require-owner')}
      onNavigate={() => {}}
    />)

    mock.policyQuery = { isLoading: true }
    expect(renderBinding()).toContain('Checking policy availability')

    mock.policyQuery = { error: { status: 404, message: 'Not Found' } }
    expect(renderBinding()).toContain('Referenced policy was not found')

    mock.policyQuery = { error: { status: 403, message: 'Forbidden' } }
    expect(renderBinding()).toContain('Not permitted to check policy availability')

    mock.policyQuery = { error: new Error('policy read failed') }
    expect(renderBinding()).toContain('Could not check policy availability')

    mock.policyQuery = { data: { resource: policy } }
    const present = renderBinding()
    expect(present).not.toContain('availability')
    expect(present).toContain('>require-owner</button>')
  })
})
