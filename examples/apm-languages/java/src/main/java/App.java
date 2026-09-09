import io.grpc.netty.shaded.io.grpc.netty.NettyServerBuilder;
import java.net.InetSocketAddress;
import io.grpc.Status;
import io.grpc.health.v1.HealthGrpc;
import io.grpc.health.v1.HealthCheckRequest;
import io.grpc.health.v1.HealthCheckResponse;
import io.grpc.stub.StreamObserver;
import io.javalin.Javalin;
import io.opentelemetry.api.trace.Span;

public class App {
  static void log(String protocol) {
    var ctx = Span.current().getSpanContext();
    System.out.printf("{\"message\":\"request completed\",\"protocol\":\"%s\",\"service.name\":\"%s\",\"service.namespace\":\"%s\",\"deployment.environment.name\":\"%s\",\"service.instance.id\":\"%s\",\"service.version\":\"%s\",\"trace_id\":\"%s\",\"span_id\":\"%s\"}%n",
        protocol, System.getenv("OTEL_SERVICE_NAME"), System.getenv().getOrDefault("SERVICE_NAMESPACE", "trade"), System.getenv().getOrDefault("DEPLOYMENT_ENVIRONMENT", "acceptance"), System.getenv().getOrDefault("SERVICE_INSTANCE_ID", ""), System.getenv().getOrDefault("SERVICE_VERSION", ""), ctx.getTraceId(), ctx.getSpanId());
  }
  public static void main(String[] args) throws Exception {
    NettyServerBuilder.forAddress(new InetSocketAddress("127.0.0.1", Integer.parseInt(System.getenv().getOrDefault("RPC_PORT", "18081"))))
        .addService(new HealthGrpc.HealthImplBase() {
          public void check(HealthCheckRequest req, StreamObserver<HealthCheckResponse> out) {
            if (req.getService().equals("slow")) {
              try { Thread.sleep(80); } catch (InterruptedException e) { Thread.currentThread().interrupt(); out.onError(Status.CANCELLED.asRuntimeException()); return; }
            }
            log("rpc");
            if (req.getService().equals("missing")) { out.onError(Status.NOT_FOUND.asRuntimeException()); return; }
            out.onNext(HealthCheckResponse.newBuilder().setStatus(HealthCheckResponse.ServingStatus.SERVING).build());
            out.onCompleted();
          }
        }).build().start();
    Javalin.create().get("/orders/{id}", ctx -> {
      if (ctx.queryParam("slow") != null) Thread.sleep(80);
      log("http");
      ctx.status(ctx.queryParam("fail") != null ? 500 : 200).result("order " + ctx.pathParam("id"));
    }).start("127.0.0.1", Integer.parseInt(System.getenv().getOrDefault("HTTP_PORT", "18080")));
  }
}
