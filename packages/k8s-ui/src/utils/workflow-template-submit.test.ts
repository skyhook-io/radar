import { describe, expect, it } from 'vitest'
import {
  initialParameterValues,
  isAmbiguousSubmitError,
  parameterErrors,
  submittedParameters,
  workflowNameError,
  workflowTemplateParameters,
} from './workflow-template-submit'

const template = {
  metadata: { name: 'migrate', namespace: 'jobs' },
  spec: {
    entrypoint: 'main',
    templates: [
      { name: 'main', inputs: { parameters: [{ name: 'target' }, { name: 'message' }, { name: 'color', default: 'blue' }] } },
      { name: 'helper', inputs: { parameters: [{ name: 'unrelated' }] } },
    ],
    arguments: {
      parameters: [
        { name: 'target', description: 'Database to migrate' },
        { name: 'mode', value: 'dry-run', enum: ['dry-run', 'apply'] },
        { name: 'batch', value: 10 },
        { name: 'region', valueFrom: { configMapKeyRef: { name: 'defaults', key: 'region' } } },
        { name: 'level', enum: [1, 2] },
        { name: 'approval', valueFrom: { supplied: {} } },
        { name: 'target', value: 'duplicate ignored' },
        { value: 'nameless ignored' },
      ],
    },
  },
}

describe('workflowTemplateParameters', () => {
  it('reads each declared parameter as Argo does', () => {
    expect(workflowTemplateParameters(template)).toEqual([
      { name: 'target', description: 'Database to migrate', required: true, defaultValue: undefined, valueFromLabel: undefined, enum: undefined },
      { name: 'mode', description: undefined, required: false, defaultValue: 'dry-run', valueFromLabel: undefined, enum: ['dry-run', 'apply'] },
      { name: 'batch', description: undefined, required: false, defaultValue: '10', valueFromLabel: undefined, enum: undefined },
      { name: 'region', description: undefined, required: false, defaultValue: undefined, valueFromLabel: 'ConfigMap defaults, key region', enum: undefined },
      { name: 'level', description: undefined, required: true, defaultValue: undefined, valueFromLabel: undefined, enum: ['1', '2'] },
      // Only a ConfigMap reference stands in for a value on a submitted run.
      { name: 'approval', description: undefined, required: true, defaultValue: undefined, valueFromLabel: undefined, enum: undefined },
      // The entrypoint's inputs the arguments don't name; another template's are not offered.
      { name: 'message', description: undefined, required: true, defaultValue: undefined, valueFromLabel: undefined, enum: undefined },
      { name: 'color', description: undefined, required: false, defaultValue: 'blue', valueFromLabel: undefined, enum: undefined },
    ])
  })

  it('is empty for a template without arguments', () => {
    expect(workflowTemplateParameters({ spec: {} })).toEqual([])
    expect(workflowTemplateParameters(undefined)).toEqual([])
  })
})

describe('Argo precedence', () => {
  it('leaves an input backed by a ConfigMap to it, ahead of its default', () => {
    const [p] = workflowTemplateParameters({ spec: { entrypoint: 'main', templates: [{ name: 'main', inputs: { parameters: [
      { name: 'env', default: 'staging', valueFrom: { configMapKeyRef: { name: 'c', key: 'env' } } },
      { name: 'zone', value: 'a', valueFrom: { configMapKeyRef: { name: 'c', key: 'zone' } } },
    ] } }] } })
    expect(p).toMatchObject({ required: false, defaultValue: undefined, valueFromLabel: 'ConfigMap c, key env' })
    const both = workflowTemplateParameters({ spec: { entrypoint: 'main', templates: [{ name: 'main', inputs: { parameters: [
      { name: 'env', default: 'staging', valueFrom: { configMapKeyRef: { name: 'c', key: 'env' } } },
      { name: 'zone', value: 'a', valueFrom: { configMapKeyRef: { name: 'c', key: 'zone' } } },
    ] } }] } })
    expect(submittedParameters(both, initialParameterValues(both))).toEqual({})
  })

  it('binds an argument with the enum of the entrypoint input it feeds', () => {
    const params = workflowTemplateParameters({ spec: {
      entrypoint: 'main',
      arguments: { parameters: [{ name: 'mode', value: 'dry-run' }] },
      templates: [{ name: 'main', inputs: { parameters: [{ name: 'mode', enum: ['dry-run', 'apply'] }] } }],
    } })
    expect(params).toEqual([expect.objectContaining({ name: 'mode', defaultValue: 'dry-run', enum: ['dry-run', 'apply'], required: false })])
    expect(parameterErrors(params, { mode: 'destroy' })).toEqual({ mode: 'One of dry-run, apply.' })
  })

  it('requires a literal value for an argument with an enum, whatever its source', () => {
    const [p] = workflowTemplateParameters({ spec: { arguments: { parameters: [
      { name: 'env', enum: ['dev', 'prod'], valueFrom: { configMapKeyRef: { name: 'c', key: 'k' } } },
    ] } } })
    expect(p).toMatchObject({ required: true, valueFromLabel: undefined })
  })
})

