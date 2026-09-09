package main

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func calculateTotal(ctx context.Context, unitPrice, quantity int, coupon string) int {
	_, span := otel.Tracer("apm-demo/checkout").Start(ctx, "pricing.calculate_total")
	defer span.End()
	subtotal := unitPrice * quantity
	total := subtotal
	if coupon == "SAVE20" {
		total -= subtotal * 20 / 100
	}
	span.SetAttributes(attribute.Int("pricing.subtotal_cents", subtotal), attribute.Int("pricing.total_cents", total))
	return total
}

func authorizePayment(ctx context.Context, amount int) error {
	_, span := otel.Tracer("apm-demo/checkout").Start(ctx, "payment.authorize")
	defer span.End()
	span.SetAttributes(attribute.Int("payment.amount_cents", amount))
	if amount <= 0 {
		err := fmt.Errorf("PAYMENT_INVALID_AMOUNT: amount must be positive, got %d cents", amount)
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}
