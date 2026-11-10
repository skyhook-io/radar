import type { ArtifactHubChart, ArtifactHubRepository } from '../types'

export function artifactHubPublisher(repository: ArtifactHubRepository): string {
  return repository.organizationDisplayName || repository.organizationName || repository.userAlias || repository.name
}

export function isArtifactHubOfficial(chart: ArtifactHubChart): boolean {
  return Boolean(chart.official || chart.repository.official)
}
