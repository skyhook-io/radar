import {spawn} from 'node:child_process';
import {createServer} from 'node:net';
import {once} from 'node:events';
import {mkdtemp, writeFile} from 'node:fs/promises';
import {createWriteStream} from 'node:fs';
import {tmpdir} from 'node:os';
import {fileURLToPath} from 'node:url';
import {resolve, join} from 'node:path';

const [kubeconfig, context] = process.argv.slice(2);
if (!kubeconfig || context !== 'kind-radar-workload-metrics-demo') throw Error('Use workload-metrics-demo.sh check with its isolated kind kubeconfig.');
const root = fileURLToPath(new URL('../../', import.meta.url));
const evidence = await mkdtemp(join(tmpdir(), 'radar-workload-check-'));
const children = [];
const logs = [];
const abort = new AbortController();
process.once('SIGINT', () => abort.abort());
process.once('SIGTERM', () => abort.abort());
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const results = {checkedAt: new Date().toISOString(), context, workloads: []};

function start(command, args, name) {
  const child = spawn(command, args, {stdio: ['ignore', 'pipe', 'pipe']});
  children.push(child);
  const log = createWriteStream(join(evidence, name + '.log'), {mode: 0o600});
  logs.push(log);
  child.stdout.pipe(log, {end: false});
  child.stderr.pipe(log, {end: false});
  child.on('error', error => { results.error = error.message; abort.abort(); });
  child.once('close', () => { child.closed = true; });
  return child;
}

const cases = [
  {workload: 'Deployment/demo/web', source: 'beyla', pods: 2, http: true, errorBand: [20, 45]},
  {workload: 'Deployment/radar-metrics-fixture/web', source: 'istio', pods: 2, http: true, errorBand: [15, 35]},
  {workload: 'Deployment/radar-metrics-fixture/worker', pods: 1},
  {workload: 'StatefulSet/radar-metrics-fixture/redis', pods: 1},
  {workload: 'DaemonSet/radar-metrics-fixture/node-worker', pods: 1},
];

function failures(response, test) {
  const errors = [];
  if (response.pods !== test.pods || response.podsTotal !== test.pods) errors.push('Pod coverage');
  const expected = ['cpu', 'memory', 'throttling'];
  if (test.http) expected.push('requests', 'errors', 'p50', 'p95', 'observedPods');
  for (const key of expected) {
    const panel = response.panels?.[key];
    const points = panel?.series?.flatMap(s => s.dataPoints) ?? [];
    const recent = points.filter(p => p.timestamp >= response.end - 90 && p.value !== null && Number.isFinite(p.value));
    if (panel?.state !== 'available' || recent.length === 0) errors.push(`${key}: ${panel?.state ?? 'missing'} ${panel?.reason ?? ''}`);
    if (['requests', 'errors', 'p50', 'p95'].includes(key) && !recent.some(p => p.value > 0)) errors.push(`${key}: no positive observations`);
    if (key === 'observedPods' && !recent.some(p => p.value === test.pods)) errors.push('Reporting Pod coverage');
    if (key === 'errors' && !recent.some(p => p.value >= test.errorBand[0] && p.value <= test.errorBand[1])) errors.push('Error percentage outside fixture band');
    if (['cpu', 'memory', 'throttling'].includes(key) && panel?.series?.length !== test.pods) errors.push(`${key}: per-Pod series coverage`);
  }
  for (const key of ['cpu', 'memory', 'throttling', ...(test.source ? [test.source] : [])]) {
    if (!response.attribution?.[key]?.startsWith('Matched')) errors.push(`${key}: automatic attribution`);
  }
  if (test.http && response.source !== test.source) errors.push('Wrong observer');
  if (!test.http && response.panels?.requests?.state !== 'unavailable') errors.push('Unexpected HTTP observations');
  return errors;
}

try {
  const forward = start('kubectl', ['--kubeconfig', kubeconfig, '--context', context, '-n', 'monitoring', 'port-forward', '--address=127.0.0.1', 'svc/prometheus', ':9090'], 'forward');
  let promPort;
  forward.stdout.on('data', chunk => { const match = chunk.toString().match(/127\.0\.0\.1:(\d+) ->/); if (match) promPort = Number(match[1]); });
  for (let i = 0; i < 100 && !promPort; i++) {
    if (abort.signal.aborted || forward.exitCode !== null) throw Error('Port-forward failed; see evidence log');
    await delay(200);
  }
  if (!promPort) throw Error('Port-forward readiness timeout');
  const socket = createServer();
  socket.listen(0, '127.0.0.1');
  await once(socket, 'listening');
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  const radar = start(resolve(root, 'radar'), [`--kubeconfig=${kubeconfig}`, '--kubeconfig-dir=', '--listen-address=127.0.0.1', `--port=${port}`, '--no-browser', '--prometheus-header=X-Radar-Demo=true', '--prometheus-header-from-env=X-Radar-Demo-Env=RADAR_METRICS_DEMO_HEADER', `--prometheus-url=http://127.0.0.1:${promPort}`], 'radar');
  const get = async path => {
    if (abort.signal.aborted || radar.exitCode !== null || forward.exitCode !== null) throw Error('An owned test process stopped');
    const response = await fetch(`http://127.0.0.1:${port}${path}`, {signal: AbortSignal.any([abort.signal, AbortSignal.timeout(30000)])});
    if (!response.ok) throw Object.assign(Error(`HTTP ${response.status}`), {status: response.status});
    return response.json();
  };
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { results.cluster = await get('/api/cluster-info'); ready = true; break; } catch {
      if (radar.exitCode !== null || abort.signal.aborted) throw Error('Radar failed startup');
      await delay(1000);
    }
  }
  if (!ready) throw Error('Radar readiness timeout');
  if (results.cluster.context !== context) throw Error('Radar connected to an unexpected Kubernetes context');
  for (const test of cases) {
    const deadline = Date.now() + 180000;
    let response, errors;
    do {
      try {
        response = await get(`/api/prometheus/workload/${test.workload}?range=10m${test.source ? '&source=' + test.source : ''}`);
        errors = failures(response, test);
      } catch (error) {
        if (![409, 503].includes(error.status)) throw error;
        errors = ['Cluster cache or discovery is not ready'];
      }
      if (!errors.length) break;
      if (abort.signal.aborted) throw Error('Check interrupted');
      console.log(`${test.workload}: waiting (${errors.slice(0, 2).join('; ')})`);
      await delay(5000);
    } while (Date.now() < deadline);
    results.workloads.push({test, response, errors});
    console.log(`${test.workload}: ${errors.length ? 'FAIL' : 'PASS'}`);
  }
  if (results.workloads.some(w => w.errors.length)) process.exitCode = 1;
} catch (error) {
  results.error = error.message;
  console.error(error.message);
  process.exitCode = 1;
} finally {
  for (const child of children.reverse()) {
    if (child.closed || child.exitCode !== null || child.signalCode !== null) continue;
    const closed = once(child, 'close');
    child.kill('SIGTERM');
    const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
    await closed;
    clearTimeout(timer);
  }
  for (const log of logs) log.end();
  await writeFile(join(evidence, 'results.json'), JSON.stringify(results, null, 2), {mode: 0o600});
  console.log(`Evidence: ${evidence}`);
}
