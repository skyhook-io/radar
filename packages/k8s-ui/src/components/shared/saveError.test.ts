import { describe, expect, it } from 'vitest'

import { isConflictSaveError } from './EditableYamlView'

describe('isConflictSaveError', () => {
  // The apiserver's own optimistic-concurrency message, verbatim — what a
  // Deployment edit hits when a controller writes status underneath the editor.
  it('recognizes the apiserver conflict', () => {
    expect(
      isConflictSaveError(
        'Operation cannot be fulfilled on deployments.apps "bubble-relay": the object has been modified; please apply your changes to the latest version and try again',
      ),
    ).toBe(true)
  })

  // Radar's own guard, raised before the write when the reviewed
  // resourceVersion no longer matches the live object.
  it('recognizes the reviewed-version guard', () => {
    expect(
      isConflictSaveError(
        'Operation cannot be fulfilled on deployments.apps "web": resource changed after review; review the latest version before applying',
      ),
    ).toBe(true)
  })

  it('recognizes a context switch between review and save', () => {
    expect(isConflictSaveError('cluster context changed after review; review the YAML again before saving')).toBe(true)
  })

  // A rejected edit is NOT a conflict: re-applying the same YAML against a
  // fresher version fails identically, so it must not be offered as the fix.
  it('leaves validation rejections alone', () => {
    expect(
      isConflictSaveError(
        'Deployment.apps "web" is invalid: spec.template.spec.containers[0].image: Required value',
      ),
    ).toBe(false)
    expect(isConflictSaveError('admission webhook "policy.example.com" denied the request')).toBe(false)
    expect(isConflictSaveError('deployments.apps "web" is forbidden: User cannot patch resource')).toBe(false)
  })
})
