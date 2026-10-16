package agenttrace

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type Query struct {
	Start            time.Time
	End              time.Time
	ServiceName      string
	ServiceNamespace *string
	Environment      *string
	SDK              string
	Model            string
	Kind             string
	Status           string
	SessionID        string
	Search           string
}

func (q Query) Validate() error {
	if q.Start.IsZero() || q.End.IsZero() || !q.End.After(q.Start) || q.End.Sub(q.Start) > 7*24*time.Hour {
		return fmt.Errorf("%w: query window must be positive and at most 7 days", errs.ErrInvalid)
	}
	values := []string{q.ServiceName, q.SDK, q.Model, q.Kind, q.Status, q.SessionID, q.Search}
	if q.ServiceNamespace != nil {
		values = append(values, *q.ServiceNamespace)
	}
	if q.Environment != nil {
		values = append(values, *q.Environment)
	}
	for _, value := range values {
		if len(value) > 1024 || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: filter exceeds 1024 bytes or contains NUL", errs.ErrInvalid)
		}
	}
	if !slices.Contains([]string{"", "langsmith", "openinference", "openllmetry", "langfuse", "gen_ai"}, q.SDK) || !slices.Contains([]string{"", "agent", "workflow", "llm", "tool", "retriever", "embedding", "reranker", "span"}, q.Kind) || !slices.Contains([]string{"", "ok", "error", "unset"}, q.Status) {
		return fmt.Errorf("%w: unknown SDK, step kind, or status", errs.ErrInvalid)
	}
	if q.SessionID != "" && (q.ServiceName == "" || q.ServiceNamespace == nil || q.Environment == nil) {
		return fmt.Errorf("%w: session queries require a complete application identity", errs.ErrInvalid)
	}
	return nil
}

func TraceQL(q Query) string {
	conditions := []string{"(span.openinference.span.kind != nil || span.langsmith.span.kind != nil || span.traceloop.span.kind != nil || span.langfuse.observation.type != nil || span.gen_ai.operation.name != nil || span.gen_ai.request.model != nil || span.gen_ai.system != nil || span.gen_ai.provider.name != nil)"}
	if q.ServiceName != "" {
		conditions = append(conditions, "resource.service.name = "+strconv.Quote(q.ServiceName))
	}
	if q.ServiceNamespace != nil {
		conditions = append(conditions, optionalAttribute("resource.service.namespace", *q.ServiceNamespace))
	}
	if q.Environment != nil {
		value := strconv.Quote(*q.Environment)
		conditions = append(conditions, "("+optionalAttribute("resource.deployment.environment.name", *q.Environment)+" || (resource.deployment.environment.name = nil && resource.deployment.environment = "+value+") || (resource.deployment.environment.name = nil && resource.deployment.environment = nil && span.langfuse.environment = "+value+"))")
	}
	// Run filters are applied after decoding: model and SDK can live on a child
	// with a different resource. Constraining one span would hide valid runs.
	return "{ " + strings.Join(conditions, " && ") + " } with (most_recent = true)"
}

func optionalAttribute(key, value string) string {
	if value == "" {
		return "(" + key + " = nil || " + key + ` = "")`
	}
	return key + " = " + strconv.Quote(value)
}

func (q Query) matches(run Run) bool {
	if run.Start.Before(q.Start) || run.Start.After(q.End) {
		return false
	}
	if q.ServiceName != "" && run.Identity.ServiceName != q.ServiceName {
		return false
	}
	if q.ServiceNamespace != nil && run.Identity.ServiceNamespace != *q.ServiceNamespace {
		return false
	}
	if q.Environment != nil && run.Identity.Environment != *q.Environment {
		return false
	}
	if q.SDK != "" && !slices.Contains(run.SDKs, q.SDK) {
		return false
	}
	if q.Model != "" && !slices.Contains(run.Models, q.Model) {
		return false
	}
	if q.Kind != "" && !slices.Contains(run.Kinds, q.Kind) {
		return false
	}
	if q.Status != "" && run.Status != q.Status {
		return false
	}
	if q.SessionID != "" && run.SessionID != q.SessionID {
		return false
	}
	return q.Search == "" || strings.Contains(strings.ToLower(run.Name+" "+run.TraceID+" "+run.SessionID+" "+run.InputPreview+" "+run.OutputPreview), strings.ToLower(q.Search))
}
