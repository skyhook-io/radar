import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {readFile} from 'node:fs/promises';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {promisify} from 'node:util';
import {test} from 'node:test';

test('capture omits backend credentials while preserving raw metric labels', async () => {
  const server = createServer((req, res) => {
    assert.equal(req.headers.authorization, 'Bearer RADAR_SECRET');
    const body = req.url === '/api/cluster-info' ? {context: 'test'}
      : req.url === '/api/prometheus/status' ? {connected: true, available: true, address: 'https://user:BACKEND_SECRET@metrics.example/?token=QUERY_SECRET', error: 'ERROR_SECRET'}
      : req.url.startsWith('/api/prometheus/query?') ? {resultType: 'matrix', series: [{labels: {pod: 'web-1'}, dataPoints: [{timestamp: 1, value: 2}]}]}
      : {state: 'available', panels: {}};
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify(body));
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  try {
    const {stdout} = await promisify(execFile)(process.execPath, [new URL('./capture.mjs', import.meta.url).pathname, `http://127.0.0.1:${server.address().port}`, 'Deployment/demo/web'], {env: {...process.env, RADAR_CAPTURE_AUTHORIZATION: 'Bearer RADAR_SECRET'}});
    const dir = stdout.trim().replace(/^Evidence: /, '');
    const saved = await readFile(`${dir}/results.json`, 'utf8');
    assert.ok(!saved.includes('SECRET'));
    const result = JSON.parse(saved);
    assert.equal(result.connection.connected, true);
    assert.equal(result.workloads[0].rawIstio.series[0].labels.pod, 'web-1');
  } finally {
    await new Promise(resolve => server.close(resolve));
  }
});
