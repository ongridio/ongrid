package externalmcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/jsonschema-go/jsonschema"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	service "github.com/ongridio/ongrid/internal/manager/service/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

// New HTTP routes stay private until their scope, transport and effects are reviewed.
//
//go:embed api_catalog.json
var apiCatalogJSON []byte

type apiOperation struct {
	ID          string          `json:"id"`
	Module      string          `json:"module"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Scope       string          `json:"scope"`
	Description string          `json:"description"`
	Query       []string        `json:"query"`
	BodySchema  json.RawMessage `json:"body_schema"`
	Source      string          `json:"source"`
	schema      json.RawMessage
	resolved    *jsonschema.Resolved
}

var pathParameter = regexp.MustCompile(`\{([^{}]+)\}`)
var pathSegment = regexp.MustCompile(`^[\pL\pN_:.@+\- ]{1,256}$`)

func (op apiOperation) mutation() bool { return !strings.HasSuffix(op.Scope, ":read") }
func (op apiOperation) scopes() []string {
	if op.mutation() {
		return []string{op.Module + ":read", op.Scope}
	}
	return []string{op.Scope}
}

// APITools uses the console's protected handlers and role middleware. Engine
// authenticates the PAT and supplies its current owner; no JWT is minted.
func APITools(router chi.Router, writesEnabled bool) ([]biz.Tool, error) {
	var entries []apiOperation
	if err := json.Unmarshal(apiCatalogJSON, &entries); err != nil {
		return nil, err
	}
	routes := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		return nil, err
	}
	ops := map[string]apiOperation{}
	readScopes, writeScopes, allScopes := map[string][]string{}, map[string][]string{}, map[string][]string{}
	for _, op := range entries {
		if !routes[op.Method+" "+op.Path] || (op.mutation() && !writesEnabled) {
			continue
		}
		if _, exists := ops[op.ID]; exists || !slices.Contains(integrationauth.Scopes(), op.Scope) {
			return nil, fmt.Errorf("invalid external API operation %s", op.ID)
		}
		var err error
		op.schema, err = operationSchema(op)
		if err != nil {
			return nil, err
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(op.schema, &schema); err != nil {
			return nil, err
		}
		op.resolved, err = schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.ID, err)
		}
		ops[op.ID], allScopes[op.ID] = op, op.scopes()
		if op.mutation() {
			writeScopes[op.ID] = op.scopes()
		} else {
			readScopes[op.ID] = op.scopes()
		}
	}
	lookup := func(a biz.Arguments) (apiOperation, error) {
		op, ok := ops[biz.String(a, "action")]
		if !ok {
			return op, errs.ErrInvalid
		}
		return op, nil
	}
	validate := func(a biz.Arguments) error {
		op, err := lookup(a)
		if err != nil {
			return err
		}
		if err := op.resolved.Validate(a); err != nil {
			return errs.ErrInvalid
		}
		if _, err := apiPath(op, a); err != nil {
			return err
		}
		if op.mutation() && (strings.TrimSpace(biz.String(a, "reason")) == "" || secretbox.KeyIsWeak()) {
			return errs.ErrInvalid
		}
		return nil
	}
	tools := []biz.Tool{{
		Name: "list_api_operations", Description: "Discover Ongrid API operations permitted by this PAT across product modules. Filter by module or search. Then use describe_api_operation for parameter schemas. Missing scopes or unavailable modules are omitted.",
		Schema: json.RawMessage(`{"type":"object","properties":{"module":{"type":"string","maxLength":64},"search":{"type":"string","maxLength":128},"offset":{"type":"integer","minimum":0,"maximum":10000},"limit":{"type":"integer","minimum":1,"maximum":100}},"additionalProperties":false}`),
		Run: func(ctx context.Context, a biz.Arguments) (any, error) {
			i, _ := integrationauth.From(ctx)
			items := []map[string]any{}
			for _, op := range ops {
				if !i.Allows(op.scopes()...) || (biz.String(a, "module") != "" && biz.String(a, "module") != op.Module) || !strings.Contains(strings.ToLower(op.ID+" "+op.Description), strings.ToLower(biz.String(a, "search"))) {
					continue
				}
				items = append(items, map[string]any{"action": op.ID, "module": op.Module, "description": op.Description, "scopes": op.scopes(), "mutation": op.mutation()})
			}
			sort.Slice(items, func(i, j int) bool { return items[i]["action"].(string) < items[j]["action"].(string) })
			total, offset, limit := len(items), int(biz.Number(a, "offset")), int(biz.Number(a, "limit"))
			if limit == 0 {
				limit = 50
			}
			start, end := min(offset, total), min(offset+limit, total)
			return map[string]any{"items": items[start:end], "total": total, "offset": offset, "limit": limit, "has_more": end < total}, nil
		},
	}}
	if len(allScopes) == 0 {
		return tools, nil
	}
	tools = append(tools, biz.Tool{Name: "describe_api_operation", Description: "Get one permitted action's path, query and JSON body schema. Wire types are described here; the existing handler enforces required values and business rules.", ActionScopes: allScopes, Schema: apiToolSchema(allScopes, false, true), Run: func(_ context.Context, a biz.Arguments) (any, error) {
		op, err := lookup(a)
		if err != nil {
			return nil, err
		}
		tool := "call_api_read"
		if op.mutation() {
			tool = "call_api_write"
		}
		return map[string]any{"action": op.ID, "tool": tool, "description": op.Description, "method": op.Method, "path_template": op.Path, "scopes": op.scopes(), "input_schema": op.schema, "approval_required": op.mutation() && !op.lowRisk(nil), "approval_policy": "Routine creation and metadata edits skip confirmation. Risky changes require confirmation; dry_run reports the decision for the supplied body.", "dry_run": "request shape validation only; does not invoke the handler", "verification": "HTTP response only; poll resource-specific jobs for completion", "source": op.Source}, nil
	}})
	for _, mutation := range []bool{false, true} {
		scopes, name := readScopes, "call_api_read"
		if mutation {
			scopes, name = writeScopes, "call_api_write"
		}
		if len(scopes) == 0 {
			continue
		}
		tool := biz.Tool{Name: name, Description: "Call a fixed Ongrid API action after discovery. No arbitrary URL, method or headers. Responses are bounded, redacted JSON. Routine writes execute directly. High-risk writes wait for the user to open approval_url and confirm once. Every write requires idempotency_key and reason; dry_run only validates request shape. Poll accepted jobs through their resource API.", ActionScopes: scopes, Mutation: mutation, Sensitive: mutation, Verification: "accepted_by_api", Schema: apiToolSchema(scopes, mutation, false), Validate: validate}
		if mutation {
			tool.Policy = func(a biz.Arguments) (biz.Policy, error) {
				op, err := lookup(a)
				if err != nil {
					return biz.Policy{}, err
				}
				path, err := apiPath(op, a)
				return biz.Policy{Target: biz.Target{Type: "api", Key: "api:" + path}, Remote: true, LowRisk: op.lowRisk(a)}, err
			}
		}
		tool.Run = func(ctx context.Context, a biz.Arguments) (any, error) {
			op, err := lookup(a)
			if err != nil {
				return nil, err
			}
			i, ok := integrationauth.From(ctx)
			if !ok || i.Owner.Role != "admin" || !i.Allows(op.scopes()...) {
				return nil, errs.ErrForbidden
			}
			if biz.Bool(a, "dry_run") {
				return map[string]any{"action": op.ID, "applied": false, "approval_required": op.mutation() && !op.lowRisk(a), "validation": "request_shape_only", "request": reviewAPIArguments(a)}, nil
			}
			return executeAPI(ctx, router, op, a)
		}
		tools = append(tools, service.ProjectTools([]biz.Tool{tool})...)
	}
	return tools, nil
}

func apiToolSchema(scopes map[string][]string, mutation, describe bool) json.RawMessage {
	names := make([]string, 0, len(scopes))
	for name := range scopes {
		names = append(names, name)
	}
	sort.Strings(names)
	props := map[string]any{"action": map[string]any{"type": "string", "enum": names}}
	required := []string{"action"}
	if !describe {
		props["path"] = map[string]any{"type": "object", "maxProperties": 8, "additionalProperties": map[string]any{"type": "string", "maxLength": 256}}
		props["query"] = map[string]any{"type": "object", "maxProperties": 64, "additionalProperties": map[string]any{"type": "string", "maxLength": 8192}}
		props["body"] = map[string]any{"type": "object"}
	}
	if mutation {
		props["idempotency_key"] = map[string]any{"type": "string", "minLength": 8, "maxLength": 128}
		props["reason"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 512}
		props["dry_run"] = map[string]any{"type": "boolean"}
		required = append(required, "idempotency_key", "reason")
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
	return raw
}

func operationSchema(op apiOperation) (json.RawMessage, error) {
	var schema map[string]any
	if err := json.Unmarshal(apiToolSchema(map[string][]string{op.ID: op.scopes()}, op.mutation(), false), &schema); err != nil {
		return nil, err
	}
	props := schema["properties"].(map[string]any)
	params, required := map[string]any{}, []string{}
	for _, match := range pathParameter.FindAllStringSubmatch(op.Path, -1) {
		params[match[1]] = map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
		required = append(required, match[1])
	}
	props["path"] = map[string]any{"type": "object", "properties": params, "required": required, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = append(schema["required"].([]any), "path")
	}
	query := map[string]any{}
	for _, key := range op.Query {
		query[key] = map[string]any{"type": "string", "maxLength": 8192}
	}
	props["query"] = map[string]any{"type": "object", "properties": query, "additionalProperties": false}
	if len(op.BodySchema) > 0 && string(op.BodySchema) != "null" {
		props["body"] = op.BodySchema
	} else {
		delete(props, "body")
	}
	return json.Marshal(schema)
}

func apiPath(op apiOperation, a biz.Arguments) (string, error) {
	path := op.Path
	params, _ := a["path"].(map[string]any)
	for _, match := range pathParameter.FindAllStringSubmatch(path, -1) {
		value, _ := params[match[1]].(string)
		if !pathSegment.MatchString(value) || value == "." || value == ".." || strings.TrimSpace(value) != value {
			return "", errs.ErrInvalid
		}
		path = strings.ReplaceAll(path, match[0], value)
	}
	return path, nil
}

func executeAPI(ctx context.Context, router chi.Router, op apiOperation, a biz.Arguments) (any, error) {
	path, err := apiPath(op, a)
	if err != nil {
		return nil, err
	}
	route := chi.NewRouteContext()
	if !router.Match(route, op.Method, path) || route.RoutePattern() != op.Path {
		return nil, errs.ErrForbidden
	}
	query := url.Values{}
	if values, ok := a["query"].(map[string]any); ok {
		for key, raw := range values {
			value, _ := raw.(string)
			if slices.Contains([]string{"limit", "page_size", "top_n"}, key) {
				n, parseErr := strconv.Atoi(value)
				if parseErr != nil || n < 1 || n > 100 {
					return nil, errs.ErrInvalid
				}
			}
			query.Set(key, value)
		}
	}
	if slices.Contains(op.Query, "limit") && !query.Has("limit") {
		query.Set("limit", "50")
	}
	body := []byte(`{}`)
	if value, ok := a["body"]; ok {
		body, err = json.Marshal(value)
		if err != nil {
			return nil, errs.ErrInvalid
		}
	}
	u := &url.URL{Path: path, RawQuery: query.Encode()}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, chi.NewRouteContext())
	req, err := http.NewRequestWithContext(ctx, op.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errs.ErrInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w := &apiResponse{headers: http.Header{}}
	router.ServeHTTP(w, req)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.tooLarge {
		return nil, biz.ErrResultTooLarge
	}
	if w.status >= 300 {
		if op.mutation() {
			// A handler can fail after an external side effect; do not claim rollback.
			return nil, fmt.Errorf("API write returned HTTP %d; inspect the target", w.status)
		}
		switch w.status {
		case 400, 422:
			return nil, errs.ErrInvalid
		case 401, 403:
			return nil, errs.ErrForbidden
		case 404:
			return nil, errs.ErrNotFound
		case 409:
			return nil, errs.ErrConflict
		default:
			return nil, fmt.Errorf("API returned HTTP %d", w.status)
		}
	}
	var data any
	if w.body.Len() > 0 && json.Unmarshal(w.body.Bytes(), &data) != nil {
		return nil, fmt.Errorf("API response is not JSON")
	}
	return map[string]any{"action": op.ID, "http_status": w.status, "result": data, "verification": "api_response"}, nil
}

type apiResponse struct {
	headers  http.Header
	body     bytes.Buffer
	status   int
	tooLarge bool
}

func (w *apiResponse) Header() http.Header { return w.headers }
func (w *apiResponse) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *apiResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.tooLarge || w.body.Len()+len(p) > 128*1024 {
		w.tooLarge = true
		return 0, biz.ErrResultTooLarge
	}
	return w.body.Write(p)
}

func reviewAPIArguments(a biz.Arguments) biz.Arguments {
	// Configuration can hide secrets under arbitrary names; previews expose only fields and a digest.
	body, _ := json.Marshal(a["body"])
	digest := sha256.Sum256(body)
	fields := []string{}
	if values, ok := a["body"].(map[string]any); ok {
		for key := range values {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return biz.Arguments{"action": a["action"], "path": a["path"], "reason": a["reason"], "body_fields": fields, "body_sha256": hex.EncodeToString(digest[:]), "body": "[ENCRYPTED]"}
}
