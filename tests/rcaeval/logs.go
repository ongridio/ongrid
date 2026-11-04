package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	toolkit "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

type logStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

func decodeStrict(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

// Tool executors consume these fixed observations without external IO.
type corpusBackend struct{ c evalCase }

func selectorMatches(selector string, labels map[string]string) (bool, error) {
	// shortcut: only equality and regexp matchers are implemented, add other operators with corpus tests when needed.
	matcher := regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|=)\s*("(?:\\.|[^"\\])*")`)
	rest := strings.TrimSpace(selector)
	matches := true
	for rest != "" {
		m := matcher.FindStringSubmatch(rest)
		if m == nil {
			return false, fmt.Errorf("unsupported corpus selector %s", selector)
		}
		value, err := strconv.Unquote(m[3])
		if err != nil {
			return false, err
		}
		if m[2] == "=" {
			matches = matches && labels[m[1]] == value
		} else {
			r, err := regexp.Compile("^(?:" + value + ")$")
			if err != nil {
				return false, err
			}
			matches = matches && r.MatchString(labels[m[1]])
		}
		rest = strings.TrimSpace(rest[len(m[0]):])
		if rest != "" {
			if !strings.HasPrefix(rest, ",") {
				return false, fmt.Errorf("unsupported corpus selector %s", selector)
			}
			rest = strings.TrimSpace(rest[1:])
			if rest == "" {
				return false, errors.New("trailing corpus selector comma")
			}
		}
	}
	return matches, nil
}
func logMatches(query string, labels map[string]string, line string) (bool, error) {
	parts := regexp.MustCompile(`(?s)^\{((?:[^"{}]|"(?:\\.|[^"\\])*")*)\}(.*)$`).FindStringSubmatch(strings.TrimSpace(query))
	if parts == nil {
		return false, fmt.Errorf("unsupported corpus LogQL %s", query)
	}
	ok, err := selectorMatches(parts[1], labels)
	if err != nil {
		return false, err
	}
	tail := strings.TrimSpace(parts[2])
	if tail == "" {
		return ok, nil
	}
	if !strings.HasPrefix(tail, "|=") && !strings.HasPrefix(tail, "|~") {
		return false, fmt.Errorf("unsupported corpus line filter %s", tail)
	}
	value, err := strconv.Unquote(strings.TrimSpace(tail[2:]))
	if err != nil {
		return false, err
	}
	if tail[:2] == "|=" {
		return ok && strings.Contains(line, value), nil
	}
	r, err := regexp.Compile(value)
	if err != nil {
		return false, err
	}
	return ok && r.MatchString(line), nil
}
func (b corpusBackend) QueryLogQL(_ context.Context, opts logquery.QueryRangeOptions) (logquery.QueryLogQLResult, error) {
	if _, err := logMatches(opts.Query, nil, ""); err != nil {
		return nil, err
	}
	if b.c.ID == "query_timeout" {
		return nil, context.DeadlineExceeded
	}
	streams := map[string]*logStream{}
	seen := map[string]bool{}
	for _, f := range b.c.Fixtures {
		if f.Tool != "query_logql" || f.Error != "" {
			continue
		}
		var raw struct {
			ResultType string      `json:"resultType"`
			Result     []logStream `json:"result"`
		}
		if err := decodeStrict(f.Result, &raw); err != nil {
			return nil, err
		}
		for _, s := range raw.Result {
			key := hashValue(s.Stream)
			for _, v := range s.Values {
				ns, err := strconv.ParseInt(v[0], 10, 64)
				if err != nil {
					return nil, err
				}
				ok, err := logMatches(opts.Query, s.Stream, v[1])
				if err != nil {
					return nil, err
				}
				identity := key + v[0] + v[1]
				if !ok || ns < opts.Start.UnixNano() || ns > opts.End.UnixNano() || seen[identity] {
					continue
				}
				seen[identity] = true
				if streams[key] == nil {
					streams[key] = &logStream{Stream: s.Stream}
				}
				streams[key].Values = append(streams[key].Values, v)
			}
		}
	}
	keys := make([]string, 0, len(streams))
	for k := range streams {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := []logStream{}
	for _, k := range keys {
		s := streams[k]
		sort.Slice(s.Values, func(i, j int) bool {
			if opts.Direction == "forward" {
				return s.Values[i][0] < s.Values[j][0]
			}
			return s.Values[i][0] > s.Values[j][0]
		})
		if len(s.Values) > opts.Limit {
			return nil, errors.New("corpus contract fixture exceeds log limit")
		}
		result = append(result, *s)
	}
	raw, err := json.Marshal(result)
	return &logquery.QueryRangeResult{ResultType: "streams", Result: raw}, err
}

// replayLogs uses only existing observations in the two audited memory cases.
// All other cases and unsupported queries retain exact fixture matching.
func replayLogs(ctx context.Context, c evalCase, args string) (fixture, bool) {
	if c.ID != "memory_oom" && c.ID != "query_timeout" {
		return fixture{}, false
	}
	var in toolkit.QueryLogQLArgs
	if err := decodeStrict([]byte(args), &in); err != nil || in.DeviceID == nil || *in.DeviceID != c.Incident.DeviceID {
		return fixture{}, false
	}
	start, startErr := time.Parse(time.RFC3339, in.Start)
	end, endErr := time.Parse(time.RFC3339, in.End)
	if startErr != nil || endErr != nil || start.After(end) {
		return fixture{}, false
	}
	output, err := basetool.WithValidation(toolkit.NewQueryLogQLTool(corpusBackend{c})).InvokableRun(ctx, args)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return fixture{ID: "timeout_logs", Tool: "query_logql", Error: "timeout"}, true
	}
	if err != nil {
		return fixture{}, false
	}
	f := fixture{ID: "filtered_logs", Tool: "query_logql", Result: json.RawMessage(output)}
	for _, known := range c.Fixtures {
		if known.Tool != "query_logql" || known.Error != "" {
			continue
		}
		var expected, actual any
		if json.Unmarshal(known.Result, &expected) == nil && json.Unmarshal(f.Result, &actual) == nil && hashValue(expected) == hashValue(actual) {
			f.ID = known.ID
			break
		}
	}
	return f, true
}
