import { describe, expect, it } from 'vitest';
import { buildIssueAppIndex } from './issue-app-index';

const api = { kind: 'Deployment', namespace: 'shop', name: 'api' };
const worker = { kind: 'StatefulSet', namespace: 'shop', name: 'worker' };
const service = { kind: 'Service', namespace: 'shop', name: 'api' };
const settings = { kind: 'ConfigMap', namespace: 'shop', name: 'settings' };
const pod = { kind: 'Pod', namespace: 'shop', name: 'api-123' };
const apps = [
  { key: 'shop-api', name: 'API', workloads: [api], relationships: { serviceRefs: [service], configRefs: [settings] } },
  { key: 'shop-worker', name: 'Worker', workloads: [worker], relationships: { configRefs: [settings] } },
];
const application = { key: 'shop-api', name: 'API' };
const lookup = buildIssueAppIndex(apps);

describe('buildIssueAppIndex', () => {
  it('matches exact workload and relationship identities, including omitted built-in groups', () => {
    expect(lookup({ ...api, group: 'apps', grouping_scope: 'workload' })).toEqual(application);
    expect(lookup({ ...service, grouping_scope: 'service' })).toEqual(application);
    expect(lookup({ ...service, group: 'serving.knative.dev', grouping_scope: 'service' })).toBeUndefined();
    expect(lookup({ ...api, namespace: 'other', grouping_scope: 'workload' })).toBeUndefined();
  });

  it('keeps a known workload with its app even when folded Pod evidence is sampled', () => {
    const issue = { ...api, grouping_scope: 'workload', members: [pod], members_truncated: true };
    expect(lookup(issue)).toEqual(application);
    expect(buildIssueAppIndex(apps, { complete: false })(issue)).toEqual(application);
    expect(lookup({ ...issue, members: [worker] })).toBeUndefined();
  });

  it('leaves shared, unmatched, incomplete and node issues standalone', () => {
    expect(lookup({ ...settings, grouping_scope: 'unknown' })).toBeUndefined();
    expect(lookup({ ...service, grouping_scope: 'service', members: [pod] })).toBeUndefined();
    expect(lookup({ ...service, grouping_scope: 'service', members_truncated: true })).toBeUndefined();
    expect(buildIssueAppIndex(apps, { complete: false })({ ...service, grouping_scope: 'service' })).toBeUndefined();
    expect(lookup({ kind: 'Node', name: 'node-1', grouping_scope: 'node', members: [api] })).toBeUndefined();
  });

  it('matches all visible members only when every member resolves to the same application', () => {
    const grouped = { kind: 'Service', name: 'another', namespace: 'shop', grouping_scope: 'service' };
    expect(lookup({ ...grouped, members: [api, service] })).toEqual(application);
    expect(lookup({ ...grouped, members: [api, worker] })).toBeUndefined();
    expect(lookup({ ...grouped, members: [api, settings] })).toBeUndefined();
  });

  it('keeps a Service standalone when another app could also use it beyond its first 20 Services', () => {
    const serviceRefs = Array.from({ length: 20 }, (_, index) => ({ ...service, name: `svc-${index}` }));
    const shared = { ...service, name: 'zzz-service' };
    const first = { ...apps[0], relationships: { serviceRefs: [shared], configRefs: [settings] } };
    const other = { ...apps[1], relationships: { serviceRefs } };
    const match = buildIssueAppIndex([first, other]);
    expect(match({ ...shared, grouping_scope: 'service' })).toBeUndefined();
    expect(match({ ...api, grouping_scope: 'workload' })).toEqual(application);
    expect(match({ ...settings, grouping_scope: 'unknown' })).toEqual(application);
    expect(match({ ...serviceRefs[0], grouping_scope: 'service' })).toEqual({ key: other.key, name: other.name });
    expect(match({ ...service, name: 'zzz-missing', grouping_scope: 'service', members: [api] })).toBeUndefined();
  });

  it('uses the reported total to distinguish a complete 20 ConfigMaps from an incomplete list', () => {
    const configRefs = Array.from({ length: 20 }, (_, index) => ({ ...settings, name: `config-${index}` }));
    const matchWithCount = (configs: number) => buildIssueAppIndex([
      apps[0], { ...apps[1], relationships: { configs, configRefs } },
    ]);
    const issue = { ...settings, grouping_scope: 'unknown' };
    expect(matchWithCount(20)(issue)).toEqual(application);
    expect(matchWithCount(21)(issue)).toBeUndefined();
    expect(matchWithCount(21)({ ...service, grouping_scope: 'service' })).toEqual(application);
  });

  it('accepts repeated sightings of one app identity without mistaking them for shared ownership', () => {
    const repeated = buildIssueAppIndex([apps[0], apps[0]]);
    expect(repeated({ ...api, grouping_scope: 'workload' })).toEqual(application);
  });
});
