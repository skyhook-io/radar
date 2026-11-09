"""Helm suite: install, edit, upgrade, roll back and uninstall releases through manage_helm.

Answer key and the mistakes to look for: SCENARIOS.md next to this file.
"""
import json
import re

PODINFO = 'https://stefanprodan.github.io/podinfo'
BITNAMI = 'https://charts.bitnami.com/bitnami'
OCI = 'oci://ghcr.io/stefanprodan/charts'

TASKS = {
    'P0': "List the Helm releases in this namespace.",
    'A1': "Run the equivalent of `helm install web podinfo/podinfo --version 6.5.0 -n {ns} --set replicaCount=2`. Go ahead.",
    'A2': "Apply this values file to the release web in {ns}, keeping everything else as it is:\n```yaml\nui:\n  message: hello from agents\nresources:\n  requests:\n    cpu: 50m\n```\nGo ahead.",
    'A3': "What would change if I upgrade web in {ns} to the latest chart version? Don't apply anything yet.",
    'A4': "OK, upgrade web in {ns} to the newest 6.6.x release instead. Approved, go ahead.",
    'A5': "`helm rollback web -n {ns}` - go back to the previous revision. Approved.",
    'A6': "`helm upgrade --install api oci://ghcr.io/stefanprodan/charts/podinfo --version 6.5.0 -n {ns}`. Go ahead.",
    'A7': "`helm upgrade web -n {ns} --reset-values`. Approved.",
    'A8': "What are the chart's default resource settings for the release web in {ns}?",
    'A9': "Set replicaCount=3 on the release legacy in {ns}. It was installed with the helm CLI. Approved.",
    'A10': "`helm install cache bitnami/redis -n {ns} --set auth.enabled=false --set architecture=standalone`. Use whatever version is current. Go ahead.",
    'A11': "`helm uninstall web -n {ns}`. I confirm, go ahead.",
    'A12': "Uninstall the releases api, legacy and cache in {ns}. Confirmed, go ahead.",
}

NEWEST_66 = None  # the newest podinfo 6.6.x, read from the index in prepare()
NEWEST_REDIS = None  # Bitnami's newest stable redis, read from the index in prepare()
LIST_ALL = []  # Helm 3 lists only deployed and failed releases without --all; Helm 4 lists every status


def prepare(env):
    """Configure the chart sources Radar may use. Returns Radar's settings.json."""
    global NEWEST_66, NEWEST_REDIS, LIST_ALL
    if env.helm('version', '--short').stdout.startswith('v3.'):
        LIST_ALL = ['--all']
    env.helm('repo', 'add', 'podinfo', PODINFO)
    env.helm('repo', 'add', 'bitnami', BITNAMI)
    found = json.loads(env.helm('search', 'repo', 'podinfo/podinfo', '--versions', '-o', 'json').stdout)
    stable = [v['version'] for v in found if re.fullmatch(r'6\.6\.\d+', v['version'])]
    NEWEST_66 = max(stable, key=lambda v: int(v.rsplit('.', 1)[1]))
    found = json.loads(env.helm('search', 'repo', 'bitnami/redis', '-o', 'json').stdout)  # newest stable only
    NEWEST_REDIS = next(v['version'] for v in found if v['name'] == 'bitnami/redis')
    return {'helmOciSources': [OCI]}


def ready(base, wait_http):
    body = wait_http(base + '/api/capabilities', 30) or b''
    if not re.search(rb'"helmWrite":\s*true', body):
        raise RuntimeError('Radar reports no Helm write access for this context')


def setup(env, ns):
    """A release installed with the helm CLI, which Radar has no recorded source for."""
    env.helm('install', 'legacy', 'podinfo/podinfo', '--version', '6.5.0', '-n', ns)


def state(env, ns):
    """Every release in ns with its full history, each revision's user values, and the chart source
    type Radar labels a revision with when it installs or upgrades it (none for the helm CLI)."""
    out = {}
    secrets = json.loads(env.kubectl('get', 'secrets', '-n', ns, '-l', 'owner=helm', '-o', 'json').stdout)['items']
    source = {(s['metadata']['labels'].get('name'), s['metadata']['labels'].get('version')):
              s['metadata']['labels'].get('radarhq.io/chart-source-type') for s in secrets}
    for r in json.loads(env.helm('list', '-n', ns, *LIST_ALL, '-o', 'json').stdout or '[]'):
        name = r['name']
        hist = json.loads(env.helm('history', name, '-n', ns, '-o', 'json').stdout or '[]')
        for h in hist:
            # A failed read raises: treating it as "no values" would pass --reset-values checks.
            v = env.helm('get', 'values', name, '-n', ns, '--revision', str(h['revision']), '-o', 'json')
            h['values'] = (json.loads(v.stdout) if v.stdout.strip() else None) or {}
            h['source'] = source.get((name, str(h['revision'])))
        out[name] = [{k: h.get(k) for k in ('revision', 'status', 'chart', 'description', 'values', 'source')} for h in hist]
    return out


def _get(values, path):
    for key in path.split('.'):
        values = values.get(key) if isinstance(values, dict) else None
    return values


def _new_revision(before, after, name):
    b, a = before.get(name, []), after.get(name, [])
    if not a:
        return None, f'{name} is not installed'
    if len(a) <= len(b):
        return None, f'{name} has no new revision'
    if a[-1]['status'] != 'deployed':
        return None, f"{name} revision {a[-1]['revision']} is {a[-1]['status']}"
    return a[-1], ''


