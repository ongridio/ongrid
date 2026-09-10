const assert = require('node:assert/strict');
const { test } = require('node:test');
const { setTimeout: delay } = require('node:timers/promises');
const grpc = require('@grpc/grpc-js');
const { metrics } = require('@opentelemetry/api');
const { MeterProvider, InMemoryMetricExporter, PeriodicExportingMetricReader } = require('@opentelemetry/sdk-metrics');

test('records unary, streaming, failure, cancellation and deadline once without traces', async () => {
  const exporter = new InMemoryMetricExporter();
  const reader = new PeriodicExportingMetricReader({ exporter, exportIntervalMillis: 60000 });
  const provider = new MeterProvider({ readers: [reader] });
  metrics.setGlobalMeterProvider(provider);
  const { grpcMetricsInterceptor } = require('./grpc-metrics.cjs');
  const method = { path: '/test.Service/Call', requestStream: false, responseStream: false,
    requestSerialize: Buffer.from, requestDeserialize: b => b.toString(),
    responseSerialize: Buffer.from, responseDeserialize: b => b.toString() };
  const definition = { call: method, stream: { ...method, path: '/test.Service/Stream', responseStream: true } };
  const server = new grpc.Server({ interceptors: [grpcMetricsInterceptor] });
  server.addService(definition, {
    call: async (call, callback) => {
      if (call.request === 'wait') { call.sendMetadata(new grpc.Metadata()); await delay(100); }
      callback(call.request === 'fail' ? { code: grpc.status.NOT_FOUND } : null, 'ok');
    },
    stream: call => { call.write('one'); call.write('two'); call.end(); },
  });
  let client;
  try {
    const port = await new Promise((resolve, reject) => server.bindAsync('127.0.0.1:0', grpc.ServerCredentials.createInsecure(), (err, port) => err ? reject(err) : resolve(port)));
    const Client = grpc.makeGenericClientConstructor(definition);
    client = new Client(`127.0.0.1:${port}`, grpc.credentials.createInsecure());
    const invoke = (request, options = {}, cancel = false) => new Promise(resolve => {
      const call = client.call(request, options, err => resolve(err?.code ?? grpc.status.OK));
      if (cancel) call.on('metadata', () => call.cancel());
    });
    assert.equal(await invoke('ok'), grpc.status.OK);
    assert.equal(await invoke('fail'), grpc.status.NOT_FOUND);
    assert.equal(await invoke('wait', {}, true), grpc.status.CANCELLED);
    assert.equal(await invoke('wait', { deadline: Date.now() + 40 }), grpc.status.DEADLINE_EXCEEDED);
    const messages = [];
    await new Promise((resolve, reject) => client.stream('ok').on('data', v => messages.push(v)).on('end', resolve).on('error', reject));
    assert.deepEqual(messages, ['one', 'two']);
    await delay(150); // Late callbacks must not add observations after cancellation.
    const result = await reader.collect();
    assert.deepEqual(result.errors, []);
    const histogram = result.resourceMetrics.scopeMetrics.flatMap(s => s.metrics).find(m => m.descriptor.name === 'rpc.server.call.duration');
    assert.equal(histogram.descriptor.unit, 's');
    assert.equal(histogram.dataPoints.reduce((n, p) => n + p.value.count, 0), 5);
    const statuses = {};
    for (const point of histogram.dataPoints) {
      assert.equal(point.attributes['rpc.system.name'], 'grpc');
      assert.match(point.attributes['rpc.method'], /^test.Service\/(Call|Stream)$/);
      statuses[point.attributes['rpc.response.status_code']] = (statuses[point.attributes['rpc.response.status_code']] || 0) + point.value.count;
      assert.ok(point.value.sum > 0 && point.value.sum < 1);
    }
    assert.deepEqual(statuses, { OK: 2, NOT_FOUND: 1, CANCELLED: 1, DEADLINE_EXCEEDED: 1 });
  } finally {
    client?.close();
    server.forceShutdown();
    await provider.shutdown();
    metrics.disable();
  }
});
