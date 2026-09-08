using System.Diagnostics;
using System.Text.Json;
using OpenTelemetry.Metrics;
using OpenTelemetry.Trace;

var builder = WebApplication.CreateBuilder(args);
builder.Logging.ClearProviders();
builder.Services.AddOpenTelemetry()
    .WithTracing(otel => otel.AddAspNetCoreInstrumentation().AddOtlpExporter())
    .WithMetrics(otel => otel.AddAspNetCoreInstrumentation().AddOtlpExporter());
var app = builder.Build();
app.MapGet("/orders/{id}", async (HttpContext context, string id) =>
{
    if (context.Request.Query.ContainsKey("slow")) await Task.Delay(80, context.RequestAborted);
    var status = context.Request.Query.ContainsKey("fail") ? 500 : 200;
    Console.WriteLine(JsonSerializer.Serialize(new Dictionary<string, object?>
    {
        ["message"] = "request completed", ["level"] = "INFO", ["protocol"] = "http",
        ["service.name"] = Environment.GetEnvironmentVariable("OTEL_SERVICE_NAME"),
        ["service.namespace"] = Environment.GetEnvironmentVariable("SERVICE_NAMESPACE"),
        ["deployment.environment.name"] = Environment.GetEnvironmentVariable("DEPLOYMENT_ENVIRONMENT"),
        ["trace_id"] = Activity.Current?.TraceId.ToString(), ["span_id"] = Activity.Current?.SpanId.ToString(),
        ["status"] = status
    }));
    return Results.Json(new { id }, statusCode: status);
});
app.MapGet("/healthz", () => Results.Ok());
app.MapGet("/readyz", () => Results.Ok());
app.Run($"http://127.0.0.1:{Environment.GetEnvironmentVariable("HTTP_PORT") ?? "18088"}");
