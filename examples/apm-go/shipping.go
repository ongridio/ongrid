package main

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func shippingQuote(ctx context.Context, region string) (int, error) {
	_, span := otel.Tracer("apm-demo/checkout").Start(ctx, "shipping.quote")
	defer span.End()
	span.SetAttributes(attribute.String("shipping.region", region))
	rates := map[string]int{"US": 500, "EU": 900}
	amount, ok := rates[region]
	if !ok {
		err := fmt.Errorf("SHIPPING_RATE_NOT_FOUND: no rate for region %q", region)
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}
	return amount, nil
}
