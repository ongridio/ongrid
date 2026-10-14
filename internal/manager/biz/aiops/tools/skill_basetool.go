package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	skillcore "github.com/ongridio/ongrid/internal/skill"
)

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
