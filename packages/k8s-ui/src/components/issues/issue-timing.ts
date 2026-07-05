import { formatRelativeAgeTime } from '../../utils/format';
import { isDeploymentLikeWorkloadKind } from '../../types';
import type { Issue } from './types';

export type IssueTimingDisplayKind = 'creation' | 'regression';

export interface IssueTimingDisplay {
  kind: IssueTimingDisplayKind;
  chip: string;
  meta: string;
  tooltip: string;
}

function isDeploymentLikeCreation(issue: Issue): boolean {
  const group = issue.group ?? '';
  if (issue.kind === 'Pod') {
    return issue.issue_timing_basis === 'pod_creation'
      || issue.issue_timing_basis === 'owner_condition';
  }
  return isDeploymentLikeWorkloadKind(issue.kind, group);
}

export function issueTiming(issue: Issue): IssueTimingDisplay | null {
  switch (issue.issue_timing) {
    case 'started_after_resource_was_healthy': {
      const started = issue.first_seen ? `started ${formatRelativeAgeTime(issue.first_seen)} after being healthy` : 'started after being healthy';
      return {
        kind: 'regression',
        chip: 'after healthy',
        meta: started,
        tooltip: 'Previously healthy before this failing signal.',
      };
    }
    case 'started_at_resource_creation': {
      if (isDeploymentLikeCreation(issue)) {
        return {
          kind: 'creation',
          chip: 'since deploy',
          meta: 'present since deployment or first reconciliation',
          tooltip: 'Failing signal began during deployment or first reconciliation.',
        };
      }
      return {
        kind: 'creation',
        chip: 'since creation',
        meta: 'present since creation or first reconciliation',
        tooltip: 'Failing signal began during resource creation or first reconciliation.',
      };
    }
    default:
      return null;
  }
}
