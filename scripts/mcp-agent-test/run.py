#!/usr/bin/env python3
"""Run AI agents against Radar's MCP server on a real cluster, then check what they did.

Each agent gets only Radar's MCP: no shell, no kubeconfig. It works through a
suite's tasks in order, in its own namespace, and after every task the runner
records the cluster state itself instead of trusting the agent's summary.
See README.md.
"""
import argparse
import importlib.util
import json
import os
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
from pathlib import Path

sys.dont_write_bytecode = True

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
DEFAULT_AGENTS = 'claude:claude-opus-5-5,claude:claude-sonnet-5-5,codex:gpt-6.1-sol'
KINDS = ('claude', 'claude-deferred', 'codex')
# Cloud CLIs that kubeconfig exec plugins call read these from HOME, which the
# runner replaces for Radar so the developer's ~/.radar settings stay out.
CREDENTIAL_DIRS = ('.aws', '.config/gcloud', '.azure', '.kube/cache')
TASK_TIMEOUT = 900
# Codex features that give an agent something besides Radar's MCP: a shell, ChatGPT apps, browsers,
# plugins. code_mode_host stays on: Codex makes its MCP calls through it.
CODEX_FEATURES_OFF = ('shell_tool', 'unified_exec', 'apps', 'multi_agent', 'image_generation', 'browser_use',
                      'browser_use_external', 'computer_use', 'in_app_browser', 'plugins', 'goals')

PREFIX = ("You are an assistant operating a Kubernetes cluster for a user. Your only access to the cluster is the "
          "Radar MCP server's tools. Work in namespace {ns}. Do the task, then reply with a short summary of what "
          "you did and the result.\n\nUser: ")


class Env:
    """The cluster and Helm configuration the runner and suites use. Agents never see it."""

    def __init__(self, work, kubeconfig):
        self.work, self.kubeconfig = work, kubeconfig
        h = work / 'helm'
        self.helm_env = {
            'HELM_REPOSITORY_CONFIG': h / 'repositories.yaml', 'HELM_REPOSITORY_CACHE': h / 'cache',
            'HELM_REGISTRY_CONFIG': h / 'registry.json', 'HELM_CONFIG_HOME': h / 'config',
            'HELM_CACHE_HOME': h / 'cachehome', 'HELM_DATA_HOME': h / 'data',
        }

    def environ(self):
        # Inherited HELM_* settings (HELM_KUBECONTEXT, HELM_KUBETOKEN, ...) would override --kubeconfig.
        inherited = {k: v for k, v in os.environ.items() if not k.startswith('HELM_')}
        return {**inherited, **{k: str(v) for k, v in self.helm_env.items()}}

    def run(self, *cmd, check=True, timeout=300):
        p = subprocess.run(cmd, capture_output=True, text=True, env=self.environ(), stdin=subprocess.DEVNULL,
                           timeout=timeout)
        if check and p.returncode:
            raise RuntimeError(f"{' '.join(cmd)}: {p.stderr.strip() or p.stdout.strip()}")
        return p

    def helm(self, *args, check=True, timeout=300):
        return self.run('helm', *args, '--kubeconfig', str(self.kubeconfig), check=check, timeout=timeout)

    def kubectl(self, *args, check=True, timeout=300):
        return self.run('kubectl', '--kubeconfig', str(self.kubeconfig), *args, check=check, timeout=timeout)


