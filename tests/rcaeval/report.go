package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type review struct {
	RunID                string `json:"run_id"`
	CaseHash             string `json:"case_sha256"`
	ResultHash           string `json:"result_sha256"`
	Verdict              string `json:"verdict"`
	Reviewer             string `json:"reviewer"`
	RootCauseSupported   string `json:"root_cause_supported"`
	UncertaintyHandled   string `json:"uncertainty_handled"`
	TargetAndTimeCorrect string `json:"target_and_time_correct"`
	NextStepUseful       string `json:"next_step_useful"`
	Notes                string `json:"notes"`
}

func readJSON(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func readRuns(dir string) ([]runResult, map[string]review, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*", "result.json"))
	if err != nil {
		return nil, nil, err
	}
	var runs []runResult
	reviews := map[string]review{}
	for _, p := range paths {
		var r runResult
		if err := readJSON(p, &r); err != nil {
			return nil, nil, err
		}
		if r.ID != filepath.Base(filepath.Dir(p)) {
			return nil, nil, fmt.Errorf("%s: result ID does not match its directory", p)
		}
		var v review
		if err := readJSON(filepath.Join(filepath.Dir(p), "review.json"), &v); err != nil {
			return nil, nil, err
		}
		if err := validateReview(r, v); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		runs = append(runs, r)
		reviews[r.ID] = v
	}
	return runs, reviews, nil
}

func validateReview(r runResult, v review) error {
	if v.RunID != r.ID || v.CaseHash != r.CaseHash || v.ResultHash != hashValue(r) {
		return fmt.Errorf("review belongs to another run or case version")
	}
	switch v.Verdict {
	case "pending":
		return nil
	case "pass", "fail", "unscorable":
	default:
		return fmt.Errorf("review verdict must be pending/pass/fail/unscorable")
	}
	if strings.TrimSpace(v.Reviewer) == "" || strings.TrimSpace(v.Notes) == "" {
		return fmt.Errorf("completed reviews need reviewer and notes")
	}
	for _, a := range []string{v.RootCauseSupported, v.UncertaintyHandled, v.TargetAndTimeCorrect, v.NextStepUseful} {
		if a != "yes" && a != "no" && a != "uncertain" {
			return fmt.Errorf("review dimensions must be yes/no/uncertain")
		}
		if v.Verdict == "pass" && a != "yes" {
			return fmt.Errorf("pass needs yes on all review dimensions")
		}
	}
	if v.Verdict != "unscorable" && r.Status != "awaiting_review" {
		return fmt.Errorf("resolve harness/model/coverage issues before assigning a diagnostic verdict")
	}
	return nil
}

func renderReport(dir, baseline string) error {
	var m manifest
	if err := readJSON(filepath.Join(dir, "manifest.json"), &m); err != nil {
		return err
	}
	runs, reviews, err := readRuns(dir)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# RCA evaluation\n\nMode: **%s** · Model: `%s` · Commit: `%s`\n\nSource fingerprint: `%s`\n\nCompleted: %d / %d. Stop reason: %s\n\n", m.Config.Mode, m.Config.Model, m.Commit, m.SourceHash, m.Completed, len(m.Cases)*m.Repeat, m.StopReason)
	if m.Config.Mode == "smoke" {
		b.WriteString("**Scripted smoke run. This verifies wiring and provenance only; it does not measure model diagnostic quality.**\n\n")
	}
	b.WriteString("Token thresholds stop subsequent calls; a response can exceed the threshold. Zero or incomplete usage is unknown, not free. No monetary cost is inferred.\n\n")
	b.WriteString("| Case / repeat | Execution | Review | Model calls | Tools (requested / executed) | Repeats | Tokens | Time (s) |\n|---|---|---|---:|---:|---:|---:|---:|\n")
	for _, r := range runs {
		usage := fmt.Sprint(r.Usage.TotalTokens)
		if !r.UsageComplete {
			usage += " (incomplete)"
		}
		fmt.Fprintf(&b, "| [%s](%s/review.md) | %s | %s | %d | %d / %d | %d | %s | %.1f |\n", r.ID, r.ID, r.Status, reviews[r.ID].Verdict, r.ModelCalls, r.ToolCalls, r.ToolExecutions, r.RepeatedQueries, usage, float64(r.ElapsedMS)/1000)
		if err := writeRunReview(filepath.Join(dir, r.ID), r, reviews[r.ID]); err != nil {
			return err
		}
	}
	var models, requested, executed, tokens int
	for _, r := range runs {
		models += r.ModelCalls
		requested += r.ToolCalls
		executed += r.ToolExecutions
		tokens += r.Usage.TotalTokens
	}
	fmt.Fprintf(&b, "\nTotals: %d model calls, %d requested tools / %d executions, %d reported tokens. Totals include unsuccessful runs.\n", models, requested, executed, tokens)
	if baseline != "" {
		var previous manifest
		if err := readJSON(filepath.Join(baseline, "manifest.json"), &previous); err != nil {
			return err
		}
		old, oldReviews, err := readRuns(baseline)
		if err != nil {
			return err
		}
		if len(old) == 0 {
			return fmt.Errorf("baseline has no results")
		}
		byID := map[string]runResult{}
		for _, r := range old {
			byID[r.ID] = r
		}
		b.WriteString("\n## Baseline comparison\n\nPairs use case ID + repeat; this is a run comparison, not statistical significance. Changed case data or execution mode is not comparable.\n\n| Run | Review before → after | Execution before → after | Δ model calls | Δ tools | Δ tokens | Δ seconds |\n|---|---|---|---:|---:|---:|---:|\n")
		for _, r := range runs {
			prior, ok := byID[r.ID]
			if !ok {
				fmt.Fprintf(&b, "| %s | no baseline | — | — | — | — | — |\n", r.ID)
				continue
			}
			if prior.CaseHash != r.CaseHash || prior.Config.Mode != r.Config.Mode {
				fmt.Fprintf(&b, "| %s | not comparable | — | — | — | — | — |\n", r.ID)
				continue
			}
			tokens := "unknown"
			if prior.UsageComplete && r.UsageComplete {
				tokens = fmt.Sprintf("%+d", r.Usage.TotalTokens-prior.Usage.TotalTokens)
			}
			fmt.Fprintf(&b, "| %s | %s → %s | %s → %s | %+d | %+d | %s | %+.1f |\n", r.ID, oldReviews[prior.ID].Verdict, reviews[r.ID].Verdict, prior.Status, r.Status, r.ModelCalls-prior.ModelCalls, r.ToolCalls-prior.ToolCalls, tokens, float64(r.ElapsedMS-prior.ElapsedMS)/1000)
		}
		fmt.Fprintf(&b, "\nBaseline model: `%s`; commit: `%s`; source: `%s`. Inspect both manifests for all parameter/budget differences.\n", previous.Config.Model, previous.Commit, previous.SourceHash)
	}
	b.WriteString("\nEdit each run's `review.json`, then rerun with `-mode report -out <directory>`. The rubric stays outside model prompts. Citation checks verify provenance, not causal truth. Missing observations require review and are not a keyword-based score.\n")
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(b.String()), 0600)
}

