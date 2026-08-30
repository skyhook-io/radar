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
  const coverage = issue.onset_coverage;
  const unknown = coverage?.unknown ?? 0;
  const base = `Active at least since ${new Date(issue.first_seen).toLocaleString()}`;
  if (unknown === 0) return `${base}.`;
  const total = (coverage?.known ?? 0) + unknown;
  return `${base}; timing unknown for ${unknown} of ${total} signals.`;
}

export function issueResourceCreatedTitle(issue: Issue): string | null {
  if (!issue.resource_created_at) return null;
  const affectedCount = Math.max(issue.count ?? 0, issue.members?.length ?? 0);
  const label = affectedCount > 1
    ? 'Oldest affected resource created'
    : affectedCount === 1
      ? 'Affected resource created'
      : 'Resource created';
  return `${label} ${new Date(issue.resource_created_at).toLocaleString()}`;
}

function isDeploymentLikeCreation(issue: Issue): boolean {
  const group = issue.group ?? '';
  if (issue.kind === 'Pod') {
    return issue.issue_timing_basis === 'pod_creation'
      || issue.issue_timing_basis === 'owner_condition';
  }
  return isDeploymentLikeWorkloadKind(issue.kind, group);
}

function issueTimingIndependentOfOnset(issue: Issue): boolean {
  return issue.issue_timing_basis === 'owner_condition'
    || issue.issue_timing_basis === 'pod_creation'
    || issue.issue_timing_basis === 'spec';
}

function onsetUncertaintyMeta(issue: Issue): string {
  const coverage = issue.onset_coverage;
  const unknown = coverage?.unknown ?? 0;
  if (issue.first_seen && (coverage?.known ?? 0) > 0 && unknown > 0) {
    return `exact onset unknown for ${unknown} of ${(coverage?.known ?? 0) + unknown} signals`;
  }
  return 'exact onset unknown';
}

function onsetUncertaintyTooltip(issue: Issue): string {
  const coverage = issue.onset_coverage;
  const unknown = coverage?.unknown ?? 0;
  if (issue.first_seen && (coverage?.known ?? 0) > 0 && unknown > 0) {
    return `Exact onset is unavailable for ${unknown} of ${(coverage?.known ?? 0) + unknown} signals.`;
  }
  return 'Exact onset of this issue is unavailable.';
}

export function issueTimingForDisplay(issue: Issue): IssueTimingDisplay | null {
  const partialOnset = Boolean(issue.first_seen && (issue.onset_coverage?.unknown ?? 0) > 0);
  if (partialOnset && !issueTimingIndependentOfOnset(issue)) return null;
  return issueTiming(issue);
}

export function issueTiming(issue: Issue): IssueTimingDisplay | null {
  const coverage = issue.onset_coverage;
  const onsetUnknown = issue.onset_unknown || (coverage?.unknown ?? 0) > 0;
  const mixedOnset = Boolean(issue.first_seen && (coverage?.known ?? 0) > 0 && (coverage?.unknown ?? 0) > 0);
  const grouped = (coverage?.known ?? 0) + (coverage?.unknown ?? 0) > 1;
  const uncertaintyMeta = onsetUncertaintyMeta(issue);
  const uncertaintyTooltip = onsetUncertaintyTooltip(issue);
  switch (issue.issue_timing) {
    case 'started_after_resource_was_healthy': {
      if (issue.issue_timing_basis === 'owner_condition') {
        if (onsetUnknown) {
          return {
            kind: 'regression',
            chip: mixedOnset ? 'health regressed' : 'workload regressed · onset unknown',
            meta: `${uncertaintyMeta}; owner workload was healthy before its current health regression`,
            tooltip: `The owner workload had a healthy period before its current failing condition. ${uncertaintyTooltip}`,
          };
        }
        return {
          kind: 'regression',
          chip: 'health regressed',
          meta: 'workload health regressed',
          tooltip: 'The owner workload had a healthy period before its current failing condition. This does not date or attribute this specific issue.',
        };
      }
      if (onsetUnknown && issue.issue_timing_basis === 'pod_creation') {
        if (grouped) {
          return {
            kind: 'regression',
            chip: 'new pods failed after healthy',
            meta: `${uncertaintyMeta}; failing pods were created after an earlier healthy period`,
            tooltip: `Failing pods were created after an earlier healthy period. ${uncertaintyTooltip}`,
          };
        }
        return {
          kind: 'regression',
          chip: 'later rollout regressed',
          meta: `${uncertaintyMeta}; affected pod belongs to a later rollout after an earlier healthy period`,
          tooltip: `The affected pod belongs to a later workload revision after an earlier healthy period. ${uncertaintyTooltip}`,
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
      if (onsetUnknown && issue.issue_timing_basis === 'owner_condition') {
        return {
          kind: 'creation',
          chip: 'workload never healthy',
          meta: `${uncertaintyMeta}; owner workload never became healthy after deployment`,
          tooltip: `The owner workload never became healthy after deployment. ${uncertaintyTooltip}`,
        };
      }
      if (onsetUnknown && issue.issue_timing_basis === 'pod_creation') {
        if (grouped) {
          return {
            kind: 'creation',
            chip: 'startup pod failures',
            meta: `${uncertaintyMeta}; failures occurred in pods created during workload startup`,
            tooltip: `Failures occurred in pods created during workload startup. ${uncertaintyTooltip}`,
          };
        }
        return {
          kind: 'creation',
          chip: 'pod failed at startup',
          meta: `${uncertaintyMeta}; affected pod failed during workload startup`,
          tooltip: `The affected pod failed during startup of its workload revision. ${uncertaintyTooltip}`,
        };
      }
      if (onsetUnknown && issue.issue_timing_basis === 'spec') {
        return {
          kind: 'creation',
          chip: 'invalid at first reconcile',
          meta: `${uncertaintyMeta}; initial spec was failing from first reconciliation`,
          tooltip: `The initial specification establishes that the resource was failing from its first reconciliation. ${uncertaintyTooltip}`,
        };
      }
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
