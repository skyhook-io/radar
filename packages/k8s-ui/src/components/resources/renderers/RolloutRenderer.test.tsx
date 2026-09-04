import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import {
  RolloutRenderer,
  rolloutActions,
  canaryStepLabel,
  canaryStepTemplateRefs,
  rolloutAnalysisRuns,
  rolloutConditionTone,
  rolloutProblems,
  blueGreenPhases,
  type RolloutAction,
  type RolloutCapabilities,
} from './RolloutRenderer'

const allCapabilities: RolloutCapabilities = {
  abort: true,
  retry: true,
  promote: true,
  promoteFull: true,
  skipStep: true,
  rollback: true,
  restart: true,
  strategy: 'canary',
  terminating: false,
}

function canaryRollout(status: Record<string, unknown>, steps = 4) {
  return {
    metadata: { name: 'web', namespace: 'prod' },
    spec: {
      replicas: 4,
      strategy: { canary: { steps: Array.from({ length: steps }, () => ({ setWeight: 25 })) } },
      template: { spec: { containers: [{ name: 'web', image: 'web:v2' }] } },
    },
    status,
  }
}

function offered(data: unknown, capabilities?: RolloutCapabilities): RolloutAction[] {
  return rolloutActions(data, capabilities).map((a) => a.action)
}

function blockedReason(data: unknown, action: RolloutAction, capabilities = allCapabilities) {
  return rolloutActions(data, capabilities).find((a) => a.action === action)?.blocked
}

describe('rolloutActions capability gating', () => {
  it('offers nothing until the capability probe resolves', () => {
    expect(rolloutActions(canaryRollout({ phase: 'Paused' }), undefined)).toEqual([])
  })

  it('omits a denied verb rather than disabling it', () => {
    const caps = { ...allCapabilities, promoteFull: false, abort: false }
    expect(offered(canaryRollout({ phase: 'Paused' }), caps)).toEqual(['promote', 'skip-step'])
  })

  it('omits every verb when the probe denies all of them', () => {
    const caps: RolloutCapabilities = {
      ...allCapabilities,
      abort: false,
      retry: false,
      promote: false,
      promoteFull: false,
      skipStep: false,
    }
    expect(offered(canaryRollout({ phase: 'Paused' }), caps)).toEqual([])
  })

  it('offers only the blueGreen-relevant verbs when the probe reports no skipStep', () => {
    const blueGreen = {
      spec: { strategy: { blueGreen: { activeService: 'active' } } },
      status: { phase: 'Paused', pauseConditions: [{ reason: 'BlueGreenPause' }] },
    }
    const caps = { ...allCapabilities, skipStep: false, strategy: 'blueGreen' }
    expect(offered(blueGreen, caps)).toEqual(['promote', 'promote-full', 'abort'])
  })
})