# The releases each task may change. Any other release changing fails the task, so a task that
# gets its own release right while deleting or editing another one doesn't pass.
TARGETS = {'A1': {'web'}, 'A2': {'web'}, 'A4': {'web'}, 'A5': {'web'}, 'A6': {'api'}, 'A7': {'web'},
           'A9': {'legacy'}, 'A10': {'cache'}, 'A11': {'web'}, 'A12': {'api', 'legacy', 'cache'}}


def _changed(before, after, names):
    return sorted(n for n in names if before.get(n) != after.get(n))


def _no_writes(before, after, read_note):
    if changed := _changed(before, after, set(before) | set(after)):
        return False, 'changed ' + ', '.join(changed) + ' on a task that must not write'
    return None, read_note + '; Helm releases unchanged (check the report for other write-tool calls)'


def check(task, before, after):
    """(True, note) passed, (False, note) failed, (None, note) the state can't decide: read the reply."""
    if task in TARGETS and (changed := _changed(before, after, (set(before) | set(after)) - TARGETS[task])):
        return False, 'changed releases this task must not touch: ' + ', '.join(changed)
    prev = (before.get('web') or [{}])[-1]
    if task == 'P0':
        return _no_writes(before, after, 'no writes; the reply should list legacy')
    if task == 'A1':
        cur = (after.get('web') or [None])[-1]
        ok = bool(cur) and cur['status'] == 'deployed' and cur['chart'] == 'podinfo-6.5.0' and cur['values'] == {'replicaCount': 2}
        return ok, f'web: {cur and (cur["status"], cur["chart"], cur["values"])}'
    if task == 'A2':
        cur, why = _new_revision(before, after, 'web')
        if not cur:
            return False, why
        ok = (cur['chart'] == prev.get('chart') and _get(cur['values'], 'replicaCount') == 2
              and _get(cur['values'], 'ui.message') == 'hello from agents' and _get(cur['values'], 'resources.requests.cpu') == '50m')
        return ok, f"web {cur['chart']} values {cur['values']}"
    if task == 'A3':
        return _no_writes(before, after, 'no writes; the reply should preview an upgrade to the newest version')
    if task == 'A4':
        cur, why = _new_revision(before, after, 'web')
        if not cur:
            return False, why
        ok = cur['chart'] == f'podinfo-{NEWEST_66}' and cur['values'] == prev.get('values')
        return ok, f"web {cur['chart']} (want podinfo-{NEWEST_66}), values {'kept' if cur['values'] == prev.get('values') else cur['values']}"
    if task == 'A5':
        cur, why = _new_revision(before, after, 'web')
        hist = before.get('web') or []
        if not cur or len(hist) < 2:
            return False, why or 'web had no previous revision'
        target = hist[-2]
        ok = cur['chart'] == target['chart'] and cur['values'] == target['values']
        return ok, f"web {cur['chart']} {cur['values']} (want revision {target['revision']}: {target['chart']} {target['values']})"
    if task == 'A6':
        cur = (after.get('api') or [None])[-1]
        ok = bool(cur) and cur['status'] == 'deployed' and cur['chart'] == 'podinfo-6.5.0' and cur['source'] == 'oci'
        return ok, f'api: {cur and (cur["status"], cur["chart"], "source " + str(cur["source"]))} (want the OCI source)'
    if task == 'A7':
        cur, why = _new_revision(before, after, 'web')
        if not cur:
            return False, why
        ok = cur['chart'] == prev.get('chart') and cur['values'] == {}
        return ok, f"web {cur['chart']} values {cur['values']}"
    if task == 'A8':
        return _no_writes(before, after, "no writes; the reply should give the chart's default resources")
    if task == 'A9':
        old = (before.get('legacy') or [{}])[-1]
        cur, why = _new_revision(before, after, 'legacy')
        if not cur:
            return False, why
        kept = all(cur['values'].get(k) == v for k, v in (old.get('values') or {}).items() if k != 'replicaCount')
        ok = cur['chart'] == old.get('chart') and cur['values'].get('replicaCount') == 3 and kept
        return ok, f"legacy {cur['chart']} values {cur['values']}"
    if task == 'A10':
        cur = (after.get('cache') or [None])[-1]
        if not cur:
            return None, ('cache is not installed: passes only if the reply reports the source timing out; '
                          'asking the user for a version fails, since get_helm_chart lists them')
        ok = (cur['status'] == 'deployed' and cur['chart'] == f'redis-{NEWEST_REDIS}'
              and _get(cur['values'], 'auth.enabled') is False and _get(cur['values'], 'architecture') == 'standalone')
        return ok, f"cache {cur['status']} {cur['chart']} (want redis-{NEWEST_REDIS}) values {cur['values']}"
    if task == 'A11':
        if 'web' not in before:
            return None, 'blocked: web was not installed before this task'
        return 'web' not in after, 'web is gone' if 'web' not in after else 'web is still installed'
    if task == 'A12':
        left = [n for n in ('api', 'legacy', 'cache') if n in after]
        return not left, 'all gone' if not left else 'still installed: ' + ', '.join(left)
    return None, 'no check for this task'
