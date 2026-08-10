import { isDeploymentLikeWorkloadKind } from '../../types';
import type { Issue } from './types';

export type IssueTimingDisplayKind = 'creation' | 'regression';

export interface IssueTimingDisplay {
  kind: IssueTimingDisplayKind;
  chip: string;
  meta: string;
  tooltip: string;
}

export function issueFirstSeenTitle(issue: Issue): string | null {
  if (!issue.first_seen) return null;
  const unknown = issue.onset_coverage?.unknown ?? 0;
  const base = `Active at least since ${new Date(issue.first_seen).toLocaleString()}`;
  if (unknown === 0) return `${base}.`;
  return `${base}; timing unknown for ${unknown} contributing ${unknown === 1 ? 'signal' : 'signals'}.`;
}

export function partialIssueOnsetTitle(issue: Issue): string | null {
  const unknown = issue.onset_coverage?.unknown ?? 0;
  if (unknown === 0) return null;
  return issueFirstSeenTitle(issue);
}

export function issueResourceCreatedTitle(issue: Issue): string | null {
  if (!issue.resource_created_at) return null;
  const grouped = (issue.count ?? 0) > 1 || (issue.members?.length ?? 0) > 1;
  const label = grouped ? 'Oldest affected resource created' : 'Resource created';
  return `${label} ${new Date(issue.resource_created_at).toLocaleString()}`;
}

export function issueOnsetUnknownTitle(issue: Issue): string {
  const coverage = issue.onset_coverage;
  const signals = (coverage?.known ?? 0) + (coverage?.unknown ?? 0);
  const grouped = signals > 1;
  const base = grouped
    ? `Radar can confirm this issue is active, but current Kubernetes state does not reveal when any of its ${signals} contributing signals began.`
    : 'Radar can confirm this issue is active, but current Kubernetes state does not reveal when it began.';
  const resourceContext = issueResourceCreatedTitle(issue);
  return resourceContext ? `${base}\n${resourceContext}.` : base;
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
      if (issue.issue_timing_basis === 'owner_condition') {
        return {
          kind: 'regression',
          chip: 'health regressed',
          meta: 'workload health regressed',
          tooltip: 'The owner workload had a healthy period before its current failing condition. This does not date or attribute this specific issue.',
        };
      }
      return {
        kind: 'regression',
        chip: 'after healthy',
        meta: 'failing evidence followed a healthy period',
        tooltip: 'A healthy period preceded this failing evidence.',
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
