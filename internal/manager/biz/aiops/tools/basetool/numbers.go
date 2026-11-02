package basetool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// normalizeIntegerNumbers 只规范化可精确表示为整数的 JSON 数字，绝不经过 float64。
// 任意 JSON 是 Schema 适配边界的输入；业务执行器仍解码自己的类型化结构。
func normalizeIntegerNumbers(value any) bool {
	changed := false
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if n, ok := item.(json.Number); ok {
				if normalized := integerLexeme(n); normalized != n {
					value[key] = normalized
					changed = true
				}
			} else if normalizeIntegerNumbers(item) {
				changed = true
			}
		}
	case []any:
		for i, item := range value {
			if n, ok := item.(json.Number); ok {
				if normalized := integerLexeme(n); normalized != n {
					value[i] = normalized
					changed = true
				}
			} else if normalizeIntegerNumbers(item) {
				changed = true
			}
		}
	}
	return changed
}

func integerLexeme(number json.Number) json.Number {
	text := string(number)
	if !strings.ContainsAny(text, ".eE") {
		return number
	}
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign = "-"
		text = text[1:]
	}
	exponent := 0
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		parsed, err := strconv.Atoi(text[index+1:])
		if err != nil || parsed > 1000 || parsed < -1000 {
			return number
		}
		exponent = parsed
		text = text[:index]
	}
	fraction := 0
	if index := strings.IndexByte(text, '.'); index >= 0 {
		fraction = len(text) - index - 1
		text = text[:index] + text[index+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return json.Number("0")
	}
	scale := fraction - exponent
	if scale > 0 {
		if scale >= len(text) || strings.Trim(text[len(text)-scale:], "0") != "" {
			return number
		}
		text = text[:len(text)-scale]
	} else {
		// Go 整数最多需要 20 位；更大的数交给类型化解码器拒绝，不做无界扩容。
		if len(text)-scale > 20 {
			return number
		}
		text += strings.Repeat("0", -scale)
	}
	return json.Number(sign + text)
}

// DecodeUint64 解析路由 ID，接受 1.0/1e0 等精确整数，不改变其他业务参数。
func DecodeUint64(raw json.RawMessage) (uint64, error) {
	if !json.Valid(raw) {
		return 0, fmt.Errorf("invalid JSON integer")
	}
	// JSON 解码边界使用 any，以拒绝字符串、null 和其他非数字类型。
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, fmt.Errorf("decode integer: %w", err)
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("expected JSON integer")
	}
	id, err := strconv.ParseUint(string(integerLexeme(number)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decode unsigned integer: %w", err)
	}
	return id, nil
}
