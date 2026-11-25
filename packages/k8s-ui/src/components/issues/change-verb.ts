import type { IssueRecentChange } from './types';

const IMAGE_PATH = /(\.image|\[image\])$/;
const WORKLOAD_CONFIG_PATH = /\.(env|envFrom)(\[|$)|\.(command|args)$|\.volumes(\[|$)/;
const CONFIG_DATA_PATH = /^(data|binaryData|stringData)(\.|\s|$)/;
// The only pod template field `kubectl rollout restart` writes.
const RESTARTED_AT_PATH = 'spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]';

function isHelmRow(change: IssueRecentChange): boolean {
  return change.source === 'helm' || (change.kind === 'HelmRelease' && !change.apiVersion);
}

function helmRevision(change: IssueRecentChange): number | undefined {
  const value = change.fields?.find((field) => field.path === 'helm.revision')?.newValue;
  return typeof value === 'number' ? value : undefined;
}

/** A short verb phrase for what happened to the changed object, read from the
 *  change type and the changed field paths. Field paths come from Radar's
 *  timeline diff, so they are the source of truth over the free-text summary. */
export function changeVerb(change: IssueRecentChange): string {
  if (isHelmRow(change)) {
    switch (change.changeType) {
      case 'upgrade_rolled_back':
        return 'Helm upgrade rolled back';
      case 'upgrade_failed':
        return 'Helm upgrade failed';
      case 'release_failed':
        return 'Helm release failed';
      case 'rollback':
        return 'Helm rolled back';
      case 'pending':
        return 'Helm operation stuck';
      case 'helm_release_revision':
        return helmRevision(change) === 1 ? 'Helm installed' : 'Helm upgraded';
      default:
        return 'Changed';
    }
  }
  if (change.changeType === 'delete') return 'Deleted';
  if (change.changeType === 'add') {
    // An add's fields don't mark a recreate: a new RBAC binding records what it
    // grants as fields too. Radar writes the "recreated with changes" summary
    // only for an add that replaced a just-deleted object; rank_reason says
    // "recreated" unless admission evidence replaced it.
    return change.summary?.startsWith('recreated with changes') || change.rank_reason?.startsWith('recreated') ? 'Recreated' : 'Created';
  }
  const paths = (change.fields ?? []).map((field) => field.path);
  if (paths.length === 0) return 'Changed';
  // ConfigMap and Secret keys are free-form, so a key named "image" is data, not a container image.
  const configKind = change.kind === 'ConfigMap' || change.kind === 'Secret';
  if (!configKind && paths.some((path) => IMAGE_PATH.test(path))) return 'Image changed';
  if (paths.every((path) => (configKind && CONFIG_DATA_PATH.test(path)) || WORKLOAD_CONFIG_PATH.test(path))) {
    return 'Config changed';
  }
  if (paths.every((path) => path === 'spec.replicas')) return 'Scaled';
  // A restart rolls new pods with nothing else changed; any other field in the
  // same change makes it an ordinary change.
  if (!configKind && paths.every((path) => path === RESTARTED_AT_PATH)) return 'Restarted';
  return 'Changed';
}

/** The verb as it reads mid-sentence on a collapsed row ("image changed",
 *  "Helm upgraded"): the first letter lower-cased, except for the name Helm. */
export function rowChangeVerb(change: IssueRecentChange): string {
  const verb = changeVerb(change);
  return verb.startsWith('Helm') ? verb : verb.charAt(0).toLowerCase() + verb.slice(1);
}
