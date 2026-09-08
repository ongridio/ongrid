#include <chrono>
#include <cstdlib>
#include <iostream>
#include <map>
#include <memory>
#include <mutex>
#include <stdexcept>
#include <string>
#include <thread>
#include <httplib.h>
#include <nlohmann/json.hpp>
#include <opentelemetry/exporters/otlp/otlp_http_exporter_factory.h>
#include <opentelemetry/exporters/otlp/otlp_http_exporter_options.h>
#include <opentelemetry/exporters/otlp/otlp_http_metric_exporter_factory.h>
#include <opentelemetry/exporters/otlp/otlp_http_metric_exporter_options.h>
#include <opentelemetry/sdk/metrics/meter_provider.h>
#include <opentelemetry/sdk/metrics/export/periodic_exporting_metric_reader_factory.h>
#include <opentelemetry/sdk/metrics/export/periodic_exporting_metric_reader_options.h>
#include <opentelemetry/sdk/trace/batch_span_processor_factory.h>
#include <opentelemetry/sdk/trace/batch_span_processor_options.h>
#include <opentelemetry/sdk/trace/tracer_provider.h>
#include <opentelemetry/sdk/trace/samplers/always_on.h>
#include <opentelemetry/sdk/trace/samplers/always_off.h>
#include <opentelemetry/trace/propagation/http_trace_context.h>

namespace otel = opentelemetry;
namespace sdk = otel::sdk;
namespace otlp = otel::exporter::otlp;

std::string env(const char *name, const char *fallback = "") {
  const char *value = std::getenv(name);
  return value ? value : fallback;
}

// Adapter required by the official W3C propagator; HTTP parsing stays in httplib.
struct Headers : otel::context::propagation::TextMapCarrier {
  const httplib::Request &request;
  explicit Headers(const httplib::Request &value) : request(value) {}
  otel::nostd::string_view Get(otel::nostd::string_view key) const noexcept override {
    auto it = request.headers.find(std::string(key));
    return it == request.headers.end() ? otel::nostd::string_view{} : otel::nostd::string_view{it->second};
  }
  void Set(otel::nostd::string_view, otel::nostd::string_view) noexcept override {}
};

int main() {
  try {
    auto resource = sdk::resource::Resource::Create({});
    auto sampler_name = env("OTEL_TRACES_SAMPLER", "always_on");
    std::unique_ptr<sdk::trace::Sampler> sampler;
    if (sampler_name == "always_on") sampler = std::make_unique<sdk::trace::AlwaysOnSampler>();
    else if (sampler_name == "always_off") sampler = std::make_unique<sdk::trace::AlwaysOffSampler>();
    else throw std::invalid_argument("example supports OTEL_TRACES_SAMPLER=always_on or always_off");
    sdk::trace::BatchSpanProcessorOptions batch;
    auto processor = sdk::trace::BatchSpanProcessorFactory::Create(
      otlp::OtlpHttpExporterFactory::Create(otlp::OtlpHttpExporterOptions{}), batch);
    sdk::trace::TracerProvider traces(std::move(processor), resource, std::move(sampler));
    auto tracer = traces.GetTracer("ongrid.apm.example");
    sdk::metrics::MeterProvider metrics(std::make_unique<sdk::metrics::ViewRegistry>(), resource);
    auto buckets = std::make_shared<sdk::metrics::HistogramAggregationConfig>();
    buckets->boundaries_ = {0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10};
    metrics.AddView(
      std::make_unique<sdk::metrics::InstrumentSelector>(sdk::metrics::InstrumentType::kHistogram, "http.server.request.duration", "s"),
      std::make_unique<sdk::metrics::MeterSelector>("ongrid.apm.example", "", ""),
      std::make_unique<sdk::metrics::View>("http.server.request.duration", "HTTP request duration", sdk::metrics::AggregationType::kHistogram, buckets));
    sdk::metrics::PeriodicExportingMetricReaderOptions interval;
    metrics.AddMetricReader(sdk::metrics::PeriodicExportingMetricReaderFactory::Create(
      otlp::OtlpHttpMetricExporterFactory::Create(otlp::OtlpHttpMetricExporterOptions{}), interval));
    auto duration = metrics.GetMeter("ongrid.apm.example")->CreateDoubleHistogram("http.server.request.duration", "HTTP request duration", "s");
    httplib::Server server;
    std::mutex log_mutex;
    server.Get("/healthz", [](const auto &, auto &response) { response.set_content("ok", "text/plain"); });
    server.Get("/readyz", [](const auto &, auto &response) { response.set_content("ok", "text/plain"); });
    server.Get(R"(/orders/([^/]+))", [&](const httplib::Request &request, httplib::Response &response) {
      const auto started = std::chrono::steady_clock::now();
      Headers headers(request);
      otel::trace::StartSpanOptions options;
      options.kind = otel::trace::SpanKind::kServer;
      otel::context::Context parent;
      options.parent = otel::trace::propagation::HttpTraceContext().Extract(headers, parent);
      const int status = request.has_param("fail") ? 500 : 200;
      std::map<std::string, otel::common::AttributeValue> attributes = {
        {"http.request.method", "GET"}, {"http.route", "/orders/{id}"}, {"http.response.status_code", status}};
      auto span = tracer->StartSpan("GET /orders/{id}", attributes, options);
      if (request.has_param("slow")) std::this_thread::sleep_for(std::chrono::milliseconds(80));
      if (status >= 500) span->SetStatus(otel::trace::StatusCode::kError, "example failure");
      char trace_id[32], span_id[16];
      span->GetContext().trace_id().ToLowerBase16(trace_id);
      span->GetContext().span_id().ToLowerBase16(span_id);
      {
      std::lock_guard<std::mutex> lock(log_mutex);
      std::cout << nlohmann::json({{"message", "request completed"}, {"level", "INFO"}, {"protocol", "http"},
        {"service.name", env("OTEL_SERVICE_NAME")}, {"service.namespace", env("SERVICE_NAMESPACE")},
        {"deployment.environment.name", env("DEPLOYMENT_ENVIRONMENT")},
        {"trace_id", std::string(trace_id, 32)}, {"span_id", std::string(span_id, 16)}, {"status", status}}).dump() << std::endl;
      }
      response.status = status;
      response.set_content("order", "text/plain");
      duration->Record(std::chrono::duration<double>(std::chrono::steady_clock::now() - started).count(), attributes, otel::context::Context{});
      span->End();
    });
    if (!server.listen("127.0.0.1", std::stoi(env("HTTP_PORT", "18092")))) throw std::runtime_error("HTTP listen failed");
    if (!metrics.Shutdown() || !traces.Shutdown()) throw std::runtime_error("telemetry shutdown failed");
  } catch (const std::exception &error) {
    std::cerr << error.what() << std::endl;
    return 1;
  }
}
