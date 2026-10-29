package externalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
)

// ReviewArguments redacts named secrets and opaque configuration before a
// trusted administrator inspects a request. The original remains encrypted.
func ReviewArguments(a biz.Arguments) any {
	var hideOpaque func(any) any
	hideOpaque = func(v any) any {
		switch value := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for key, item := range value {
				if key == "fields" || key == "value" || key == "config" {
					out[key] = "[REDACTED]"
				} else {
					out[key] = hideOpaque(item)
				}
			}
			return out
		case []any:
			out := make([]any, len(value))
			for i, item := range value {
				out[i] = hideOpaque(item)
			}
			return out
		default:
			return v
		}
	}
	return sanitize(hideOpaque(map[string]any(a)))
}

// BuiltinWriteTools covers actions that have no equivalent JSON product API.
func BuiltinWriteTools(base []basetool.BaseTool) ([]biz.Tool, error) {
	allow := map[string][]string{
		"serve_page":        {"pages:read", "pages:write"},
		"send_notification": {"notifications:read", "notifications:execute"},
		"send_im_message":   {"im:read", "im:execute"},
	}
	out := []biz.Tool{}
	for _, baseTool := range base {
		info, err := baseTool.Info(context.Background())
		if err != nil {
			return nil, err
		}
		scopes, ok := allow[info.Name]
		if !ok {
			continue
		}
		if info.Class != "write" || info.Origin != "" {
			return nil, fmt.Errorf("unexpected built-in write %s", info.Name)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(info.Parameters, &schema); err != nil {
			return nil, err
		}
		for key, value := range schema.Properties {
			p := value.(map[string]any)
			if p["type"] == "string" {
				p["maxLength"] = 32768
			}
			if strings.HasSuffix(key, "_id") && p["type"] == "integer" {
				schema.Properties[key] = idSchema()
			}
		}
		name := info.Name
		tool := biz.Tool{Name: name, Description: info.Description + " Requires explicit user intent and one confirmation on the operation page at approval_url. dry_run previews without sending or publishing.", Scopes: scopes, Mutation: true, Sensitive: true, Verification: "accepted_by_remote", Schema: writeSchema(schema.Properties, append(schema.Required, "reason")...), Validate: requireReason,
			Policy: func(a biz.Arguments) (biz.Policy, error) {
				return biz.Policy{Target: biz.Target{Type: "builtin", Key: name + ":" + biz.String(a, "channel") + ":" + biz.String(a, "group_id")}, Remote: true}, nil
			},
			Run: func(ctx context.Context, a biz.Arguments) (any, error) {
				if biz.Bool(a, "dry_run") {
					return map[string]any{"tool": name, "applied": false, "approval_required": true}, nil
				}
				return runBase(ctx, baseTool, a)
			},
		}
		out = append(out, tool)
	}
	return out, nil
}

// ActionTools accepts dedicated built-ins; Engine enforces scopes before Run.
func ActionTools(restart, k8s basetool.BaseTool) ([]biz.Tool, error) {
	out := []biz.Tool{}
	if restart != nil {
		out = append(out, biz.Tool{Name: tools.ToolNameRestartService, Description: "Restart one allowed host service after the user confirms once at approval_url. dry_run only previews the request. Poll get_operation; remote acceptance is not a health check.", Scopes: []string{"devices:read", "host:execute"}, Mutation: true, Schema: writeSchema(map[string]any{"device_id": idSchema(), "service": enumSchema(tools.AllowedRestartServices...)}, "device_id", "service", "reason"), Validate: requireReason, Policy: func(a biz.Arguments) (biz.Policy, error) {
			return biz.Policy{Target: target("device", uint64(biz.Number(a, "device_id"))), Remote: true}, nil
		}, Run: func(ctx context.Context, a biz.Arguments) (any, error) {
			if biz.Bool(a, "dry_run") {
				return map[string]any{"target": a["device_id"], "service": a["service"], "approval_required": true, "applied": false}, nil
			}
			result, err := runBase(ctx, restart, a)
			if err != nil {
				return nil, err
			}
			m, _ := result.(map[string]any)
			if m["restarted"] != true || m["mocked"] == true {
				return nil, fmt.Errorf("remote restart not confirmed")
			}
			return result, nil
		}})
	}
	if k8s != nil {
		var schema map[string]any
		if err := json.Unmarshal(tools.ExecuteK8sActionSchema, &schema); err != nil {
			return nil, err
		}
		properties := schema["properties"].(map[string]any)
		properties["cluster_id"] = idSchema()
		for key, raw := range properties {
			p, _ := raw.(map[string]any)
			if p["type"] == "string" {
				p["maxLength"] = 256
			}
			if key == "reason" {
				p["maxLength"] = 512
			}
		}
		properties["kind"] = enumSchema("Deployment", "StatefulSet", "DaemonSet", "Pod", "Node")
		out = append(out, biz.Tool{Name: tools.ToolNameExecuteK8sAction, Description: "Run Kubernetes dry_run=true first, then submit exactly the same parameters with the returned resource version and one-use preflight token. Submit the action, then ask the user to open approval_url and confirm once.", Scopes: []string{"k8s:read"}, ActionScopes: map[string][]string{"rollout_restart": {"k8s:execute"}, "scale": {"k8s:write"}, "delete_pod": {"k8s:delete"}, "evict_pod": {"k8s:execute"}, "cordon": {"k8s:execute"}, "uncordon": {"k8s:execute"}, "drain": {"k8s:execute"}}, Mutation: true, Schema: writeSchema(properties, "cluster_id", "action", "name", "reason"), Validate: func(a biz.Arguments) error {
			if err := requireReason(a); err != nil {
				return err
			}
			action := biz.String(a, "action")
			if action == "scale" {
				if _, ok := a["replicas"]; !ok {
					return errs.ErrInvalid
				}
			}
			if action == "scale" || action == "rollout_restart" {
				if biz.String(a, "kind") == "" {
					return errs.ErrInvalid
				}
			}
			if strings.TrimSpace(biz.String(a, "name")) == "" {
				return errs.ErrInvalid
			}
			if !biz.Bool(a, "dry_run") && (len(biz.String(a, "preflight_token")) != 43 || biz.String(a, "expected_resource_version") == "") {
				return errs.ErrInvalid
			}
			return nil
		}, Policy: func(a biz.Arguments) (biz.Policy, error) {
			action := biz.String(a, "action")
			kind := biz.String(a, "kind")
			if kind == "" {
				kind = "Node"
				if action == "delete_pod" || action == "evict_pod" {
					kind = "Pod"
				}
			}
			key := fmt.Sprintf("k8s:%d:%s:%s:%s", uint64(biz.Number(a, "cluster_id")), kind, biz.String(a, "namespace"), biz.String(a, "name"))
			return biz.Policy{Target: biz.Target{Type: "k8s", Key: key}, Remote: true}, nil
		}, RequiredScopes: func(a biz.Arguments) []string {
			if biz.Bool(a, "force") || biz.Bool(a, "disable_eviction") || biz.Bool(a, "delete_emptydir_data") {
				return []string{"k8s:delete"}
			}
			return nil
		}, Run: func(ctx context.Context, a biz.Arguments) (any, error) {
			i, _ := integrationauth.From(ctx)
			if (biz.Bool(a, "force") || biz.Bool(a, "disable_eviction") || biz.Bool(a, "delete_emptydir_data")) && !i.Allows("k8s:delete") {
				return nil, errs.ErrForbidden
			}
			return runBase(ctx, k8s, a)
		}})
	}
	return out, nil
}

func runBase(ctx context.Context, t basetool.BaseTool, a biz.Arguments) (any, error) {
	args := maps.Clone(a)
	delete(args, "idempotency_key")
	delete(args, "expected_revision")
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	out, err := t.InvokableRun(ctx, string(raw))
	if err != nil {
		return nil, err
	}
	if len(out) > 120*1024 {
		return nil, biz.ErrResultTooLarge
	}
	var data any
	if err = json.Unmarshal([]byte(out), &data); err != nil {
		return nil, err
	}
	return data, nil
}

// ProjectTools applies the same output guard to reads, write results and previews.
func ProjectTools(catalog []biz.Tool) []biz.Tool {
	for i := range catalog {
		run := catalog[i].Run
		catalog[i].Run = func(ctx context.Context, a biz.Arguments) (any, error) {
			result, err := run(ctx, a)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return nil, err
			}
			if len(raw) > 8*1024*1024 {
				return nil, biz.ErrResultTooLarge
			}
			var v any
			if err = json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			return sanitize(v), nil
		}
	}
	return catalog
}