describe('rolloutActions state gating', () => {
  it('swaps Abort for Retry once the rollout is aborted', () => {
    expect(offered(canaryRollout({ phase: 'Paused' }), allCapabilities)).toContain('abort')
    expect(offered(canaryRollout({ phase: 'Paused' }), allCapabilities)).not.toContain('retry')

    const aborted = offered(canaryRollout({ phase: 'Degraded', abort: true }), allCapabilities)
    expect(aborted).toContain('retry')
    expect(aborted).not.toContain('abort')
  })

  it('blocks Promote unless the rollout is paused', () => {
    expect(blockedReason(canaryRollout({ phase: 'Paused' }), 'promote')).toBeUndefined()
    expect(blockedReason(canaryRollout({ phase: 'Progressing', currentStepIndex: 1 }), 'promote')).toBe(
      'Rollout is not paused and no analysis is running',
    )
    expect(blockedReason(canaryRollout({ phase: 'Healthy' }), 'promote')).toBe(
      'Rollout is not paused and no analysis is running',
    )
  })

  it('blocks the promotion verbs on a fully promoted rollout', () => {
    const settled = canaryRollout({ phase: 'Healthy', currentStepIndex: 4 }, 4)
    expect(blockedReason(settled, 'promote-full')).toBe('Nothing left to promote')
    expect(blockedReason(settled, 'skip-step')).toBe('No canary step left to skip')
    expect(blockedReason(settled, 'abort')).toBe('Rollout is fully promoted — roll back instead')
  })

  it('offers the promotion verbs while the controller is still behind the spec', () => {
    // A rollback bumps the generation; until the controller writes status the phase is
    // still the pre-rollback one, and this is exactly when Promote full is needed.
    const settled = canaryRollout({ phase: 'Healthy', currentStepIndex: 4 }, 4)
    const stale = {
      ...settled,
      metadata: { ...settled.metadata, generation: 4 },
      status: { ...settled.status, observedGeneration: '3' },
    }
    expect(blockedReason(stale, 'promote-full')).toBeUndefined()
    // Abort deliberately keeps its phase-based gate: in this window status.stable still
    // points at the revision the operator just rolled away from.
    expect(blockedReason(stale, 'abort')).toBe('Rollout is fully promoted — roll back instead')
    // Once the controller catches up the settled reading is trustworthy again.
    const caughtUp = { ...stale, status: { ...settled.status, observedGeneration: '4' } }
    expect(blockedReason(caughtUp, 'promote-full')).toBe('Nothing left to promote')
  })

  it('keeps Promote full offered on a workloadRef Rollout, whose template lives elsewhere', () => {
    const settled = canaryRollout({ phase: 'Healthy', currentStepIndex: 4 }, 4)
    const workloadRef = {
      ...settled,
      spec: { ...settled.spec, workloadRef: { kind: 'Deployment', name: 'web' } },
    }
    expect(blockedReason(workloadRef, 'promote-full')).toBeUndefined()
  })

  it('routes an aborted rollout through Retry before it will promote again', () => {
    const aborted = canaryRollout({ phase: 'Degraded', abort: true, currentStepIndex: 1 })
    expect(blockedReason(aborted, 'promote-full')).toBe('Retry the rollout first')
    expect(blockedReason(aborted, 'skip-step')).toBe('Retry the rollout first')
  })

  // An aborted rollout is Degraded, not Healthy, so isSettled must not swallow it.
  it('does not treat an aborted rollout as settled', () => {
    const abortedHealthy = canaryRollout({ phase: 'Healthy', abort: true })
    expect(blockedReason(abortedHealthy, 'promote-full')).toBe('Retry the rollout first')
    expect(offered(abortedHealthy, allCapabilities)).toContain('retry')
  })

  it('blocks Skip step once the last step is reached', () => {
    expect(blockedReason(canaryRollout({ phase: 'Paused', currentStepIndex: 3 }, 4), 'skip-step')).toBeUndefined()
    expect(blockedReason(canaryRollout({ phase: 'Paused', currentStepIndex: 4 }, 4), 'skip-step')).toBe(
      'No canary step left to skip',
    )
  })

  it('treats a missing currentStepIndex as step 0 with steps remaining', () => {
    expect(blockedReason(canaryRollout({ phase: 'Paused' }, 4), 'skip-step')).toBeUndefined()
  })

  it('blocks Skip step on a stepless canary', () => {
    expect(blockedReason(canaryRollout({ phase: 'Paused' }, 0), 'skip-step')).toBe('No canary step left to skip')
  })

  it('survives a Rollout with no status at all', () => {
    expect(() => rolloutActions({ spec: {} }, allCapabilities)).not.toThrow()
    expect(blockedReason({ spec: {} }, 'promote')).toBe(
      'Rollout is not paused and no analysis is running',
    )
  })
})

describe('RolloutRenderer step display', () => {
  it('does not render beyond the final declared canary step', () => {
    const html = renderToString(<RolloutRenderer data={canaryRollout({ phase: 'Healthy', currentStepIndex: 4 }, 4)} />)
    expect(html).toContain('4/4')
    expect(html).not.toContain('5/4')
  })
})

