package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	skillsvc "github.com/ongridio/ongrid/internal/manager/biz/skill"
	skillcore "github.com/ongridio/ongrid/internal/skill"
)

// SkillRunner is the narrow contract the bridge needs. *skillsvc.Service
// satisfies it; tests inject a fake.
type SkillRunner interface {
	Execute(ctx context.Context, caller skillsvc.Caller, in skillsvc.ExecuteInput) (*skillsvc.ExecuteOutput, error)
}

// buildSkillToolSchema generates a JSON-schema-shaped object that
// matches the BaseTool parameter contract.
//
// SubprocessSkill carries its own raw JSON Schema (from skill.json's
// "schema" field) so we forward it verbatim — manifests should already
// be in the function-calling shape. Otherwise we derive the schema from
// the metadata's ParamSchema. Edge-scoped skills get an additional
// required `device_id` integer; manager-scoped skills don't.
func buildSkillToolSchema(e skillcore.Executor) (json.RawMessage, error) {
	meta := e.Metadata()
	// Prefer the unified BuildSchema helper which honors the
	// RawSchemaProvider extension (used by the inventory bridge for
	// hand-written BaseTools whose schemas have arrays / nested objects
	// that ParamSchema can't express). SubprocessSkill is a special-case
	// preserved here: its raw schema lives on the struct field, not via
	// the interface method, so we still inline-merge.
	var base map[string]any
	if ss, ok := e.(*skillcore.SubprocessSkill); ok && len(ss.Schema) > 0 {
		if err := json.Unmarshal(ss.Schema, &base); err != nil {
			return nil, fmt.Errorf("subprocess skill %q: invalid manifest schema: %w", meta.Key, err)
		}
		if base == nil {
			base = map[string]any{}
		}
		if _, hasType := base["type"]; !hasType {
			base["type"] = "object"
		}
	} else {
		raw, err := skillcore.BuildSchema(e)
		if err != nil {
			return nil, fmt.Errorf("skill %q: build schema: %w", meta.Key, err)
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			return nil, fmt.Errorf("skill %q: decode schema: %w", meta.Key, err)
		}
		if base == nil {
			base = map[string]any{"type": "object"}
		}
	}
	if meta.EffectiveScope() == skillcore.ScopeHost {
		props, _ := base["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			base["properties"] = props
		}
		// Schema-level identifier is `device_id` — matches @-mention chip
		// id and Prom label. Executor still resolves it to an edge_id for
		// the tunnel call. `edge_id` lingered as a confusing alias before
		// the device split landed; keeping it out of the schema entirely
		// prevents prompts from latching back onto it.
		props["device_id"] = map[string]any{
			"type":        "integer",
			"description": "Device id to run this skill on (required). Same id as the @-mention chip and the Prom device_id label.",
		}
		required, _ := base["required"].([]any)
		hasDeviceID := false
		for _, name := range required {
			if name == "device_id" {
				hasDeviceID = true
				break
			}
		}
		if !hasDeviceID {
			base["required"] = append(required, "device_id")
		}
	}
	return json.Marshal(base)
}

