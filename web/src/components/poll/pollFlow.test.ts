import { describe, expect, it } from 'vitest'
import { CURRENT_ROUND, POLL_COPY, type PollQuestion } from './pollDefinition'
import { closingLine, discoveryLinks, newSubmissionId, offeredOptions, pruneAnswers, toggleChoice, visibleQuestions, type PollContext } from './pollFlow'

const local: PollContext = { mode: 'local', available: { argocd: false, prometheus: false } }
const inCluster: PollContext = { ...local, mode: 'in-cluster' }
const q = (id: string) => CURRENT_ROUND.questions.find((x) => x.id === id) as PollQuestion
const ids = (ctx: PollContext, answers = {}) => visibleQuestions(ctx, answers).map((x) => x.id)

describe('visible questions', () => {
  it('asks nine core questions plus Round A locally, with no follow-ups yet', () => {
    expect(ids(local)).toEqual(['q1', 'q2', 'q3', 'q4', 'q5', 'q6', 'q7', 'q8', 'q9', 'a_role', 'a_team', 'a_views'])
  })

  it('swaps Q3 for the two in-cluster questions', () => {
    const got = ids(inCluster)
    expect(got).not.toContain('q3')
    expect(got.slice(2, 4)).toEqual(['q3c_people', 'q3c_front'])
  })

  it('adds the Q5 follow-ups for the branch picked', () => {
    expect(ids(local, { q5: { choices: ['tried'] } })).toEqual(expect.arrayContaining(['q5_agent', 'q5_useful']))
    expect(ids(local, { q5: { choices: ['tried'] } })).not.toContain('q5_blocker')
    expect(ids(local, { q5: { choices: ['not_setup'] } })).toContain('q5_blocker')
  })

  it('skips Q9 for people already on the hosted agent', () => {
    const answers = { q5: { choices: ['regularly'] }, q5_agent: { choices: ['radar_cloud'] } }
    expect(ids(local, answers)).not.toContain('q9')
  })
})

describe('pruneAnswers', () => {
  it('drops follow-ups stranded by changing Q5 after going Back', () => {
    const got = pruneAnswers(local, {
      q5: { choices: ['no_ai'] },
      q5_agent: { choices: ['codex'] },
      q5_useful: { choices: ['mixed'] },
    })
    expect(got).toEqual({ q5: { choices: ['no_ai'] } })
  })

  it('keeps "other" text only while "other" is picked, and always keeps Q6 reasons', () => {
    expect(pruneAnswers(local, { q1: { choices: ['debug'], text: 'stale' } })).toEqual({ q1: { choices: ['debug'] } })
    expect(pruneAnswers(local, { q1: { choices: ['other'], text: ' ops ' } })).toEqual({ q1: { choices: ['other'], text: 'ops' } })
    expect(pruneAnswers(local, { q6: { text: 'the timeline' } })).toEqual({ q6: { text: 'the timeline' } })
  })

  it('drops detection-gated Q8 options that were never offered', () => {
    expect(pruneAnswers(local, { q8: { choices: ['argo_diff', 'rbac'] } })).toEqual({ q8: { choices: ['rbac'] } })
    const withArgo = { ...local, available: { argocd: true, prometheus: false } }
    expect(offeredOptions(q('q8'), withArgo).map((o) => o.id)).toContain('argo_diff')
    expect(offeredOptions(q('q8'), withArgo).map((o) => o.id)).not.toContain('rightsize')
  })

  it('drops in-cluster answers on a local Radar', () => {
    expect(pruneAnswers(local, { q3c_people: { choices: ['two_five'] } })).toEqual({})
  })
})

describe('toggleChoice', () => {
  it('takes any number of uses in Q1', () => {
    expect(toggleChoice(q('q1'), ['debug', 'browse'], 'understand')).toEqual(['debug', 'browse', 'understand'])
  })
  it('keeps "None of these" exclusive in Q4', () => {
    expect(toggleChoice(q('q4'), ['screenshots'], 'none')).toEqual(['none'])
    expect(toggleChoice(q('q4'), ['none'], 'screenshots')).toEqual(['screenshots'])
  })
  it('toggles a single-choice answer off when clicked again', () => {
    expect(toggleChoice(q('q2'), ['one'], 'one')).toEqual([])
    expect(toggleChoice(q('q2'), ['one'], 'four_ten')).toEqual(['four_ten'])
  })
})

describe('closingLine', () => {
  it('thanks Cloud users first', () => {
    expect(closingLine({ q9: { choices: ['already'] }, q9x: {} }, true)).toBe('already_cloud')
    expect(closingLine({ q5_agent: { choices: ['radar_cloud'] } }, true)).toBe('already_cloud')
  })

  it('points hosted-service objections at Self-Managed ahead of investigations', () => {
    const answers = { q9: { choices: ['airgapped'] }, q5_blocker: { choices: ['no_agent'] } }
    expect(closingLine(answers, true)).toBe('self_managed')
  })

  it('offers hosted investigations to people blocked on agent setup', () => {
    expect(closingLine({ q5_blocker: { choices: ['no_api'] } }, true)).toBe('investigations')
    expect(closingLine({ q5_blocker: { choices: ['policy'] } }, true)).toBeNull()
  })

  it('never shows a Cloud line where the Cloud dialog is unavailable', () => {
    expect(closingLine({ q9: { choices: ['no_hosted'] } }, false)).toBeNull()
    expect(closingLine({ q5_blocker: { choices: ['no_agent'] } }, false)).toBeNull()
  })

  it('tells solo, small-footprint users OSS is the right fit', () => {
    expect(closingLine({ q4: { choices: ['none'] }, q2: { choices: ['two_three'] } }, true)).toBe('right_fit')
    expect(closingLine({ q4: { choices: ['none'] }, q2: { choices: ['four_ten'] } }, true)).toBeNull()
  })

  it('does not pitch Cloud off the team-problems answers', () => {
    expect(closingLine({ q4: { choices: ['screenshots', 'kubeconfig'] }, q2: { choices: ['more_ten'] } }, true)).toBeNull()
  })
})

describe('discovery links and ids', () => {
  it('links only the Q8 features picked', () => {
    expect(discoveryLinks({ q8: { choices: ['upgrade'] } })).toEqual([{ path: '/checks/upgrade', label: 'Open Upgrade impact' }])
  })
  it('makes submission ids the server accepts', () => {
    const id = newSubmissionId()
    expect(id).toMatch(/^[A-Za-z0-9-]{16,64}$/)
    expect(newSubmissionId()).not.toBe(id)
  })
})

describe('copy', () => {
  it('matches the settled spec wording', () => {
    expect(POLL_COPY.cardTitle).toBe('Got two minutes to help us improve Radar OSS?')
    expect(POLL_COPY.cardBody).toBe('A few questions about how you use it. Anonymous.')
    expect(POLL_COPY.star).toBe('If Radar saves you time, a star on GitHub helps other people find it.')
    expect(q('q9').description).toBe(
      'Radar Cloud keeps Radar working after you close the laptop: it alerts your team when something breaks, remembers what changed, and investigates the cause. Have you looked at it?',
    )
  })
  it('uses no em dashes anywhere', () => {
    const all = JSON.stringify(CURRENT_ROUND) + JSON.stringify(POLL_COPY)
    expect(all).not.toContain('—')
  })
  it('has no review ask', () => {
    expect(JSON.stringify(POLL_COPY).toLowerCase()).not.toContain('review')
  })
})
