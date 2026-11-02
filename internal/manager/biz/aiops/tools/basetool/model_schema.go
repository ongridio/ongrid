package basetool

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/eino-contrib/jsonschema"
)

// ModelSchema 保留 SDK 未声明的 Schema 关键字（如 draft-07 definitions），
// 避免校验器与模型接收不同的契约。仅在 Schema 节点递归，不能改写 default/enum 等业务值。
func ModelSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	raw = bytes.TrimSpace(raw)
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("decode model schema: %w", err)
	}
	if err := preserveModelSchema(&schema, raw); err != nil {
		return nil, err
	}
	return &schema, nil
}

func preserveModelSchema(schema *jsonschema.Schema, raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if schema == nil || string(raw) == "true" || string(raw) == "false" {
		return nil
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(raw, &original); err != nil {
		return fmt.Errorf("decode schema keywords: %w", err)
	}
	// SDK 的 any 字段默认经 float64 解码；重新按原始 JSON 精确解码业务值。
	// 此处仅处理值关键字，嵌套对象不是子 Schema。
	for key, target := range map[string]any{
		"const": &schema.Const, "enum": &schema.Enum,
		"default": &schema.Default, "examples": &schema.Examples,
	} {
		if value, ok := original[key]; ok {
			decoder := json.NewDecoder(bytes.NewReader(value))
			decoder.UseNumber()
			if err := decoder.Decode(target); err != nil {
				return fmt.Errorf("decode schema %s: %w", key, err)
			}
		}
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encode model schema: %w", err)
	}
	var emitted map[string]json.RawMessage
	// 空 Schema 在 SDK 中可能编码为 true。
	if string(encoded) != "true" && string(encoded) != "false" {
		if err := json.Unmarshal(encoded, &emitted); err != nil {
			return fmt.Errorf("decode emitted schema: %w", err)
		}
	}
	for key, value := range original {
		if _, exists := emitted[key]; !exists {
			if schema.Extras == nil {
				schema.Extras = map[string]any{}
			}
			// SDK 扩展边界使用 RawMessage，保留数字精度和嵌套未知关键字。
			schema.Extras[key] = value
		}
	}
	for key, child := range map[string]*jsonschema.Schema{
		"not": schema.Not, "if": schema.If, "then": schema.Then, "else": schema.Else,
		"items": schema.Items, "contains": schema.Contains, "additionalProperties": schema.AdditionalProperties,
		"propertyNames": schema.PropertyNames, "contentSchema": schema.ContentSchema,
	} {
		if child != nil {
			if err := preserveModelSchema(child, original[key]); err != nil {
				return err
			}
		}
	}
	for key, children := range map[string][]*jsonschema.Schema{"allOf": schema.AllOf, "anyOf": schema.AnyOf, "oneOf": schema.OneOf, "prefixItems": schema.PrefixItems} {
		if len(children) == 0 {
			continue
		}
		var values []json.RawMessage
		if err := json.Unmarshal(original[key], &values); err != nil {
			return fmt.Errorf("decode schema %s: %w", key, err)
		}
		for i, child := range children {
			if err := preserveModelSchema(child, values[i]); err != nil {
				return err
			}
		}
	}
	maps := map[string]map[string]*jsonschema.Schema{"$defs": schema.Definitions, "patternProperties": schema.PatternProperties, "dependentSchemas": schema.DependentSchemas}
	if schema.Properties != nil {
		props := map[string]*jsonschema.Schema{}
		for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
			props[pair.Key] = pair.Value
		}
		maps["properties"] = props
	}
	for key, children := range maps {
		if len(children) == 0 {
			continue
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(original[key], &values); err != nil {
			return fmt.Errorf("decode schema %s: %w", key, err)
		}
		for name, child := range children {
			if err := preserveModelSchema(child, values[name]); err != nil {
				return err
			}
		}
	}
	return nil
}
