// Preload the official auto-instrumentations-node/register before this file.
const { trace } = require('@opentelemetry/api');
const express = require('express');
const grpc = require('@grpc/grpc-js');
const { grpcMetricsInterceptor } = require('./grpc-metrics.cjs');
const loader = require('@grpc/proto-loader');
const path = require('node:path');
const health = grpc.loadPackageDefinition(loader.loadSync(path.join(__dirname, 'health.proto'))).grpc.health.v1;
function logRequest(protocol) {
  const ctx = trace.getActiveSpan()?.spanContext();
  console.log(JSON.stringify({ message: 'request completed', protocol,
    'service.name': process.env.OTEL_SERVICE_NAME, 'service.namespace': process.env.SERVICE_NAMESPACE || 'trade',
    'deployment.environment.name': process.env.DEPLOYMENT_ENVIRONMENT || 'acceptance', trace_id: ctx?.traceId, span_id: ctx?.spanId }));
}
const app = express();
app.get('/orders/:id', async (req, res) => {
  if (req.query.slow) await new Promise(resolve => setTimeout(resolve, 80));
  logRequest('http');
  res.status(req.query.fail ? 500 : 200).json({ id: req.params.id });
});
app.listen(Number(process.env.HTTP_PORT || 18080), '127.0.0.1');
const rpc = new grpc.Server({ interceptors: [grpcMetricsInterceptor] });
rpc.addService(health.Health.service, { check: async (call, callback) => {
  if (call.request.service === 'slow') await new Promise(resolve => setTimeout(resolve, 80));
  logRequest('rpc');
  callback(call.request.service === 'missing' ? { code: grpc.status.NOT_FOUND, details: 'missing' } : null, { status: 1 });
}});
rpc.bindAsync(`127.0.0.1:${process.env.RPC_PORT || 18081}`, grpc.ServerCredentials.createInsecure(), err => { if (err) throw err; });
