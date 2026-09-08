<?php
// Official Slim instrumentation creates spans; the official Metrics API records real requests.
require __DIR__ . '/vendor/autoload.php';

use OpenTelemetry\API\Globals;
use OpenTelemetry\API\Trace\Span;
use React\EventLoop\Loop;
use React\Http\HttpServer;
use React\Socket\SocketServer;
use Slim\Factory\AppFactory;

$app = AppFactory::create();
$app->addRoutingMiddleware();
$app->addErrorMiddleware(false, true, true);
$app->get('/orders/{id}', function ($request, $response, $args) {
    $query = $request->getQueryParams();
    if (isset($query['slow'])) usleep(80000);
    $status = isset($query['fail']) ? 500 : 200;
    $span = Span::getCurrent()->getContext();
    fwrite(STDOUT, json_encode([
        'message' => 'request completed', 'level' => 'INFO', 'protocol' => 'http',
        'service.name' => getenv('OTEL_SERVICE_NAME'), 'service.namespace' => getenv('SERVICE_NAMESPACE'),
        'deployment.environment.name' => getenv('DEPLOYMENT_ENVIRONMENT'),
        'trace_id' => $span->getTraceId(), 'span_id' => $span->getSpanId(), 'status' => $status,
    ], JSON_THROW_ON_ERROR) . "\n");
    $response->getBody()->write(json_encode(['id' => $args['id']], JSON_THROW_ON_ERROR));
    return $response->withStatus($status)->withHeader('Content-Type', 'application/json');
});
foreach (['/healthz', '/readyz'] as $path) $app->get($path, fn ($request, $response) => $response);
$duration = Globals::meterProvider()->getMeter('ongrid.apm.example')->createHistogram(
    'http.server.request.duration', 's', 'HTTP server request duration',
    ['ExplicitBucketBoundaries' => [0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10]]
);
$server = new HttpServer(function ($request) use ($app, $duration) {
    $started = hrtime(true);
    $response = $app->handle($request);
    if ($request->getMethod() === 'GET' && preg_match('#^/orders/[^/]+$#', $request->getUri()->getPath())) {
        $duration->record((hrtime(true) - $started) / 1e9, [
            'http.request.method' => 'GET', 'http.route' => '/orders/{id}',
            'http.response.status_code' => $response->getStatusCode(),
        ]);
    }
    return $response;
});
$server->on('error', static function (Throwable $error) { fwrite(STDERR, $error . "\n"); });
$server->listen(new SocketServer('127.0.0.1:' . (getenv('HTTP_PORT') ?: '18090')));
// Long-lived worker: counters survive requests. Flush through official SDK readers.
Loop::addPeriodicTimer(5, static function () {
    Globals::meterProvider()->forceFlush();
    Globals::tracerProvider()->forceFlush();
});
Loop::run();
