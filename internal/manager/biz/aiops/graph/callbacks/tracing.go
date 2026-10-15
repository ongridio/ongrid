package callbacks

import (
	"context"
	"errors"

	langfuse "github.com/cloudwego/eino-ext/callbacks/langfuse/v2"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

// TraceHandler uses Eino's maintained callback with the existing Tempo exporter.
type TraceHandler struct {
	*langfuse.CallbackHandler
}

func NewTraceHandler(ctx context.Context) (*TraceHandler, error) {
	provider, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		return nil, nil
	}
	handler, err := langfuse.NewHandler(ctx, &langfuse.Config{
		TracerProvider:          provider,
		MaskFunc:                tracing.MaskAgentContent,
		MaxAttributeValueLength: tracing.AgentContentMaxBytes,
		MaxSpanAttributeBytes:   24 * 1024,
	})
	if err != nil {
		return nil, err
	}
	return &TraceHandler{CallbackHandler: handler}, nil
}

func (h *TraceHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	if !contentComponent(info) {
		input = nil
	}
	ctx = h.CallbackHandler.OnStart(ctx, info, input)
	if info != nil && info.Component == components.ComponentOfChatModel {
		ctx = tracing.WithModelCallbackSpan(ctx)
	}
	return ctx
}

func contentComponent(info *callbacks.RunInfo) bool {
	return info != nil && (info.Component == components.ComponentOfChatModel || info.Component == components.ComponentOfTool)
}

func (h *TraceHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	if !contentComponent(info) {
		output = nil
	}
	return h.CallbackHandler.OnEnd(ctx, info, output)
}

func (h *TraceHandler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	if !contentComponent(info) && output != nil {
		output = schema.StreamReaderWithConvert(output, func(callbacks.CallbackOutput) (callbacks.CallbackOutput, error) { return nil, nil })
	}
	return h.CallbackHandler.OnEndWithStreamOutput(ctx, info, output)
}

func (h *TraceHandler) OnStartWithStreamInput(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	if !contentComponent(info) && input != nil {
		input = schema.StreamReaderWithConvert(input, func(callbacks.CallbackInput) (callbacks.CallbackInput, error) { return nil, nil })
	}
	ctx = h.CallbackHandler.OnStartWithStreamInput(ctx, info, input)
	if info != nil && info.Component == components.ComponentOfChatModel {
		ctx = tracing.WithModelCallbackSpan(ctx)
	}
	return ctx
}

func (h *TraceHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	if err != nil {
		_, rerun := compose.IsInterruptRerunError(err)
		_, interrupted := compose.ExtractInterruptInfo(err)
		if !rerun && !interrupted {
			switch {
			case errors.Is(err, context.Canceled):
				err = context.Canceled
			case errors.Is(err, context.DeadlineExceeded):
				err = context.DeadlineExceeded
			default:
				// shortcut: snapshot mutable Eino node paths until the upstream callback snapshots errors before its goroutine.
				err = errors.New(err.Error())
			}
		}
	}
	return h.CallbackHandler.OnError(ctx, info, err)
}