func writeRunReview(dir string, r runResult, v review) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nExecution: **%s** · Review: **%s** · Sessions: %d\n\n", r.Title, r.Status, v.Verdict, r.Sessions)
	for _, section := range []struct {
		name   string
		values []string
	}{{"Acceptable conclusions", r.Rubric.Acceptable}, {"Unsupported conclusions", r.Rubric.Forbidden}, {"Useful next steps", r.Rubric.NextSteps}, {"Missing observations", r.MissingObservations}, {"Automatic findings", r.Issues}} {
		fmt.Fprintf(&b, "## %s\n\n", section.name)
		for _, s := range section.values {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		b.WriteByte('\n')
	}
	if r.Report != nil {
		fmt.Fprintf(&b, "## Structured root cause\n\n%s\n\n## Investigation narrative\n\n%s\n\n## Evidence check\n\n", r.Report.RootCause, r.Report.FindingsMD)
		if r.Report.EvidenceCheckJSON != nil {
			b.WriteString("```json\n" + *r.Report.EvidenceCheckJSON + "\n```\n\n")
		}
	}
	for _, q := range r.Queries {
		fmt.Fprintf(&b, "### %s → %s\n\nSession: `%s` · Error: `%s`\n\n```json\n%s\n```\n\n", q.Tool, q.Fixture, q.SessionID, q.Error, q.Arguments)
	}
	b.WriteString("[Model requests and replies](model_calls.json) · [Session transcript](sessions.json) · [Result](result.json) · [Review form](review.json)\n")
	return os.WriteFile(filepath.Join(dir, "review.md"), []byte(b.String()), 0600)
}
