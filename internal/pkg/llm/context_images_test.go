package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestImageBudgetIgnoresBase64SizeAndPreservesRequest(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	var prior int
	for _, compression := range []png.CompressionLevel{png.NoCompression, png.BestCompression} {
		var buf bytes.Buffer
		encoder := png.Encoder{CompressionLevel: compression}
		require.NoError(t, encoder.Encode(&buf, img))
		encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
		msg := schema.UserMessage("Explain this screenshot.")
		msg.UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{MIMEType: "image/png", Base64Data: &encoded}}}}
		before, _ := json.Marshal(msg)
		cost, err := estimateMessageTokens(msg, "openai", "gpt-4o")
		require.NoError(t, err)
		require.Less(t, cost, 1000)
		if prior != 0 {
			require.Equal(t, prior, cost)
		}
		prior = cost
		stub := &einoStubClient{}
		inner, err := NewClientChatModel(ClientChatModelConfig{Client: stub, Model: "gpt-4o"})
		require.NoError(t, err)
		bounded := WithContextBudget(inner.(model.ToolCallingChatModel), ContextBudget{})
		_, err = bounded.Generate(context.Background(), []*schema.Message{msg})
		require.NoError(t, err)
		require.Equal(t, encoded, stub.lastReq.Messages[0].Images[0].Data)
		stream, err := bounded.Stream(context.Background(), []*schema.Message{msg})
		require.NoError(t, err)
		stream.Close()
		after, _ := json.Marshal(msg)
		require.JSONEq(t, string(before), string(after))
	}
}

func TestImageBudgetCountsImagesAndTextSeparately(t *testing.T) {
	url := "https://example.invalid/image.png"
	msg := schema.UserMessage("text")
	msg.UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}}}}
	one, err := estimateMessageTokens(msg, "custom", "alias")
	require.NoError(t, err)
	msg.UserInputMultiContent = append(msg.UserInputMultiContent, msg.UserInputMultiContent[0])
	two, err := estimateMessageTokens(msg, "custom", "alias")
	require.NoError(t, err)
	require.GreaterOrEqual(t, two-one, 32768)
	msg.Content += "additional text"
	more, err := estimateMessageTokens(msg, "custom", "alias")
	require.NoError(t, err)
	require.Equal(t, len("additional text"), more-two)
}
