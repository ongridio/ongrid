package basetool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// WithValidation 在实际调用前执行完整 Schema 校验。动态工具保留原始扩展规则。
// 缓存按 Schema 内容刷新，避免 MCP 更新后仍使用旧约束。
func WithValidation(inner BaseTool) BaseTool {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(*validatedTool); ok {
		return inner
	}
	return &validatedTool{inner: inner}
}

type validatedTool struct {
	inner    BaseTool
	mu       sync.Mutex
	raw      string
	compiled *jsonschema.Schema
}

func (t *validatedTool) schema(ctx context.Context) (*ToolInfo, *jsonschema.Schema, error) {
	info, err := t.inner.Info(ctx)
	if err != nil {
		return nil, nil, err
	}
	if info == nil {
		return nil, nil, fmt.Errorf("tool metadata is nil")
	}
	copyInfo := *info
	raw := info.Parameters
	if len(info.ExecutionParameters) > 0 {
		raw = info.ExecutionParameters
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{"type":"object"}`)
	}
	// 只关闭内置工具根参数对象；嵌套字典与外部 MCP 的 additionalProperties 保持不变。
	if info.Origin == OriginBuiltin {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, nil, fmt.Errorf("tool %s schema: %w", info.Name, err)
		}
		if obj != nil && obj["properties"] != nil && obj["additionalProperties"] == nil {
			obj["additionalProperties"] = json.RawMessage(`false`)
			raw, err = json.Marshal(obj)
			if err != nil {
				return nil, nil, fmt.Errorf("tool %s schema: %w", info.Name, err)
			}
		}
	}
	if len(info.ExecutionParameters) > 0 {
		copyInfo.ExecutionParameters = raw
	} else {
		copyInfo.Parameters = raw
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.compiled != nil && t.raw == string(raw) {
		return &copyInfo, t.compiled, nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	// Schema 是输入契约，不能在编译期间触发外部网络或文件读取。
	compiler.LoadURL = func(url string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external schema reference is not supported: %s", url)
	}
	const location = "https://ongrid.invalid/tool-schema.json"
	if err := compiler.AddResource(location, bytes.NewReader(raw)); err != nil {
		return nil, nil, fmt.Errorf("tool %s schema: %w", info.Name, err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, nil, fmt.Errorf("tool %s schema: %w", info.Name, err)
	}
	t.raw = string(raw)
	t.compiled = compiled
	return &copyInfo, compiled, nil
}

func (t *validatedTool) Info(ctx context.Context) (*ToolInfo, error) {
	info, _, err := t.schema(ctx)
	return info, err
}

func (t *validatedTool) InvokableRun(ctx context.Context, args string, opts ...InvokeOption) (string, error) {
	info, schema, err := t.schema(ctx)
	if err != nil {
		return "", err
	}
	// JSON Schema 校验边界必须接收任意 JSON 值；业务层仍使用类型化参数。
	var value any
	decoder := json.NewDecoder(bytes.NewBufferString(args))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("tool %s invalid JSON arguments: %w", info.Name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", fmt.Errorf("tool %s arguments must contain one JSON value", info.Name)
	}
	if err := schema.Validate(value); err != nil {
		return "", fmt.Errorf("tool %s invalid arguments: %w", info.Name, err)
	}
	// 外部工具保持原参数表示；内置工具使用 Go 整数解码，需统一 1.0/1e0 的表示。
	if info.Origin == OriginBuiltin && normalizeIntegerNumbers(value) {
		normalized, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("tool %s normalize arguments: %w", info.Name, err)
		}
		args = string(normalized)
	}
	return t.inner.InvokableRun(ctx, args, opts...)
}