def load_suite(name):
    path = HERE / 'suites' / name / 'suite.py'
    if not path.exists():
        sys.exit(f'no suite {name!r}; have: {", ".join(p.name for p in (HERE / "suites").iterdir() if p.is_dir())}')
    spec = importlib.util.spec_from_file_location(f'suite_{name}', path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def parse_agents(spec):
    agents = []
    for item in spec.split(','):
        kind, _, model = item.partition(':')
        if kind not in KINDS or not model:
            sys.exit(f'bad agent {item!r}: use kind:model with kind one of {", ".join(KINDS)}')
        tag = re.sub(r'[^a-z0-9]+', '-', model.lower()).strip('-') + ('-d' if kind == 'claude-deferred' else '')
        if any(tag == t for t, _, _ in agents):
            sys.exit(f'agent {item!r} appears twice')
        agents.append((tag, kind, model))
    return agents


def agent_command(kind, model, prompt, mcp_url, mcp_config, empty_dir):
    if kind in ('claude', 'claude-deferred'):
        # claude-deferred keeps ToolSearch, so Radar's tools load on demand (names only up front) as in
        # default Claude Code. Plain claude gets no built-in tools, so every Radar tool loads up front.
        return ['claude', '-p', prompt, '--model', model, '--mcp-config', str(mcp_config), '--strict-mcp-config',
                '--tools', 'ToolSearch' if kind == 'claude-deferred' else '', '--allowedTools', 'mcp__radar',
                '--output-format', 'stream-json', '--verbose', '--no-session-persistence', '--setting-sources', '']
    off = [arg for f in CODEX_FEATURES_OFF for arg in ('-c', f'features.{f}=false')]
    return ['codex', 'exec', '-m', model, '--json', '-s', 'read-only', '--skip-git-repo-check', '--ephemeral',
            '--ignore-user-config', '-C', str(empty_dir), *off, '-c', 'web_search="disabled"',
            '-c', f'mcp_servers.radar.url="{mcp_url}"', '-c', 'approval_policy="never"',
            '-c', 'mcp_servers.radar.default_tools_approval_mode="approve"', prompt]


def parse_transcript(path):
    """Tool calls, the final reply and any CLI failure from a claude stream-json or codex --json transcript.

    A call marked foreign used something other than Radar's MCP (ToolSearch, which only loads Radar's
    tools, is allowed).
    """
    calls, final, failure, pending = [], '', '', {}
    for line in open(path, errors='replace'):
        try:
            e = json.loads(line)
        except ValueError:
            continue
        t = e.get('type')
        if t == 'assistant':
            for c in e['message']['content']:
                if c.get('type') == 'tool_use':
                    foreign = not c['name'].startswith('mcp__radar__') and c['name'] != 'ToolSearch'
                    rec = {'tool': c['name'].replace('mcp__radar__', ''), 'args': c['input'], 'err': False, 'out': '',
                           'foreign': foreign}
                    pending[c['id']] = rec
                    calls.append(rec)
        elif t == 'user':
            for c in e['message']['content']:
                if isinstance(c, dict) and c.get('type') == 'tool_result' and c['tool_use_id'] in pending:
                    txt = c['content'] if isinstance(c['content'], str) else ' '.join(x.get('text', '') for x in c['content'])
                    pending[c['tool_use_id']].update(err=bool(c.get('is_error')), out=txt)
        elif t == 'result':
            final = e.get('result') or ''
            if e.get('is_error') or e.get('subtype', 'success') != 'success':
                failure = f"claude: {e.get('subtype')}: {final[:300]}"
        elif t == 'turn.failed':
            failure = 'codex: ' + str((e.get('error') or {}).get('message', e))[:300]
        elif t == 'item.completed' and e['item']['type'] not in ('mcp_tool_call', 'agent_message', 'reasoning', 'error', 'todo_list'):
            it = e['item']
            calls.append({'tool': it['type'], 'args': {'command': it.get('command')} if 'command' in it else {},
                          'err': False, 'out': '', 'foreign': True})
        elif t == 'item.completed' and e['item']['type'] == 'mcp_tool_call':
            it, res = e['item'], e['item'].get('result') or {}
            txt = ' '.join(x.get('text', '') for x in (res.get('content') or []))
            err = bool(it.get('error')) or it.get('status') == 'failed' or bool(res.get('isError') or res.get('is_error'))
            msg = it['error'].get('message', '') if isinstance(it.get('error'), dict) else str(it.get('error') or '')
            calls.append({'tool': it['tool'], 'args': it['arguments'], 'err': err, 'out': msg or txt,
                          'foreign': it.get('server') != 'radar'})
        elif t == 'item.completed' and e['item']['type'] == 'agent_message':
            final = e['item']['text']
    return calls, final, failure


def short_args(args):
    a = {k: v for k, v in (args or {}).items() if k != 'namespace'}
    if 'confirm' in a:
        a['confirm'] = '<token>'
    return json.dumps(a, separators=(',', ':'))[:220]


def report(out):
    """Write report.md for a run directory and print the verdict table."""
    namespaces_file = out / 'namespaces.json'
    own = json.loads(namespaces_file.read_text()) if namespaces_file.exists() else {}
    tools_file = out / 'tools.json'
    write_tools = set(json.loads(tools_file.read_text())['write']) if tools_file.exists() else set()
    path = out / 'results.jsonl'
    if not path.exists():
        print(f'no results in {out}')
        return
    results = [json.loads(l) for l in path.read_text().splitlines() if l.strip()]
    lines, table = [], {}
    for r in results:
        transcript = out / r['agent'] / f"{r['task']}.jsonl"
        calls, final, failure = parse_transcript(transcript) if transcript.exists() else ([], '', '')
        errors = sum(1 for c in calls if c['err'])
        foreign = sum(1 for c in calls if c['foreign'])
        table.setdefault(r['agent'], {})[r['task']] = (r['verdict'], errors, foreign)
        lines.append(f"### {r['agent']} {r['task']}: {r['verdict']} ({len(calls)} calls, {errors} errors, {r['seconds']}s)")
        lines.append(f"check: {r['note']}")
        if failure:
            lines.append(f"agent CLI failure: {failure}")
        for c in calls:
            mark = 'NOT RADAR' if c['foreign'] else 'ERR' if c['err'] else 'ok '
            write = ' (write tool)' if c['tool'] in write_tools else ''
            ns = (c['args'] or {}).get('namespace')
            elsewhere = f' OTHER NAMESPACE {ns}' if ns and own.get(r['agent']) and ns != own[r['agent']] else ''
            lines.append(f"- {mark} `{c['tool']}`{write}{elsewhere} {short_args(c['args'])}")
            if c['err']:
                lines.append(f"  -> {c['out'][:400].replace(chr(10), ' ')}")
        lines.append(f"\nreply: {final.strip()[:1200]}\n")
    tasks = list(dict.fromkeys(r['task'] for r in results))
    head = '| agent | ' + ' | '.join(tasks) + ' |\n|---|' + '---|' * len(tasks)
    def cell(t, task):
        if task not in t:
            return '-'
        verdict, errors, foreign = t[task]
        notes = [f'{errors} err'] * bool(errors) + [f'{foreign} NOT RADAR'] * bool(foreign)
        return verdict + (f" ({', '.join(notes)})" if notes else '')
    rows = [f'| {a} | ' + ' | '.join(cell(t, task) for task in tasks) + ' |' for a, t in table.items()]
    summary = head + '\n' + '\n'.join(rows)
    (out / 'report.md').write_text('# Agent test report\n\n' + summary + '\n\nREAD means the state check can\'t '
                                   'decide: read the reply against the answer key. ERROR means the agent CLI or the '
                                   'runner failed, so that agent stopped. NOT RADAR counts calls to something other '
                                   'than Radar\'s MCP, which the test is meant to rule out. OTHER NAMESPACE marks a call outside the '
                                   'agent\'s own namespace, which the state checks don\'t see. Calls to tools Radar '
                                   'marks as writes are labelled, so a READ task can be checked for side effects.\n\n' + '\n'.join(lines) + '\n')
    print('\n' + summary + f'\n\nfull report: {out / "report.md"}')


def wait_http(url, seconds):
    deadline = time.time() + seconds
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=5) as r:
                if r.status == 200:
                    return r.read()
        except OSError:
            pass
        time.sleep(1)
    return None


