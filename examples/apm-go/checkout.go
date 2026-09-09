package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// An in-memory checkout demo: authorization is simulated, with no real charges.
func checkoutHandler(log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quantity := 2
		if raw := r.URL.Query().Get("quantity"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 10 {
				http.Error(w, "quantity must be 1..10", http.StatusBadRequest)
				return
			}
			quantity = value
		}
		coupon, region := r.URL.Query().Get("coupon"), r.URL.Query().Get("region")
		if region == "" {
			region = "US"
		}
		if (coupon != "" && coupon != "SAVE20") || (region != "US" && region != "EU" && region != "us" && region != "eu") {
			http.Error(w, "unsupported coupon or region", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("checkout.coupon", coupon), attribute.String("checkout.region", region), attribute.Int("checkout.quantity", quantity))
		total, err := checkout(ctx, r.PathValue("id"), quantity, coupon, region)
		status, message := http.StatusOK, "checkout completed"
		if err != nil {
			status, message = http.StatusInternalServerError, err.Error()
			span.RecordError(err, trace.WithStackTrace(true))
			span.SetStatus(codes.Error, message)
		}
		sc := span.SpanContext()
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelError
		}
		log.Log(ctx, level, message, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "status", status, "order_id", r.PathValue("id"), "quantity", quantity, "coupon", coupon, "region", region)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(map[string]any{"message": message, "trace_id": sc.TraceID().String(), "total_cents": total, "version": version, "commit_sha": commit}); err != nil {
			log.Debug("write checkout response", "error", err)
		}
	})
}

func checkout(ctx context.Context, id string, quantity int, coupon, region string) (int, error) {
	ctx, span := otel.Tracer("apm-demo/checkout").Start(ctx, "checkout.process")
	defer span.End()
	price, err := loadOrder(ctx, id)
	if err == nil {
		price = calculateTotal(ctx, price, quantity, coupon)
		err = authorizePayment(ctx, price)
	}
	if err == nil {
		var shipping int
		shipping, err = shippingQuote(ctx, region)
		price += shipping
	}
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return 0, fmt.Errorf("checkout order %s: %w", id, err)
	}
	return price, nil
}

func loadOrder(ctx context.Context, id string) (int, error) {
	_, span := otel.Tracer("apm-demo/checkout").Start(ctx, "catalog.load_order")
	defer span.End()
	if id != "42" {
		err := fmt.Errorf("ORDER_NOT_FOUND: %s", id)
		span.RecordError(err, trace.WithStackTrace(true))
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}
	span.SetAttributes(attribute.Int("order.unit_price_cents", 2500))
	return 2500, nil
}
