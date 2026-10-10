package chatruntime

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"sync/atomic"
)

// directStreamChatModel emits user-visible model chunks before handing the
// stream to Eino. The ReAct tool-call checker must drain a model stream before
// choosing its branch; emitting from an HTTP callback copy alone can therefore
// make all deltas arrive together at stream end.
type directStreamChatModel struct {
	inner      model.ToolCallingChatModel
	emit       Emit
	iterations atomic.Int64
}

func (m *directStreamChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, input, opts...)
}

func (m *directStreamChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	innerStream, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	copies := innerStream.Copy(2)
	direct, graph := copies[0], copies[1]
	iteration := int(m.iterations.Add(1))

	go func() {
		defer direct.Close()
		for {
			chunk, err := direct.Recv()
			if err != nil {
				return
			}
			if chunk == nil {
				continue
			}
			if chunk.Content != "" {
				m.emit(Event{
					Type: EventAssistantDelta,
					Delta: &AssistantDeltaEvent{
						Iteration: iteration,
						Content:   chunk.Content,
						Kind:      "content",
					},
				})
			}
			if chunk.ReasoningContent != "" {
				m.emit(Event{
					Type: EventAssistantDelta,
					Delta: &AssistantDeltaEvent{
						Iteration: iteration,
						Content:   chunk.ReasoningContent,
						Kind:      "reasoning",
					},
				})
			}
		}
	}()
	return graph, nil
}

func (m *directStreamChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &directStreamChatModel{inner: inner, emit: m.emit}, nil
}
