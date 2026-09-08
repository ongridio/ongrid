use opentelemetry::{global, metrics::MeterProvider, trace::{Span, SpanKind, Status, Tracer, TracerProvider}, KeyValue};
use opentelemetry_otlp::{MetricExporter, SpanExporter};
use opentelemetry_sdk::{metrics::SdkMeterProvider, propagation::TraceContextPropagator, trace::SdkTracerProvider, Resource};
use std::{collections::HashMap, env, error::Error, thread, time::{Duration, Instant}};
use tiny_http::{Method, Response, Server};

fn main() -> Result<(), Box<dyn Error + Send + Sync>> {
    let resource = Resource::builder().build();
    let traces = SdkTracerProvider::builder()
        .with_resource(resource.clone())
        .with_batch_exporter(SpanExporter::builder().with_http().build()?).build();
    let metrics = SdkMeterProvider::builder()
        .with_resource(resource)
        .with_periodic_exporter(MetricExporter::builder().with_http().build()?).build();
    global::set_text_map_propagator(TraceContextPropagator::new());
    let tracer = traces.tracer("ongrid.apm.example");
    let duration = metrics.meter("ongrid.apm.example")
        .f64_histogram("http.server.request.duration").with_unit("s")
        .with_boundaries(vec![0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0]).build();
    let server = Server::http(format!("127.0.0.1:{}", env::var("HTTP_PORT").unwrap_or("18094".into())))?;
    for request in server.incoming_requests() {
        let url = request.url().to_owned();
        let (path, query) = url.split_once('?').unwrap_or((&url, ""));
        if ["/healthz", "/readyz"].contains(&path) {
            request.respond(Response::from_string("ok"))?;
            continue;
        }
        if request.method() != &Method::Get || !path.strip_prefix("/orders/").is_some_and(|id| !id.is_empty() && !id.contains('/')) {
            request.respond(Response::empty(404))?;
            continue;
        }
        let started = Instant::now();
        let headers: HashMap<String, String> = request.headers().iter()
            .map(|h| (h.field.to_string().to_lowercase(), h.value.to_string())).collect();
        let parent = global::get_text_map_propagator(|p| p.extract(&headers));
        let mut span = tracer.span_builder("GET /orders/{id}").with_kind(SpanKind::Server).start_with_context(&tracer, &parent);
        let status: u16 = if query.split('&').any(|p| p == "fail=1") { 500 } else { 200 };
        if query.split('&').any(|p| p == "slow=1") { thread::sleep(Duration::from_millis(80)); }
        let attributes = [KeyValue::new("http.request.method", "GET"), KeyValue::new("http.route", "/orders/{id}"), KeyValue::new("http.response.status_code", i64::from(status))];
        span.set_attributes(attributes.iter().cloned());
        if status >= 500 { span.set_status(Status::error("example failure")); }
        println!("{}", serde_json::json!({
            "message": "request completed", "level": "INFO", "protocol": "http",
            "service.name": env::var("OTEL_SERVICE_NAME")?, "service.namespace": env::var("SERVICE_NAMESPACE")?,
            "deployment.environment.name": env::var("DEPLOYMENT_ENVIRONMENT")?,
            "trace_id": span.span_context().trace_id().to_string(), "span_id": span.span_context().span_id().to_string(), "status": status
        }));
        let result = request.respond(Response::from_string("order").with_status_code(status));
        duration.record(started.elapsed().as_secs_f64(), &attributes);
        span.end();
        if let Err(error) = result { eprintln!("response failed: {error}"); }
    }
    metrics.shutdown()?;
    traces.shutdown()?;
    Ok(())
}
