package alertdraft

import "testing"

func TestCompileDraft_NormalizesRuleAndSummary(t *testing.T) {
	got, err := CompileDraft(CompileInput{
		Action: "create",
		Rule: RuleConfigInput{
			Conditions: []RuleCondition{
				{Metric: "cpu_usage_percent", Operator: ">", Threshold: 80},
			},
		},
		RequestText: "创建 CPU 使用率超过 80% 的告警",
	})
	if err != nil {
		t.Fatalf("CompileDraft: %v", err)
	}
	if got.Action != "create" {
		t.Fatalf("Action = %q, want create", got.Action)
	}
	if got.Rule.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Rule.Kind)
	}
	if got.Rule.RuleKey != "cpu_high" {
		t.Fatalf("RuleKey = %q, want cpu_high", got.Rule.RuleKey)
	}
	if got.Rule.Name != "CPU > 80%" {
		t.Fatalf("Name = %q, want CPU > 80%%", got.Rule.Name)
	}
	if got.Summary != `create alert rule "CPU > 80%"` {
		t.Fatalf("Summary = %q", got.Summary)
	}
}
