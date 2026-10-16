package chatruntime

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

func startAgentSpan(ctx context.Context, session *aiopsmodel.Session, name string) (context.Context, trace.Span) {
	conversationID := session.ID
	if session.RootSessionID != nil && *session.RootSessionID != "" {
		conversationID = *session.RootSessionID
	}
	if session.AgentID != nil && *session.AgentID != "" {
		name = *session.AgentID
	}
	return otel.Tracer("github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/chatruntime").Start(ctx, "invoke_agent "+name,
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", name),
			attribute.String("gen_ai.conversation.id", conversationID),
			attribute.String("ongrid.agent.session.id", session.ID),
			attribute.Bool("ongrid.agent.content.capture", tracing.AgentContentEnabled()),
		),
	)
}