def mcp_tools(url):
    """Radar's MCP tool list, split by its readOnlyHint annotation."""
    req = urllib.request.Request(url, data=json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list', 'params': {}}).encode(),
                                 headers={'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream',
                                          'MCP-Protocol-Version': '2025-11-25'})
    raw = urllib.request.urlopen(req, timeout=30).read().decode()
    if not raw.startswith('{'):
        raw = next(l[6:] for l in raw.splitlines() if l.startswith('data: '))
    tools = json.loads(raw)['result']['tools']
    return {'read': sorted(t['name'] for t in tools if (t.get('annotations') or {}).get('readOnlyHint')),
            'write': sorted(t['name'] for t in tools if not (t.get('annotations') or {}).get('readOnlyHint'))}


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def stop(p, grace=10):
    """Stop a process started in its own session, with everything it spawned."""
    if p.poll() is None:
        try:
            os.killpg(p.pid, signal.SIGTERM)
            p.wait(timeout=grace)
        except (ProcessLookupError, subprocess.TimeoutExpired):
            try:
                os.killpg(p.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            p.wait()


class AgentFailed(Exception):
    """The agent CLI failed or timed out, so the cluster state after it can't be trusted for later tasks."""


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--suite', default='helm')
    ap.add_argument('--context', help='kubeconfig context to test against (default: the current one)')
    ap.add_argument('--agents', default=DEFAULT_AGENTS, help=f'comma-separated kind:model, kinds {", ".join(KINDS)} (default %(default)s)')
    ap.add_argument('--tasks', help='comma-separated task ids, in order (default: all). Later tasks build on earlier ones.')
    ap.add_argument('--radar-bin', help='use this Radar binary instead of building ./cmd/explorer')
    ap.add_argument('--port', type=int, help="Radar's port (default: a free one)")
    ap.add_argument('--out', help='directory for transcripts and the report (default: a new temp directory)')
    ap.add_argument('--keep', action='store_true', help='keep the test namespaces for inspection')
    ap.add_argument('--yes', action='store_true', help='skip the confirmation prompt')
    ap.add_argument('--report', metavar='DIR', help='only rebuild the report of an earlier run')
    a = ap.parse_args()

    if a.report:
        return report(Path(a.report))

    suite = load_suite(a.suite)
    agents = parse_agents(a.agents)
    tasks = a.tasks.split(',') if a.tasks else list(suite.TASKS)
    if unknown := [t for t in tasks if t not in suite.TASKS]:
        sys.exit(f'unknown tasks for suite {a.suite}: {", ".join(unknown)}')
    needed = {'kubectl', 'helm'} | {'claude' if k.startswith('claude') else 'codex' for _, k, _ in agents} | ({'go'} if not a.radar_bin else set())
    if missing := [b for b in sorted(needed) if not shutil.which(b)]:
        sys.exit(f'missing on PATH: {", ".join(missing)}')
    port = a.port or free_port()
    base = f'http://127.0.0.1:{port}'
    if wait_http(base + '/api/health', 1) is not None:
        sys.exit(f'something already answers on port {port}; pass another --port')

    context = a.context or subprocess.run(['kubectl', 'config', 'current-context'], capture_output=True, text=True).stdout.strip()
    run_id = time.strftime('%m%d%H%M') + secrets.token_hex(2)
    prefix_len = 63 - len(run_id) - 1
    namespaces = {tag: f'radar-agent-{a.suite}-{tag}'[:prefix_len].rstrip('-') + '-' + run_id for tag, _, _ in agents}
    if len(set(namespaces.values())) < len(namespaces):
        sys.exit('two agents shorten to the same namespace name; use models with more distinct ids')
    print(f'context:    {context}\nsuite:      {a.suite} ({", ".join(tasks)})\nagents:     {", ".join(f"{t} ({k})" for t, k, _ in agents)}')
    print('namespaces: ' + ', '.join(namespaces.values()))
    print("Agents act with this context's identity through Radar. Use a test cluster.")
    if not a.yes and input('Continue? [y/N] ').strip().lower() != 'y':
        sys.exit('aborted')

    out = Path(a.out) if a.out else Path(tempfile.mkdtemp(prefix=f'radar-agent-test-{run_id}-'))
    out.mkdir(parents=True, exist_ok=True)
    print(f'run directory: {out}')
    (out / 'namespaces.json').write_text(json.dumps(namespaces, indent=1))
    work = Path(tempfile.mkdtemp(prefix='radar-agent-work-'))  # holds credentials; removed however the run ends
    env = Env(work, work / 'kubeconfig')
    radar, attempted, children, threads = None, [], [], []
    stopping, spawn_lock, record_lock = threading.Event(), threading.Lock(), threading.Lock()
    try:
        try:
            kc = subprocess.run(['kubectl', 'config', 'view', '--minify', '--flatten', '--context', context],
                                capture_output=True, text=True, check=True).stdout
            env.kubeconfig.write_text(kc)
            env.kubeconfig.chmod(0o600)
            home, real_home = work / 'home', Path.home()
            for rel in CREDENTIAL_DIRS:
                if (real_home / rel).exists():
                    (home / rel).parent.mkdir(parents=True, exist_ok=True)
                    (home / rel).symlink_to(real_home / rel)
            (home / '.radar').mkdir(parents=True, exist_ok=True)
            print('preparing suite sources...')
            (home / '.radar' / 'settings.json').write_text(json.dumps(suite.prepare(env)))

            binary = a.radar_bin
            if not binary:
                print('building Radar (backend only)...')
                binary = str(work / 'radar')
                subprocess.run(['go', 'build', '-o', binary, './cmd/explorer'], cwd=REPO, check=True)
            log = open(out / 'radar.log', 'w')
            radar = subprocess.Popen([binary, '--kubeconfig', str(env.kubeconfig), '--port', str(port), '--no-browser',
                                      '--disable-auto-port-forward', *getattr(suite, 'RADAR_FLAGS', [])],
                                     env={**env.environ(), 'HOME': str(home)}, stdout=log, stderr=subprocess.STDOUT,
                                     start_new_session=True)
            # Radar logs each request it serves, so finding this run's probe in its own log proves the
            # answer came from it and not from another process that took the port.
            probe, deadline = f'/api/health?radar-agent-test={run_id}', time.time() + 120
            while run_id not in (out / 'radar.log').read_text(errors='replace'):
                if radar.poll() is not None or time.time() > deadline:
                    raise RuntimeError(f'Radar did not start on port {port}; see {out / "radar.log"}')
                wait_http(base + probe, 1)
            if hasattr(suite, 'ready'):
                suite.ready(base, wait_http)
            mcp_url = base + '/mcp'
            (out / 'tools.json').write_text(json.dumps(mcp_tools(mcp_url), indent=1))

            for tag, ns in namespaces.items():
                attempted.append(ns)
                env.kubectl('create', 'namespace', ns)
                env.kubectl('label', 'namespace', ns, f'radar-agent-test={run_id}')
                suite.setup(env, ns)

            mcp_config = work / 'mcp.json'
            mcp_config.write_text(json.dumps({'mcpServers': {'radar': {'type': 'http', 'url': mcp_url}}}))
            empty = work / 'cwd'
            empty.mkdir()
            agent_env = {k: v for k, v in os.environ.items() if not k.startswith(('HELM_', 'KUBE'))}
            agent_env['KUBECONFIG'] = os.devnull

            def record(rec):
                with record_lock:
                    with open(out / 'results.jsonl', 'a') as f:
                        f.write(json.dumps(rec) + '\n')
                    print(f"{rec['agent']} {rec['task']}: {rec['verdict']} ({rec['seconds']}s) {rec['note']}", flush=True)

            def run_task(tag, kind, model, tid, before):
                ns, d = namespaces[tag], out / tag
                prompt = PREFIX.format(ns=ns) + suite.TASKS[tid].format(ns=ns)
                started, timed_out = time.time(), False
                with open(d / f'{tid}.jsonl', 'w') as f:
                    with spawn_lock:
                        if stopping.is_set():
                            raise AgentFailed('run stopped')
                        p = subprocess.Popen(agent_command(kind, model, prompt, mcp_url, mcp_config, empty), stdout=f,
                                             stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL, cwd=empty,
                                             env=agent_env, start_new_session=True)
                        children.append(p)
                    try:
                        p.wait(timeout=TASK_TIMEOUT)
                    except subprocess.TimeoutExpired:
                        timed_out = True
                        stop(p)
                if stopping.is_set():
                    raise AgentFailed('run stopped')
                calls, _, failure = parse_transcript(d / f'{tid}.jsonl')
                if timed_out or p.returncode or failure:
                    why = f'timed out after {TASK_TIMEOUT}s' if timed_out else failure or f'exited {p.returncode}'
                    raise AgentFailed(f'{tid}: agent CLI {why}')
                if not any(not c['foreign'] and c['tool'] != 'ToolSearch' for c in calls):
                    # Every task needs at least one Radar call; none means the agent couldn't reach the MCP.
                    raise AgentFailed(f'{tid}: the agent made no Radar MCP calls; see {d / f"{tid}.jsonl"}')
                after = suite.state(env, ns)
                (d / f'{tid}.state.json').write_text(json.dumps(after, indent=1))
                verdict, note = suite.check(tid, before, after)
                record({'agent': tag, 'task': tid, 'verdict': {True: 'PASS', False: 'FAIL', None: 'READ'}[verdict],
                        'note': note, 'exit': p.returncode, 'seconds': round(time.time() - started)})
                return after

            def stream(tag, kind, model):
                (out / tag).mkdir(exist_ok=True)
                tid, started = 'setup', time.time()
                try:
                    before = suite.state(env, namespaces[tag])
                    for tid in tasks:
                        started = time.time()
                        before = run_task(tag, kind, model, tid, before)
                except Exception as e:  # noqa: BLE001 - one agent's failure must not hide the others' results
                    record({'agent': tag, 'task': tid, 'verdict': 'ERROR', 'note': str(e)[:500], 'exit': None,
                            'seconds': round(time.time() - started)})

            threads = [threading.Thread(target=stream, args=agent, daemon=True) for agent in agents]
            [t.start() for t in threads]
            [t.join() for t in threads]
            report(out)
        finally:
            stopping.set()
            with spawn_lock:
                for p in children:
                    stop(p)
            for t in threads:
                t.join(timeout=60)
            if radar:
                stop(radar, grace=20)
            if attempted and not a.keep:
                print('deleting test namespaces...')
                for ns in attempted:
                    try:
                        p = env.kubectl('delete', 'namespace', ns, '--ignore-not-found', '--wait=false', check=False, timeout=60)
                        if p.returncode:
                            print(f'WARNING: could not delete namespace {ns}: {p.stderr.strip()}')
                    except subprocess.TimeoutExpired:
                        print(f'WARNING: could not delete namespace {ns}: kubectl timed out')
            elif attempted:
                print('kept namespaces: ' + ', '.join(attempted))
    finally:
        # Removes the flattened kubeconfig; the credential links inside are removed as links, not followed.
        shutil.rmtree(work, ignore_errors=True)
        if work.exists():
            print(f'WARNING: could not remove {work}, which holds cluster credentials; delete it by hand')
        print(f'transcripts: {out}')


if __name__ == '__main__':
    main()