describe('rolloutProblems', () => {
  it('reports an aborted rollout once, not once per matching branch', () => {
    const problems = rolloutProblems(
      canaryRollout({
        phase: 'Degraded',
        abort: true,
        message: 'RolloutAborted: Rollout aborted update to revision 6',
      }),
    )
    expect(problems).toEqual([
      { color: 'red', message: 'RolloutAborted: Rollout aborted update to revision 6' },
    ])
  })

  it('collapses a Progressing condition that echoes status.message', () => {
    const problems = rolloutProblems(
      canaryRollout({
        phase: 'Degraded',
        message: 'ProgressDeadlineExceeded: ReplicaSet has timed out',
        conditions: [
          {
            type: 'Progressing',
            status: 'False',
            reason: 'ProgressDeadlineExceeded',
            message: 'ProgressDeadlineExceeded: ReplicaSet has timed out',
          },
        ],
      }),
    )
    expect(problems).toHaveLength(1)
  })

  it('keeps genuinely different messages apart', () => {
    const problems = rolloutProblems(
      canaryRollout({
        phase: 'Degraded',
        abort: true,
        message: 'RolloutAborted: analysis failed',
        canary: { currentStepAnalysisRunStatus: { name: 'ar-1', status: 'Failed', message: 'error rate 12%' } },
      }),
    )
    expect(problems).toHaveLength(2)
    expect(problems[1].message).toBe('Step analysis failed (ar-1): error rate 12%')
  })

  it('names every pause reason with how long it has been waiting', () => {
    const problems = rolloutProblems(
      canaryRollout({
        phase: 'Paused',
        pauseConditions: [{ reason: 'CanaryPauseStep', startTime: '2020-01-01T00:00:00Z' }],
      }),
    )
    expect(problems).toHaveLength(1)
    expect(problems[0].color).toBe('yellow')
    expect(problems[0].message).toMatch(/^Rollout is paused: CanaryPauseStep \(since .+\)$/)
  })

  it('does not report a paused rollout that is actually aborted', () => {
    const problems = rolloutProblems(
      canaryRollout({ phase: 'Paused', abort: true, message: 'aborted mid-step' }),
    )
    expect(problems.map((p) => p.message)).toEqual(['aborted mid-step'])
  })

  it('reports nothing for a healthy rollout', () => {
    expect(rolloutProblems(canaryRollout({ phase: 'Healthy' }))).toEqual([])
  })

  it('surfaces an invalid spec', () => {
    const problems = rolloutProblems(
      canaryRollout({
        phase: 'Progressing',
        conditions: [{ type: 'InvalidSpec', status: 'True', message: 'spec.strategy is required' }],
      }),
    )
    expect(problems).toEqual([{ color: 'red', message: 'spec.strategy is required' }])
  })
})

describe('rolloutAnalysisRuns', () => {
  it('labels every populated slot with its trigger', () => {
    const runs = rolloutAnalysisRuns({
      canary: {
        currentStepAnalysisRunStatus: { name: 'ar-step', status: 'Inconclusive', message: 'no verdict' },
        currentBackgroundAnalysisRunStatus: { name: 'ar-bg', status: 'Successful' },
      },
      blueGreen: {
        prePromotionAnalysisRunStatus: { name: 'ar-pre', status: 'Running' },
        postPromotionAnalysisRunStatus: { name: 'ar-post', status: 'Failed' },
      },
    })
    expect(runs.map((r) => [r.label, r.name, r.status])).toEqual([
      ['Step analysis', 'ar-step', 'Inconclusive'],
      ['Background analysis', 'ar-bg', 'Successful'],
      ['Pre-promotion analysis', 'ar-pre', 'Running'],
      ['Post-promotion analysis', 'ar-post', 'Failed'],
    ])
    expect(runs[0].message).toBe('no verdict')
  })

  // A slot with no name is a slot the controller has not populated.
  it('drops nameless and absent slots', () => {
    expect(rolloutAnalysisRuns({})).toEqual([])
    expect(rolloutAnalysisRuns(undefined)).toEqual([])
    expect(rolloutAnalysisRuns({ canary: { currentStepAnalysisRunStatus: { status: 'Running' } } })).toEqual([])
    expect(rolloutAnalysisRuns({ canary: { currentStepAnalysisRunStatus: { name: '', status: 'Running' } } })).toEqual(
      [],
    )
  })
})

