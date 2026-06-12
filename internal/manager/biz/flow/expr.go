// expr.go — the {{ ... }} template resolver and the tiny condition
// evaluator. Deliberately small: paths + literals + one comparison,
// no script engine. Anything smarter belongs in an agent / set node.
package flow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RunContext is the shared state a run accumulates. Guarded by the
// engine's mutex — executors receive resolved values, never the map.
type RunContext struct {
	// Trigger is the trigger payload ({{trigger.x}}).
	Trigger map[string]any
	// Nodes maps node id → its data output ({{nodes.<id>.output.<path>}}).
	Nodes map[string]any
	// Vars holds set-node variables ({{vars.<name>}}).
	Vars map[string]any
}

var tmplRe = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// ResolveString substitutes every {{path}} in s. A template that is the
// ENTIRE string resolves to the referenced value's native type (so a
// tool arg can receive a number / object, not its string form); mixed
// text renders values with fmt/JSON.
func (c *RunContext) ResolveString(s string) (any, error) {
	matches := tmplRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	// Whole-string single template → native value.
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(s) {
		return c.lookup(strings.TrimSpace(s[matches[0][2]:matches[0][3]]))
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		v, err := c.lookup(strings.TrimSpace(s[m[2]:m[3]]))
		if err != nil {
			return nil, err
		}
		b.WriteString(stringify(v))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

// ResolveValue walks an arbitrary decoded-JSON value resolving every
// string leaf. Used to resolve a node's whole config object.
func (c *RunContext) ResolveValue(v any) (any, error) {
	switch t := v.(type) {
	case string:
		return c.ResolveString(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			r, err := c.ResolveValue(vv)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			r, err := c.ResolveValue(vv)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

// lookup resolves a dotted path rooted at trigger / nodes / vars.
// nodes.<id>.output.<rest> reads into that node's data output.
func (c *RunContext) lookup(path string) (any, error) {
	parts := strings.Split(path, ".")
	var cur any
	switch parts[0] {
	case "trigger":
		cur = anyMap(c.Trigger)
		parts = parts[1:]
	case "nodes":
		if len(parts) < 3 || parts[2] != "output" {
			return nil, fmt.Errorf("expr: %q — node refs are nodes.<id>.output.<path>", path)
		}
		v, ok := c.Nodes[parts[1]]
		if !ok {
			return nil, fmt.Errorf("expr: node %q has no output yet (not upstream?)", parts[1])
		}
		cur = v
		parts = parts[3:]
	case "vars":
		cur = anyMap(c.Vars)
		parts = parts[1:]
	default:
		return nil, fmt.Errorf("expr: unknown root %q (want trigger/nodes/vars)", parts[0])
	}
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expr: %q — %q is not an object", path, p)
		}
		cur, ok = m[p]
		if !ok {
			return nil, fmt.Errorf("expr: %q — field %q missing", path, p)
		}
	}
	return cur, nil
}

func anyMap(m map[string]any) any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// EvalCondition evaluates `lhs OP rhs` where each side is a template,
// quoted string, number, or bool. Supported OPs: == != > >= < <=
// contains. A bare side with no operator is truthy-tested.
func (c *RunContext) EvalCondition(expr string) (bool, error) {
	ops := []string{"==", "!=", ">=", "<=", ">", "<", " contains "}
	for _, op := range ops {
		if i := strings.Index(expr, op); i >= 0 {
			l, err := c.evalOperand(strings.TrimSpace(expr[:i]))
			if err != nil {
				return false, err
			}
			r, err := c.evalOperand(strings.TrimSpace(expr[i+len(op):]))
			if err != nil {
				return false, err
			}
			return compare(l, r, strings.TrimSpace(op))
		}
	}
	v, err := c.evalOperand(strings.TrimSpace(expr))
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

func (c *RunContext) evalOperand(s string) (any, error) {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1], nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	if s == "true" {
		return true, nil
	}
	if s == "false" {
		return false, nil
	}
	return c.ResolveString(s)
}

func compare(l, r any, op string) (bool, error) {
	if op == "contains" {
		return strings.Contains(stringify(l), stringify(r)), nil
	}
	lf, lok := toFloat(l)
	rf, rok := toFloat(r)
	if lok && rok {
		switch op {
		case "==":
			return lf == rf, nil
		case "!=":
			return lf != rf, nil
		case ">":
			return lf > rf, nil
		case ">=":
			return lf >= rf, nil
		case "<":
			return lf < rf, nil
		case "<=":
			return lf <= rf, nil
		}
	}
	switch op {
	case "==":
		return stringify(l) == stringify(r), nil
	case "!=":
		return stringify(l) != stringify(r), nil
	}
	return false, fmt.Errorf("expr: cannot %s non-numeric values", op)
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != "" && t != "false" && t != "0"
	case float64:
		return t != 0
	default:
		return true
	}
}