// newSkillExecutor returns the skill execution function. For edge-scoped
// skills resolve device_id through the current Host binding before invoking the skill. For manager-scoped
// skills (web_search, subprocess packs) it forwards args verbatim.
func (r *Registry) newSkillExecutor(svc SkillRunner, key string, scope skillcore.Scope) func(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	return func(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
		var envelope map[string]json.RawMessage
		if len(args) > 0 {
			if err := json.Unmarshal(args, &envelope); err != nil {
				return ExecuteResult{}, fmt.Errorf("skill %q: decode args: %w", key, err)
			}
		}
		if envelope == nil {
			envelope = map[string]json.RawMessage{}
		}

		var edgeID, deviceID uint64
		if scope == skillcore.ScopeHost {
			if raw, ok := envelope["device_id"]; ok {
				if err := json.Unmarshal(raw, &deviceID); err != nil {
					return ExecuteResult{}, fmt.Errorf("skill %q: device_id must be integer: %w", key, err)
				}
				delete(envelope, "device_id")
			}
			if _, exists := envelope["edge_id"]; exists {
				return ExecuteResult{}, fmt.Errorf("skill %q: use device_id for host targets", key)
			}

			if deviceID == 0 {
				return ExecuteResult{}, fmt.Errorf("skill %q: device_id required", key)
			}
			// Resolve device_id → current Edge through the verified binding.
			// Never reinterpret a Host ID as an Edge ID.
			var resolveErr error
			edgeID, resolveErr = NewDeviceResolver(r.devices, r.edges).ResolveEdgeID(ctx, deviceID)
			if resolveErr != nil {
				return ExecuteResult{}, resolveErr
			}
			if edgeID == 0 {
				return ExecuteResult{}, fmt.Errorf("skill %q: device_id=%d has no host-edge link (try query_devices to list available device ids)", key, deviceID)
			}
		} else {
			// Manager-scoped skills shouldn't get an edge_id / device_id,
			// but if a confused LLM sends one we strip rather than
			// erroring — the model tends to recover better from silent
			// ignore.
			delete(envelope, "edge_id")
			delete(envelope, "device_id")
		}

		params, err := json.Marshal(envelope)
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("skill %q: re-marshal params: %w", key, err)
		}

		out, err := svc.Execute(ctx, skillsvc.Caller{Role: "system"}, skillsvc.ExecuteInput{
			Key:    key,
			EdgeID: edgeID,
			Params: params,
		})
		if err != nil {
			if scope == skillcore.ScopeHost {
				return ExecuteResult{}, fmt.Errorf("skill %q on edge %d: %w", key, edgeID, err)
			}
			return ExecuteResult{}, fmt.Errorf("skill %q: %w", key, err)
		}

		// The LLM consumes JSON; bundle skill result + error string into
		// one envelope so the model sees structured output even on
		// skill-side errors.
		body, marshalErr := json.Marshal(map[string]any{
			"result": out.Result,
			"error":  out.Error,
		})
		if marshalErr != nil {
			return ExecuteResult{}, marshalErr
		}
		result := ExecuteResult{ResultJSON: body}
		if edgeID != 0 {
			result.DeviceID = &deviceID
		}
		return result, nil
	}
}

// SetSkillRunner 在构建目录前注入 Skill 服务，所有入口使用相同适配器。
func (r *Registry) SetSkillRunner(runner SkillRunner) { r.skillRunner = runner }

func (r *Registry) appendSafeSkillTools(out []basetool.BaseTool) []basetool.BaseTool {
	if r.skillRunner == nil {
		return out
	}
	seen := make(map[string]bool, len(out))
	for _, t := range out {
		if info, err := t.Info(context.Background()); err == nil && info != nil {
			seen[info.Name] = true
		}
	}
	for _, executor := range skillcore.AllByClass(skillcore.ClassSafe) {
		// 展示目录的反向桥接不是新的工具来源，否则会递归适配。
		if _, exported := executor.(*baseToolSkillExecutor); exported {
			continue
		}
		meta := executor.Metadata()
		// 内置 BaseTool 是同名能力的执行实现（如抓包查询），只保留一份。
		if seen[meta.Key] {
			if _, external := executor.(*skillcore.SubprocessSkill); external {
				r.log.Error("external skill conflicts with built-in tool; registration rejected", "skill", meta.Key)
			}
			continue
		}
		schema, err := buildSkillToolSchema(executor)
		if err != nil {
			r.log.Error("build skill tool schema", "skill", meta.Key, "error", err)
			continue
		}
		origin := basetool.OriginBuiltin
		if _, external := executor.(*skillcore.SubprocessSkill); external {
			origin = basetool.OriginSkill
		}
		out = append(out, &skillBaseTool{
			info:    basetool.ToolInfo{Name: meta.Key, Description: meta.Description, Parameters: schema, Class: "read", Origin: origin},
			execute: r.newSkillExecutor(r.skillRunner, meta.Key, meta.EffectiveScope()),
		})
		seen[meta.Key] = true
	}
	return out
}

type skillBaseTool struct {
	info    basetool.ToolInfo
	execute func(context.Context, json.RawMessage) (ExecuteResult, error)
}

func (t *skillBaseTool) Info(context.Context) (*basetool.ToolInfo, error) {
	info := t.info
	return &info, nil
}
func (t *skillBaseTool) InvokableRun(ctx context.Context, args string, _ ...basetool.InvokeOption) (string, error) {
	if t.info.Name == basetool.ToolWebSearch && !basetool.WebSearchAllowed(ctx) {
		return "", fmt.Errorf("web_search disabled for this request")
	}
	result, err := t.execute(ctx, json.RawMessage(args))
	if err != nil {
		return "", err
	}
	return string(result.ResultJSON), nil
}
