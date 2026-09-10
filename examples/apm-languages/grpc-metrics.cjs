const { metrics } = require('@opentelemetry/api');
const { ServerInterceptingCall, status } = require('@grpc/grpc-js');

const duration = metrics.getMeter('ongrid.grpc-example').createHistogram('rpc.server.call.duration', {
  unit: 's', description: 'Duration of inbound gRPC calls',
  advice: { explicitBucketBoundaries: [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10] },
});

// Install once per server, alongside trace instrumentation. No span sampling dependency.
function grpcMetricsInterceptor(method, call) {
  const started = process.hrtime.bigint();
  let recorded = false;
  function finish(code) {
    if (recorded) return;
    recorded = true;
    duration.record(Number(process.hrtime.bigint() - started) / 1e9, {
      'rpc.system.name': 'grpc', 'rpc.method': method.path.replace(/^\//, ''),
      'rpc.response.status_code': status[code] || 'UNKNOWN',
    });
  }
  return new ServerInterceptingCall(call, {
    start(next) {
      next({ onCancel() {
        finish(Number(call.getDeadline()) <= Date.now() ? status.DEADLINE_EXCEEDED : status.CANCELLED);
      } });
    },
    sendStatus(result, next) {
      finish(result.code);
      next(result);
    },
  });
}

module.exports = { grpcMetricsInterceptor };
