import java.util.List;
import io.opentelemetry.sdk.autoconfigure.spi.AutoConfigurationCustomizer;
import io.opentelemetry.sdk.autoconfigure.spi.AutoConfigurationCustomizerProvider;
import io.opentelemetry.sdk.metrics.Aggregation;
import io.opentelemetry.sdk.metrics.InstrumentSelector;
import io.opentelemetry.sdk.metrics.InstrumentType;
import io.opentelemetry.sdk.metrics.View;

// Agent 2.31.1 uses generic buckets for stable RPC durations (seconds).
// A standard SDK view changes aggregation without replacing instrumentation.
public final class RpcMetricsConfiguration implements AutoConfigurationCustomizerProvider {
    @Override
    public void customize(AutoConfigurationCustomizer customizer) {
        customizer.addMeterProviderCustomizer((builder, config) -> builder.registerView(
            InstrumentSelector.builder().setType(InstrumentType.HISTOGRAM)
                .setName("rpc.*.call.duration").build(),
            View.builder().setAggregation(Aggregation.explicitBucketHistogram(List.of(
                .005, .01, .025, .05, .075, .1, .25, .5, .75, 1., 2.5, 5., 7.5, 10.
            ))).build()));
    }
}