describe('rolloutActions confirmation gating', () => {
  it('confirms exactly the verbs that shift production traffic in one click', () => {
    const specs = rolloutActions(canaryRollout({ phase: 'Paused' }), allCapabilities)
    const confirmed = specs.filter((a) => a.confirm).map((a) => a.action)
    expect(confirmed.sort()).toEqual(['abort', 'promote-full'])
  })
})

describe('rolloutActions promote reachability', () => {
  // The controller reports Progressing, not Paused, while a step analysis runs —
  // and promote is what advances past it, so it must not be blocked there.
  it('offers promote while a step analysis is in flight', () => {
    for (const status of ['Running', 'Pending', 'Inconclusive']) {
      const data = canaryRollout({
        phase: 'Progressing',
        currentStepIndex: 2,
        canary: { currentStepAnalysisRunStatus: { name: 'ar-1', status } },
      })
      expect(blockedReason(data, 'promote')).toBeUndefined()
    }
  })

  it('still blocks promote on a plain progressing rollout with no analysis', () => {
    const data = canaryRollout({ phase: 'Progressing', currentStepIndex: 1 })
    expect(blockedReason(data, 'promote')).toBe('Rollout is not paused and no analysis is running')
  })

  it('leaves promote unblocked when paused', () => {
    expect(blockedReason(canaryRollout({ phase: 'Paused' }), 'promote')).toBeUndefined()
  })

  // Live state after aborting a Rollout paused on an inconclusive analysis: the
  // analysis status persists, so promote must not ride analysisInFlight past the abort.
  it('blocks promote on an aborted rollout whose analysis stayed inconclusive', () => {
    const data = canaryRollout({
      phase: 'Degraded',
      abort: true,
      currentStepIndex: 0,
      canary: { currentStepAnalysisRunStatus: { name: 'ar-1', status: 'Inconclusive' } },
    })
    expect(blockedReason(data, 'promote')).toBe('Retry the rollout first')
  })

  it('blocks promote, promote-full and skip-step alike on an aborted rollout', () => {
    const data = canaryRollout({ phase: 'Degraded', abort: true, currentStepIndex: 1 })
    for (const action of ['promote', 'promote-full', 'skip-step']) {
      expect(blockedReason(data, action)).toBe('Retry the rollout first')
    }
  })
})

describe('canaryStepLabel', () => {
  it('labels every step type Argo defines without leaking JSON', () => {
    const cases: Array<[any, string]> = [
      [{ setWeight: 25 }, 'Set weight: 25%'],
      [{ pause: {} }, 'Pause: until promoted'],
      [{ pause: { duration: '30s' } }, 'Pause: 30s'],
      [{ analysis: { templates: [{ templateName: 'success-rate' }] } }, 'Analysis: success-rate'],
      [
        { analysis: { templates: [{ clusterTemplateName: 'shared-check' }] } },
        'Analysis: shared-check',
      ],
      [
        { experiment: { templates: [{ name: 'baseline' }], duration: '5m' } },
        'Experiment: baseline for 5m',
      ],
      [{ setCanaryScale: { weight: 40 } }, 'Set canary scale: 40%'],
      [{ setCanaryScale: { replicas: 3 } }, 'Set canary scale: 3 replicas'],
      [{ setCanaryScale: { matchTrafficWeight: true } }, 'Set canary scale: match traffic weight'],
      [
        { setHeaderRoute: { name: 'canary-hdr', match: [{ headerName: 'x-canary' }] } },
        'Header route canary-hdr: x-canary',
      ],
      [{ setHeaderRoute: { name: 'canary-hdr', match: [] } }, 'Remove header route: canary-hdr'],
      [
        { setMirrorRoute: { name: 'mirror', match: [{}], percentage: 20 } },
        'Mirror route mirror (20%)',
      ],
      [{ setMirrorRoute: { name: 'mirror' } }, 'Remove mirror route: mirror'],
      [{ plugin: { name: 'acme/step' } }, 'Plugin: acme/step'],
    ]
    for (const [step, want] of cases) {
      expect(canaryStepLabel(step)).toBe(want)
    }
  })

  it('names an unrecognized step instead of dumping its body', () => {
    const label = canaryStepLabel({ someFutureStep: { nested: { a: 1 } } })
    expect(label).toBe('Unrecognized step: someFutureStep')
    expect(label).not.toContain('{')
  })

  it('survives malformed steps', () => {
    expect(canaryStepLabel({})).toBe('Unknown step')
    expect(canaryStepLabel(null)).toBe('Unknown step')
  })

  it('renders an analysis step as prose in the step list', () => {
    const html = renderToString(
      <RolloutRenderer
        data={{
          metadata: { name: 'web', namespace: 'prod' },
          spec: {
            strategy: {
              canary: {
                steps: [{ analysis: { templates: [{ templateName: 'success-rate' }] } }],
              },
            },
            template: { spec: { containers: [{ name: 'web', image: 'web:v2' }] } },
          },
          status: { phase: 'Progressing' },
        }}
      />,
    )
    expect(html).toContain('Analysis: success-rate')
    expect(html).not.toContain('templateName')
  })
})