describe('the form', () => {
  const parameters = workflowTemplateParameters(template)

  it('starts from the defaults and asks for each required value', () => {
    const values = initialParameterValues(parameters)
    expect(values).toEqual({ target: '', mode: 'dry-run', batch: '10', region: '', level: '', approval: '', message: '', color: 'blue' })
    expect(parameterErrors(parameters, values)).toEqual({ target: 'Required.', level: 'Required.', approval: 'Required.', message: 'Required.' })
  })

  it('refuses a value outside the enum, including a cleared default', () => {
    const filled = { target: 'x', level: '2', approval: 'yes', message: 'hi' }
    expect(parameterErrors(parameters, { ...filled, mode: 'destroy' })).toEqual({ mode: 'One of dry-run, apply.' })
    expect(parameterErrors(parameters, { ...filled, mode: '' })).toEqual({ mode: 'One of dry-run, apply.' })
  })

  it('sends what was reviewed, and leaves an empty valueFrom field to its source', () => {
    const values = { ...initialParameterValues(parameters), target: 'orders', level: '1', approval: 'yes', message: 'hi' }
    expect(submittedParameters(parameters, values)).toEqual({ target: 'orders', mode: 'dry-run', batch: '10', level: '1', approval: 'yes', message: 'hi', color: 'blue' })
    expect(submittedParameters(parameters, { ...values, region: 'eu', batch: '' })).toMatchObject({ batch: '', region: 'eu' })
  })

  it('keeps a parameter named __proto__', () => {
    const protoParams = workflowTemplateParameters({ spec: { arguments: { parameters: [{ name: '__proto__' }] } } })
    const values = { ...initialParameterValues(protoParams), ['__proto__']: 'set' }
    expect(parameterErrors(protoParams, values)).toEqual({})
    expect(Object.entries(submittedParameters(protoParams, values))).toEqual([['__proto__', 'set']])
  })
})

describe('run names', () => {
  it('allows an empty name and explains a name Argo or Kubernetes would refuse', () => {
    expect(workflowNameError('')).toBeNull()
    expect(workflowNameError('a'.repeat(64))).toContain('At most 63')
    expect(workflowNameError('Migrate_1')).toContain('lowercase')
    expect(workflowNameError('migrate.v2-1')).toBeNull()
  })
})

describe('isAmbiguousSubmitError', () => {
  it('treats network errors, timeouts and 5xx as ambiguous', () => {
    expect(isAmbiguousSubmitError(new TypeError('Failed to fetch'))).toBe(true)
    expect(isAmbiguousSubmitError(new Error('timeout'))).toBe(true)
    expect(isAmbiguousSubmitError(Object.assign(new Error('timeout'), { status: 408 }))).toBe(true)
    expect(isAmbiguousSubmitError(Object.assign(new Error('internal'), { status: 500 }))).toBe(true)
    expect(isAmbiguousSubmitError(Object.assign(new Error('gateway'), { status: 504 }))).toBe(true)
  })

  it('treats definite refusals and unsupported features as not ambiguous', () => {
    for (const status of [400, 403, 404, 409, 422]) {
      expect(isAmbiguousSubmitError(Object.assign(new Error('refused'), { status }))).toBe(false)
    }
    const unsupported = Object.assign(new Error('needs newer Radar'), { name: 'RadarFeatureUnsupportedError' })
    expect(isAmbiguousSubmitError(unsupported)).toBe(false)
  })
})
