import type { ResourceRef } from '../../types/core';
import { canonicalResourceGroup } from '../../utils/api-resources';
import { appRelationshipFields, type AppRow } from '../../utils/applications';
import { resourceKey } from '../../utils/resource-refs';
import type { Issue, IssueResourceRef } from './types';

export type IssueAppInventoryRow = Pick<AppRow, 'key' | 'name' | 'relationships'> & { workloads: readonly ResourceRef[] };
type Subject = Pick<Issue, 'grouping_scope' | 'kind' | 'group' | 'name' | 'namespace' | 'members' | 'members_truncated'>;

function refKey(ref: IssueResourceRef): string {
  return resourceKey(canonicalResourceGroup(ref.kind, ref.group) ?? '', ref.kind, ref.namespace ?? '', ref.name);
}

/** Resolve an issue against one cluster's inventory. A shared or uncertain match stays in the queue on its own. */
export function buildIssueAppIndex(apps: readonly IssueAppInventoryRow[], { complete = true } = {}) {
  const owners = new Map<string, Pick<AppRow, 'key' | 'name'> | null>();
  const workloadKeys = new Set<string>();
  const add = (ref: ResourceRef, { key, name }: IssueAppInventoryRow) => {
    const resource = refKey(ref);
    const previous = owners.get(resource);
    if (previous === undefined) owners.set(resource, { key, name });
    else if (previous?.key !== key) owners.set(resource, null);
    return resource;
  };
  for (const app of apps) {
    for (const workload of app.workloads) workloadKeys.add(add(workload, app));
  }

  let hasIncompleteRelationships = false;
  for (const [refsField, summaryField] of appRelationshipFields) {
    const incompleteApps = new Set<string>();
    for (const { key, relationships } of apps) {
      const refCount = relationships?.[refsField]?.length ?? 0;
      const summary = relationships?.[summaryField];
      // finalizeRelationships caps each ref list at 20. Services, Ingresses and
      // routes also cap their display names, so a full page cannot prove coverage.
      const hasTotal = typeof summary === 'number';
      const count = hasTotal ? summary : summary?.length ?? 0;
      if (count > refCount || (!hasTotal && refCount >= 20)) incompleteApps.add(key);
    }
    if (incompleteApps.size) hasIncompleteRelationships = true;
    for (const app of apps) {
      const otherAppMayReference = incompleteApps.size > 1 || (incompleteApps.size === 1 && !incompleteApps.has(app.key));
      for (const ref of app.relationships?.[refsField] ?? []) {
        const resource = add(ref, app);
        if (otherAppMayReference && !workloadKeys.has(resource)) owners.set(resource, null);
      }
    }
  }

  return (issue: Subject) => {
    if (issue.grouping_scope === 'node' || (issue.kind === 'Node' && !issue.group)) return undefined;
    const subjectKey = refKey(issue);
    let app = owners.get(subjectKey);
    if (app === null) return undefined;
    // If this subject itself could be an omitted reference, matching only its
    // visible issue members cannot rule out involvement of another application.
    if (!app && hasIncompleteRelationships) return undefined;
    // Radar folds a workload's Pods under their owner. A capped Pod list does
    // not change that attribution. Other subjects require the full membership.
    const workloadSubject = issue.grouping_scope === 'workload' && workloadKeys.has(subjectKey);
    if (!workloadSubject && (!complete || issue.members_truncated)) return undefined;
    for (const member of issue.members ?? []) {
      const memberApp = owners.get(refKey(member));
      if (memberApp === undefined && workloadSubject) continue;
      if (!memberApp || (app && app.key !== memberApp.key)) return undefined;
      app = memberApp;
    }
    return app;
  };
}