describe('canaryStepTemplateRefs', () => {
  it('reads a canary step analysis.templates[] entry', () => {
    const refs = canaryStepTemplateRefs({ analysis: { templates: [{ templateName: 'success-rate' }] } })
    expect(refs).toEqual([{ name: 'success-rate', clusterScoped: false }])
  })

  // Real Argo Rollouts schema uses ONE field name (templateName) for both
  // namespaced and cluster-scoped refs, disambiguated by a separate
  // clusterScope boolean — not a distinct clusterTemplateName field.
  it('reads clusterScoped off the clusterScope flag, not a distinct field name', () => {
    const refs = canaryStepTemplateRefs({ analysis: { templates: [{ templateName: 'shared-check', clusterScope: true }] } })
    expect(refs).toEqual([{ name: 'shared-check', clusterScoped: true }])
  })

  // Experiment spec.analyses[] entries carry templateName/clusterScope flat
  // on the entry itself, not nested under a templates[] array.
  it('reads a flat Experiment spec.analyses[] entry', () => {
    const refs = canaryStepTemplateRefs({ name: 'baseline-check', templateName: 'success-rate', clusterScope: false })
    expect(refs).toEqual([{ name: 'success-rate', clusterScoped: false }])
  })

  it('reads a flat, cluster-scoped Experiment analysis entry', () => {
    const refs = canaryStepTemplateRefs({ name: 'shared', templateName: 'shared-check', clusterScope: true })
    expect(refs).toEqual([{ name: 'shared-check', clusterScoped: true }])
  })

  it('returns an empty list for a step with no analysis refs', () => {
    expect(canaryStepTemplateRefs({ setWeight: 25 })).toEqual([])
    expect(canaryStepTemplateRefs(null)).toEqual([])
  })
})

describe('blueGreenPhases', () => {
  // Argo Rollouts clears previewSelector once there's no active preview to
  // track — a settled, fully-promoted blueGreen commonly has none, so
  // activeSelector === previewSelector alone never fires here even though
  // cutover genuinely completed.
  it('treats activeSelector matching currentPodHash as promoted, even with an empty previewSelector', () => {
    const data = {
      spec: { strategy: { blueGreen: {} } },
      status: {
        currentPodHash: 'abc123',
        blueGreen: { activeSelector: 'abc123', previewSelector: '' },
      },
    }
    const phases = blueGreenPhases(data)
    expect(phases.find((p) => p.label === 'Active cutover')?.state).toBe('completed')
    expect(phases.every((p) => p.state !== 'current')).toBe(true)
  })

  it('still recognizes the narrower activeSelector === previewSelector window', () => {
    const data = {
      spec: { strategy: { blueGreen: {} } },
      status: {
        currentPodHash: 'def456',
        blueGreen: { activeSelector: 'abc123', previewSelector: 'abc123' },
      },
    }
    const phases = blueGreenPhases(data)
    expect(phases.find((p) => p.label === 'Active cutover')?.state).toBe('completed')
  })

  it('is not promoted before either selector matches anything', () => {
    const data = {
      spec: { strategy: { blueGreen: {} } },
      status: {
        currentPodHash: 'abc123',
        blueGreen: { activeSelector: 'old999', previewSelector: 'abc123' },
      },
    }
    const phases = blueGreenPhases(data)
    expect(phases.find((p) => p.label === 'Active cutover')?.state).not.toBe('completed')
  })
})

