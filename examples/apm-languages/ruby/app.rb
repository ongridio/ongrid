require 'json'
require 'opentelemetry/sdk'
require 'opentelemetry/exporter/otlp'
require 'opentelemetry/instrumentation/sinatra'

require 'sinatra/base'
OpenTelemetry::SDK.configure { |config| config.use 'OpenTelemetry::Instrumentation::Sinatra' }

class App < Sinatra::Base
  set :bind, '127.0.0.1'
  set :port, Integer(ENV.fetch('HTTP_PORT', '18096'))
  set :server, 'webrick'
  set :environment, :production

  get '/orders/:id' do
    sleep 0.08 if params.key?('slow')
    code = params.key?('fail') ? 500 : 200
    context = OpenTelemetry::Trace.current_span.context
    puts JSON.generate({
      message: 'request completed', level: 'INFO', protocol: 'http',
      'service.name' => ENV.fetch('OTEL_SERVICE_NAME'), 'service.namespace' => ENV.fetch('SERVICE_NAMESPACE'),
      'deployment.environment.name' => ENV.fetch('DEPLOYMENT_ENVIRONMENT'),
      trace_id: context.hex_trace_id, span_id: context.hex_span_id, status: code
    })
    status code
    content_type :json
    JSON.generate(id: params['id'])
  end
  get('/healthz') { 'ok' }
  get('/readyz') { 'ok' }
end

$stdout.sync = true
App.run!
