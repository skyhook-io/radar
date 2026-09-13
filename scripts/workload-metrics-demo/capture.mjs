import {mkdtemp, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';

const [address, ...workloads] = process.argv.slice(2);
if (!address || workloads.length === 0) throw Error('Usage: node capture.mjs RADAR_URL Kind/namespace/name [...]');
const base = new URL(address);
if (!['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.search || base.hash) throw Error('Use a plain Radar URL; supply auth only through RADAR_CAPTURE_AUTHORIZATION.');
const headers = process.env.RADAR_CAPTURE_AUTHORIZATION ? {Authorization: process.env.RADAR_CAPTURE_AUTHORIZATION} : {};
const dir = await mkdtemp(join(tmpdir(), 'radar-metrics-capture-'));
const result = {checkedAt: new Date().toISOString(), workloads: []};
async function get(path) {
  const response = await fetch(base.href.replace(/\/$/, '') + path, {headers, signal: AbortSignal.timeout(40000), redirect: 'error'});
  if (!response.ok) throw Error(`Radar request failed: HTTP ${response.status}`);
  const reader = response.body.getReader();
  const chunks = [];
  let size = 0;
  while (true) {
    const {done, value} = await reader.read();
    if (done) break;
    size += value.length;
    if (size > 8 * 1024 * 1024) { await reader.cancel(); throw Error('Evidence response exceeds 8 MiB'); }
    chunks.push(value);
  }
  const body = JSON.parse(Buffer.concat(chunks).toString());
  if (body.truncated) throw Error('Radar summarized the raw series. Use a dedicated test instance with RADAR_MCP_PROM_MAX_RESPONSE_BYTES=8388608; do not claim complete evidence.');
  return body;
}
try {
  result.cluster = await get('/api/cluster-info');
  const status = await get('/api/prometheus/status');
  result.connection = {available: status.available, connected: status.connected, discovering: status.discovering};
  for (const workload of workloads) {
    const parts = workload.split('/');
    if (parts.length !== 3 || !['Deployment', 'StatefulSet', 'DaemonSet'].includes(parts[0]) || parts.slice(1).some(s => !/^[a-z0-9][a-z0-9.-]*$/.test(s))) throw Error('Expected Kind/namespace/name');
    const path = '/api/prometheus/workload/' + parts.map(encodeURIComponent).join('/') + '?range=30m';
    let response;
    for (let attempt = 0; attempt < 6; attempt++) {
      response = await get(path);
      if (response.state !== 'detecting' || attempt === 5) break;
      await new Promise(resolve => setTimeout(resolve, 2000));
    }
    const entry = {workload, response};
    result.workloads.push(entry);
    const selector = '{__name__=~"istio_requests_total|istio_request_duration_milliseconds_count|istio_request_duration_milliseconds_bucket",reporter="destination",request_protocol="http",namespace=' + JSON.stringify(parts[1]) + ',destination_workload=' + JSON.stringify(parts[2]) + '}';
    entry.rawIstio = await get('/api/prometheus/query?range=10m&query=' + encodeURIComponent(selector));
  }
} catch (error) { result.error = error.message; process.exitCode = 1; }
await writeFile(join(dir, 'results.json'), JSON.stringify(result, null, 2), {mode: 0o600});
console.log(`Evidence: ${dir}`);