describe('rolloutConditionTone', () => {
  it('reads Paused=False as the healthy state', () => {
    expect(rolloutConditionTone({ type: 'Paused', status: 'False' })).toBe('ok')
  })

  it('warns rather than fails when actually paused', () => {
    expect(rolloutConditionTone({ type: 'Paused', status: 'True' })).toBe('warning')
  })

  it('reads InvalidSpec=False as the healthy state', () => {
    expect(rolloutConditionTone({ type: 'InvalidSpec', status: 'False' })).toBe('ok')
  })

  it('fails on InvalidSpec=True', () => {
    expect(rolloutConditionTone({ type: 'InvalidSpec', status: 'True' })).toBe('fail')
  })

  it('defers to the generic rule for ordinary condition types', () => {
    expect(rolloutConditionTone({ type: 'Available', status: 'True' })).toBeUndefined()
    expect(rolloutConditionTone({ type: 'Progressing', status: 'False' })).toBeUndefined()
  })

  it('defers when the status is neither True nor False', () => {
    expect(rolloutConditionTone({ type: 'Paused', status: 'Unknown' })).toBeUndefined()
    expect(rolloutConditionTone({ type: 'InvalidSpec' })).toBeUndefined()
  })
})

describe('RolloutRenderer', () => {
  it('renders no action row without an onAction handler', () => {
    const html = renderToString(
      <RolloutRenderer data={canaryRollout({ phase: 'Paused' })} capabilities={allCapabilities} />,
    )
    expect(html).not.toContain('Skip step')
    expect(html).not.toContain('Promote')
  })

  it('counts no failing conditions on a settled healthy Rollout', () => {
    const html = renderToString(
      <RolloutRenderer
        data={canaryRollout({
          phase: 'Healthy',
          conditions: [
            { type: 'Available', status: 'True' },
            { type: 'Paused', status: 'False' },
            { type: 'Healthy', status: 'True' },
            { type: 'Progressing', status: 'True' },
            { type: 'Completed', status: 'True' },
          ],
        })}
      />,
    )
    expect(html).not.toContain('failing')
  })

  it('renders the offered verbs when wired', () => {
    const html = renderToString(
      <RolloutRenderer
        data={canaryRollout({ phase: 'Paused' })}
        capabilities={allCapabilities}
        onAction={() => {}}
      />,
    )
    expect(html).toContain('Promote full')
    expect(html).toContain('Skip step')
    expect(html).toContain('Abort')
  })

  it('marks the in-flight action', () => {
    const html = renderToString(
      <RolloutRenderer
        data={canaryRollout({ phase: 'Paused' })}
        capabilities={allCapabilities}
        onAction={() => {}}
        pendingAction="promote"
      />,
    )
    expect(html).toContain('Promoting…')
    expect(html).toContain('animate-pulse')
  })

  it('names the inconclusive AnalysisRun and its message alongside the pause reason', () => {
    const html = renderToString(
      <RolloutRenderer
        data={canaryRollout({
          phase: 'Paused',
          currentStepIndex: 1,
          pauseConditions: [{ reason: 'InconclusiveAnalysisRun' }],
          canary: {
            currentStepAnalysisRunStatus: {
              name: 'web-6c4f-2',
              status: 'Inconclusive',
              message: 'success-rate returned no verdict',
            },
          },
        })}
      />,
    )
    expect(html).toContain('InconclusiveAnalysisRun')
    expect(html).toContain('web-6c4f-2')
    expect(html).toContain('success-rate returned no verdict')
  })

  it('escalates a failed analysis to an error alert, not a warning', () => {
    const html = renderToString(
      <RolloutRenderer
        data={canaryRollout({
          phase: 'Degraded',
          canary: { currentStepAnalysisRunStatus: { name: 'web-6c4f-2', status: 'Failed' } },
        })}
      />,
    )
    expect(html).toContain('Step analysis failed')
    expect(html).toContain('Issue Detected')
  })

  it('renders no Analysis section when no slot is populated', () => {
    const html = renderToString(<RolloutRenderer data={canaryRollout({ phase: 'Healthy' })} />)
    expect(html).not.toContain('Step analysis')
  })
})
