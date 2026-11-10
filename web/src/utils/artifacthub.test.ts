import { describe, expect, it } from 'vitest'
import { artifactHubPublisher, isArtifactHubOfficial } from './artifacthub'

describe('ArtifactHub display metadata', () => {
  const repository = { name: 'repository', url: 'https://charts.example.test' }
  it('prefers the publisher display name and supports older responses', () => {
    expect(artifactHubPublisher({ ...repository, organizationDisplayName: 'Publisher Team', organizationName: 'publisher', userAlias: 'account' })).toBe('Publisher Team')
    expect(artifactHubPublisher({ ...repository, organizationName: 'publisher', userAlias: 'account' })).toBe('publisher')
    expect(artifactHubPublisher({ ...repository, userAlias: 'account' })).toBe('account')
    expect(artifactHubPublisher(repository)).toBe('repository')
  })
  it('recognizes either official scope without treating verified as official', () => {
    const chart = { packageId: 'id', name: 'chart', version: '1.0.0', stars: 0, repository }
    expect(isArtifactHubOfficial({ ...chart, official: true })).toBe(true)
    expect(isArtifactHubOfficial({ ...chart, repository: { ...repository, official: true } })).toBe(true)
    expect(isArtifactHubOfficial({ ...chart, repository: { ...repository, verifiedPublisher: true } })).toBe(false)
  })
})
