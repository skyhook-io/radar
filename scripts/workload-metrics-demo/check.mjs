import {spawn} from 'node:child_process';
import {createServer} from 'node:net';
import {once} from 'node:events';
import {mkdtemp, writeFile} from 'node:fs/promises';
import {createWriteStream} from 'node:fs';
import {tmpdir} from 'node:os';
import {fileURLToPath} from 'node:url';
import {resolve, join} from 'node:path';

const [kubeconfig, context, mode = 'check'] = process.argv.slice(2);
if (!kubeconfig || context !== 'kind-radar-workload-metrics-demo') throw Error('Use workload-metrics-demo.sh check with its isolated kind kubeconfig.');
if (!['check', 'history'].includes(mode)) throw Error('Expected check or history mode.');
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
    if (['cpu', 'memory', 'throttling'].includes(key)) {
      if (response.history?.[key]?.mode !== 'workload-history') errors.push(`${key}: historical scope`);
      if (panel?.series?.length !== 2 || !panel.series.some(s => s.labels.aggregation === 'Workload')) errors.push(`${key}: total/maximum series`);
      if (response.comparison?.[key]?.series?.length !== test.pods) errors.push(`${key}: current comparison coverage`);
    }
  }
  for (const key of ['cpu', 'memory', 'throttling', ...(test.source ? [test.source] : [])]) {
    if (!response.attribution?.[key]?.startsWith('Matched')) errors.push(`${key}: automatic attribution`);
  }
  if (test.http && response.source !== test.source) errors.push('Wrong observer');
  if (test.http && response.history?.requests?.mode !== 'workload-history') errors.push('HTTP historical scope');
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
  if (mode === 'history' && !process.exitCode) {
    results.history = [];
    const kubectl = async args => {
      const child = spawn('kubectl', ['--kubeconfig', kubeconfig, '--context', context, ...args], {stdio: ['ignore', 'pipe', 'pipe']});
      let output = '';
      child.stdout.on('data', data => { output += data; });
      child.stderr.on('data', data => { output += data; });
      const [code] = await once(child, 'close');
      if (code !== 0) throw Error(`Fixture command failed: ${args.join(' ')} (${output.slice(-500)})`);
      return output;
    };
    const pointsAt = (response, timestamp) => {
      for (const family of ['cpu', 'memory', 'throttling', 'requests']) {
        if (response.history?.[family]?.mode !== 'workload-history') throw Error(`${family}: lost historical scope`);
      }
      const values = {};
      for (const key of ['cpu', 'memory', 'throttling', 'requests', 'errors', 'p50', 'p95']) {
        for (const series of response.panels[key]?.series ?? []) {
          const point = series.dataPoints.find(p => p.timestamp === timestamp && Number.isFinite(p.value) && p.value !== null);
          if (!point) throw Error(`${key}: missing historical point at ${timestamp}`);
          values[`${key}:${JSON.stringify(series.labels)}`] = point.value;
        }
        if (!Object.keys(values).some(k => k.startsWith(key + ':'))) throw Error(`${key}: no historical series`);
      }
      return values;
    };
    for (const test of cases.filter(test => test.http)) {
      const [, namespace, name] = test.workload.split('/');
      const resource = `deployment/${name}`;
      const original = JSON.parse(await kubectl(['-n', namespace, 'get', resource, '-o', 'json']));
      const originalPods = JSON.parse(await kubectl(['-n', namespace, 'get', 'pods', '-l', Object.entries(original.spec.selector.matchLabels).map(([key,value]) => `${key}=${value}`).join(','), '-o', 'json'])).items.map(p => p.metadata.uid);
      const path = `/api/prometheus/workload/${test.workload}?range=10m&source=${test.source}`;
      let before, timestamp, values;
      const baselineDeadline = Date.now() + 180000;
      do {
        before = await get(path);
        const candidates = (before.panels.requests?.series[0]?.dataPoints ?? []).map(p => p.timestamp).filter(t => t <= before.end - 30).sort((a,b) => b-a);
        for (const candidate of candidates) {
          try {
            const sample = pointsAt(before, candidate);
            if (!Object.entries(sample).filter(([key]) => ['requests:', 'errors:', 'p50:', 'p95:'].some(prefix => key.startsWith(prefix))).every(([,value]) => value > 0)) continue;
            timestamp = candidate;
            values = sample;
            break;
          } catch { /* Warm-up may not yet have a complete, settled sample. */ }
        }
        if (!values) { console.log(`${test.workload}: waiting for settled historical baseline`); await delay(5000); }
      } while (!values && Date.now() < baselineDeadline && !abort.signal.aborted);
      if (!values) throw Error('No complete settled historical baseline; fixtures have not been mutated');
      const entry = {workload: test.workload, timestamp, before, values, originalPods, phases: []};
      results.history.push(entry);
      const verify = async phase => {
        const response = await get(path);
        const after = pointsAt(response, timestamp);
        if (JSON.stringify(Object.keys(after).sort()) !== JSON.stringify(Object.keys(values).sort())) throw Error(`${phase}: historical series changed`);
        for (const key of Object.keys(values)) {
          if (Math.abs(after[key] - values[key]) > 1e-9 * Math.max(1, Math.abs(values[key]))) throw Error(`${phase}: ${key} changed at ${timestamp}: ${values[key]} -> ${after[key]}`);
        }
        entry.phases.push({phase, response, values: after});
        console.log(`${test.workload}: ${phase} historical values unchanged at ${timestamp}`);
      };
      try {
        await kubectl(['-n', namespace, 'rollout', 'restart', resource]);
        await kubectl(['-n', namespace, 'rollout', 'status', resource, '--timeout=180s']);
        for (let attempt = 0; attempt < 60; attempt++) {
          const live = JSON.parse(await kubectl(['-n', namespace, 'get', 'pods', '-o', 'json'])).items;
          if (!live.some(p => originalPods.includes(p.metadata.uid))) break;
          if (attempt === 59) throw Error('Old Pods were not deleted');
          await delay(1000);
        }
        await verify('rollout');
        await kubectl(['-n', namespace, 'scale', resource, '--replicas=0']);
        for (let attempt = 0; attempt < 60; attempt++) {
          const response = await get(path);
          if (response.podsTotal === 0) break;
          if (attempt === 59) throw Error('Workload did not reach zero current Pods');
          await delay(1000);
        }
        await verify('scale-zero');
      } finally {
        await kubectl(['-n', namespace, 'scale', resource, `--replicas=${original.spec.replicas ?? 1}`]);
        await kubectl(['-n', namespace, 'rollout', 'status', resource, '--timeout=180s']);
      }
    }
  }
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
