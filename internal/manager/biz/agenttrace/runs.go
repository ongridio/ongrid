package agenttrace

import (
	"math"
	"sort"
)

func buildRuns(trace *Trace) {
	byID := map[string]int{}
	for i := range trace.Steps {
		byID[trace.Steps[i].SpanID] = i
	}
	// Repair only the display edge; Raw retains the exact reported parent.
	visited := make([]bool, len(trace.Steps))
	for i := range trace.Steps {
		path := map[int]bool{}
		for current := i; !visited[current]; {
			path[current] = true
			step := &trace.Steps[current]
			if step.ParentSpanID == "" {
				break
			}
			parent, ok := byID[step.ParentSpanID]
			if !ok {
				step.Issues = appendUnique(step.Issues, "missing_parent")
				step.ParentSpanID = ""
				break
			}
			if path[parent] {
				step.Issues = appendUnique(step.Issues, "parent_cycle")
				step.ParentSpanID = ""
				break
			}
			current = parent
		}
		for index := range path {
			visited[index] = true
		}
	}
	children := map[string][]int{}
	for i, step := range trace.Steps {
		children[step.ParentSpanID] = append(children[step.ParentSpanID], i)
	}
	type node struct {
		index, run      int
		meteredAncestor bool
		captureDisabled bool
	}
	stack := []node{}
	for _, i := range children[""] {
		stack = append(stack, node{index: i, run: -1})
	}
	members := [][]Step{}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		step := &trace.Steps[current.index]
		if capture, ok := step.Attributes["ongrid.agent.content.capture"].(bool); ok {
			current.captureDisabled = !capture
		}
		if current.captureDisabled {
			for _, value := range []*Content{&step.Input, &step.Output} {
				if value.State == "not_recorded" {
					value.State = "capture_disabled"
				}
			}
		}
		if step.SDK != "otel" && (current.run < 0 || trace.Runs[current.run].Identity != step.Identity) {
			current.run = len(trace.Runs)
			current.meteredAncestor = false
			trace.Runs = append(trace.Runs, Run{TraceID: trace.TraceID, EntrySpanID: step.SpanID, Identity: step.Identity, Name: step.Name,
				Start: step.Start, DurationMS: step.DurationMS, Status: step.Status, SessionID: step.SessionID,
				InputPreview: preview(step.Input), OutputPreview: preview(step.Output), InputState: step.Input.State, OutputState: step.Output.State,
				ChatSessionID: step.ChatSessionID, SDKs: []string{}, Models: []string{}, Kinds: []string{}, Issues: append([]string{}, trace.Issues...)})
			members = append(members, []Step{})
		}
		if current.run >= 0 {
			run := &trace.Runs[current.run]
			run.StepCount++
			run.ContentAvailable = run.ContentAvailable || step.Input.State == "available" || step.Output.State == "available"
			if step.Status == "error" {
				run.ErrorCount++
			}
			if step.SDK != "otel" {
				run.SDKs = appendUnique(run.SDKs, step.SDK)
			}
			run.Models = appendUnique(run.Models, step.Model)
			run.Kinds = appendUnique(run.Kinds, step.Kind)
			for _, issue := range step.Issues {
				run.Issues = appendUnique(run.Issues, issue)
			}
			if step.SessionID != "" {
				if run.SessionID == "" {
					run.SessionID = step.SessionID
				} else if run.SessionID != step.SessionID {
					run.Issues = appendUnique(run.Issues, "session_conflict")
				}
			}
			if step.Kind == "llm" || step.Kind == "embedding" {
				metered := hasUsage(step.Usage)
				if metered && current.meteredAncestor {
					run.Issues = appendUnique(run.Issues, "usage_overlap")
				}
				current.meteredAncestor = current.meteredAncestor || metered
				members[current.run] = append(members[current.run], *step)
			}
		}
		for _, child := range children[step.SpanID] {
			stack = append(stack, node{child, current.run, current.meteredAncestor, current.captureDisabled})
		}
	}
	for i := range trace.Runs {
		run := &trace.Runs[i]
		run.Usage = aggregateUsage(members[i])
		for _, issue := range run.Issues {
			if issue == "usage_overlap" {
				run.Usage = Usage{Conflict: true}
			}
			if issue == "session_conflict" {
				run.SessionID = ""
			}
		}
		if run.Usage.Conflict {
			run.Issues = appendUnique(run.Issues, "usage_conflict")
		}
		sort.Strings(run.SDKs)
		sort.Strings(run.Models)
		sort.Strings(run.Kinds)
		sort.Strings(run.Issues)
	}
	sort.Slice(trace.Runs, func(i, j int) bool { return trace.Runs[i].Start.After(trace.Runs[j].Start) })
}

func hasUsage(u Usage) bool {
	return u.InputTokens != nil || u.OutputTokens != nil || u.TotalTokens != nil || u.ReportedCost != nil
}

func aggregateUsage(steps []Step) Usage {
	if len(steps) == 0 {
		return Usage{}
	}
	var input, output, total, cached int64
	var cost float64
	u := Usage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total, CachedTokens: &cached, ReportedCost: &cost, Currency: "USD", Complete: true}
	responseIDs := map[string]bool{}
	for _, step := range steps {
		if id := stringAttr(step.Attributes, "gen_ai.response.id"); id != "" {
			key := step.Provider + "\x00" + step.Model + "\x00" + id
			if responseIDs[key] {
				return Usage{Conflict: true}
			}
			responseIDs[key] = true
		}
		if step.Usage.Conflict {
			return Usage{Conflict: true}
		}
		addTokens := func(dst **int64, src *int64) {
			if src == nil {
				*dst = nil
				return
			}
			if *dst != nil {
				if **dst > math.MaxInt64-*src {
					*dst = nil
					u.Conflict = true
				} else {
					**dst += *src
				}
			}
		}
		addTokens(&u.InputTokens, step.Usage.InputTokens)
		addTokens(&u.OutputTokens, step.Usage.OutputTokens)
		addTokens(&u.TotalTokens, step.Usage.TotalTokens)
		addTokens(&u.CachedTokens, step.Usage.CachedTokens)
		if step.Usage.ReportedCost == nil {
			u.ReportedCost = nil
		} else if u.ReportedCost != nil {
			*u.ReportedCost += *step.Usage.ReportedCost
			if math.IsInf(*u.ReportedCost, 0) {
				u.ReportedCost = nil
				u.Conflict = true
			}
		}
	}
	if u.ReportedCost == nil {
		u.Currency = ""
	}
	u.Complete = !u.Conflict && u.InputTokens != nil && u.OutputTokens != nil
	return u
}
