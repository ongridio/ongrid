package llm

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strings"

	"github.com/cloudwego/eino/schema"
)

func estimateMessageTokens(msg *schema.Message) (int, error) {
	cp := *msg
	cp.UserInputMultiContent = append([]schema.MessageInputPart(nil), msg.UserInputMultiContent...)
	cp.MultiContent = append([]schema.ChatMessagePart(nil), msg.MultiContent...)
	images := 0
	for i, part := range cp.UserInputMultiContent {
		if part.Image == nil {
			continue
		}
		img := *part.Image
		images += estimateImageTokens(img.MIMEType, img.Base64Data, img.URL)
		img.Base64Data, img.URL = nil, nil
		cp.UserInputMultiContent[i].Image = &img
	}
	for i, part := range cp.MultiContent {
		if part.ImageURL == nil {
			continue
		}
		img := *part.ImageURL
		images += estimateImageTokens("", nil, &img.URL)
		img.URL = ""
		cp.MultiContent[i].ImageURL = &img
	}
	b, err := json.Marshal(cp)
	return len(b) + 32 + images, err
}

func estimateImageTokens(mime string, data, url *string) int {
	encoded := ""
	if data != nil {
		encoded = *data
	}
	if encoded == "" && url != nil && strings.HasPrefix(*url, "data:") {
		if header, body, ok := strings.Cut(*url, ","); ok && strings.HasSuffix(header, ";base64") {
			encoded, mime = body, strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		}
	}
	// Read only the header region, never fetch remote image URLs during preflight.
	header, err := io.ReadAll(io.LimitReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)), 1<<20))
	w, h, ok := ImageDimensions(header, mime)
	if err != nil || !ok || w <= 0 || h <= 0 || w > 65535 || h > 65535 {
		// shortcut: unavailable dimensions use a conservative image allowance;
		// use provider token-count APIs if exact multimodal budgeting is required.
		return 32768
	}
	width, height := float64(w), float64(h)
	// shortcut: pixel allowances are approximate; use provider token-count APIs
	// if exact multimodal budgeting is required.
	patches := math.Ceil(width/32) * math.Ceil(height/32)
	return max(4096, int(math.Ceil(patches*3)))
}
