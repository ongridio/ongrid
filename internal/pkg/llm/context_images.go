package llm

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strings"

	"github.com/cloudwego/eino/schema"
)

func EstimateMessageTokens(msg *schema.Message, provider, model string) (int, error) {
	cp := *msg
	cp.Extra = nil
	cp.ResponseMeta = nil // These fields are not sent to the provider.
	cp.UserInputMultiContent = append([]schema.MessageInputPart(nil), msg.UserInputMultiContent...)
	cp.MultiContent = append([]schema.ChatMessagePart(nil), msg.MultiContent...)
	images := 0
	for i, part := range cp.UserInputMultiContent {
		if part.Image == nil {
			continue
		}
		img := *part.Image
		images += estimateImageTokens(provider, model, img.MIMEType, img.Base64Data, img.URL, img.Detail)
		img.Base64Data, img.URL = nil, nil
		cp.UserInputMultiContent[i].Image = &img
	}
	for i, part := range cp.MultiContent {
		if part.ImageURL == nil {
			continue
		}
		img := *part.ImageURL
		images += estimateImageTokens(provider, model, "", nil, &img.URL, img.Detail)
		img.URL = ""
		cp.MultiContent[i].ImageURL = &img
	}
	b, err := json.Marshal(cp)
	return EstimateText(string(b)) + 8 + images, err
}

func estimateImageTokens(provider, model, mime string, data, url *string, detail schema.ImageURLDetail) int {
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
	name := strings.ToLower(model)
	if provider == ProviderAnthropic || strings.HasPrefix(name, "claude-") {
		cap := 4784.0
		for _, standard := range strings.Fields("claude-opus-4 claude-opus-4-1 claude-opus-4-5 claude-opus-4-6 claude-sonnet-4 claude-sonnet-4-5 claude-sonnet-4-6 claude-haiku-4-5") {
			if modelVersion(name, standard) {
				cap = 1568
				break
			}
		}
		return int(min(cap, math.Ceil(width/28)*math.Ceil(height/28))) + 64
	}
	base, tile := 0, 0
	switch {
	case modelVersion(name, "gpt-4o-mini"):
		base, tile = 2833, 5667
	case modelVersion(name, "gpt-4o"), modelVersion(name, "gpt-4.1"):
		base, tile = 85, 170
	case modelVersion(name, "gpt-5"), modelVersion(name, "gpt-5.1"):
		base, tile = 70, 140
	}
	if tile > 0 {
		if detail == schema.ImageURLDetailLow {
			return base
		}
		scale := min(1.0, 2048/math.Max(width, height))
		width, height = width*scale, height*scale
		scale = min(1.0, 768/math.Min(width, height))
		return base + tile*int(math.Ceil(width*scale/512)*math.Ceil(height*scale/512))
	}
	// A conservative patch estimate avoids charging compressed bytes as text.
	// Unknown models retain headroom rather than assuming a vendor tokenizer.
	patches := math.Ceil(width/32) * math.Ceil(height/32)
	return max(4096, int(math.Ceil(patches*3)))
}

// EstimateText approximates mixed prose, JSON and CJK without a provider tokenizer.
func EstimateText(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+2)/3 + (other*3+1)/2
}
