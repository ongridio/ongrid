// Command rcaeval runs the production investigator against synthetic observations.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
)

type evalCase struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Incident caseIncident `json:"incident"`
	Fixtures []fixture    `json:"fixtures"`
	Rubric   rubric       `json:"rubric"`
	Smoke    smokeScript  `json:"smoke"`
}

type caseIncident struct {
	ID          uint64            `json:"id"`
	DeviceID    uint64            `json:"device_id"`
	Rule        string            `json:"rule"`
	Summary     string            `json:"summary"`
	Description string            `json:"description"`
	At          time.Time         `json:"at"`
	Labels      map[string]string `json:"labels,omitempty"`
}

func (c caseIncident) model() *alertmodel.Incident {
	labels, _ := json.Marshal(c.Labels)
	return &alertmodel.Incident{ID: c.ID, DeviceID: &c.DeviceID, Rule: c.Rule,
		RuleName: c.Rule, Severity: "critical", Summary: c.Summary, Description: c.Description,
		Title: c.Summary, Status: alertmodel.IncidentStatusOpen, ScopeType: alertmodel.RuleScopeHost, EventCount: 1,
		LabelsJSON: string(labels), FirstFiredAt: c.At, LastFiredAt: c.At}
}

type fixture struct {
	ID     string            `json:"id"`
	Tool   string            `json:"tool"`
	Args   []json.RawMessage `json:"args"`
	Result json.RawMessage   `json:"result,omitempty"`
	Error  string            `json:"error,omitempty"`
}

type rubric struct {
	Acceptable []string `json:"acceptable"`
	Forbidden  []string `json:"forbidden"`
	Required   []string `json:"required_observations"`
	NextSteps  []string `json:"next_steps"`
}

// Smoke replies are wiring fixtures, never a diagnostic accuracy baseline.
type smokeScript struct {
	Steps      []smokeStep     `json:"steps"`
	Specialist []smokeStep     `json:"specialist,omitempty"`
	Extraction json.RawMessage `json:"extraction"`
}

type smokeStep struct {
	Fixture string          `json:"fixture,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Content string          `json:"content,omitempty"`
}

func loadCases(dir, selected string) ([]evalCase, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []evalCase
	seen := map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var c evalCase
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("%s: invalid JSON document", path)
		}
		if err := c.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("duplicate case %s", c.ID)
		}
		seen[c.ID] = true
		if selected == "" || selected == c.ID {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no cases selected in %s (case=%q)", dir, selected)
	}
	return out, nil
}

func (c evalCase) validate() error {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`).MatchString(c.ID) || c.Title == "" || c.Incident.ID == 0 || c.Incident.DeviceID == 0 || c.Incident.At.IsZero() {
		return fmt.Errorf("case needs a safe ID, title, incident ID, device ID and timestamp")
	}
	if len(c.Rubric.Acceptable) == 0 || len(c.Rubric.Forbidden) == 0 || len(c.Rubric.Required) == 0 || len(c.Rubric.NextSteps) == 0 {
		return fmt.Errorf("all rubric sections must be populated")
	}
	ids, matches := map[string]bool{}, map[string]string{}
	for _, f := range c.Fixtures {
		if f.ID == "" || ids[f.ID] || f.Tool == "" || len(f.Args) == 0 || (len(f.Result) == 0) == (f.Error == "") {
			return fmt.Errorf("fixture %q needs a unique ID, tool, args, and exactly one result or error", f.ID)
		}
		ids[f.ID] = true
		for _, a := range f.Args {
			key, err := queryKey(f.Tool, a)
			if err != nil {
				return fmt.Errorf("fixture %s: %w", f.ID, err)
			}
			if prior := matches[key]; prior != "" && prior != f.ID {
				return fmt.Errorf("ambiguous fixture query: %s", key)
			}
			matches[key] = f.ID
		}
	}
	for _, id := range c.Rubric.Required {
		if !ids[id] {
			return fmt.Errorf("required observation %q has no fixture", id)
		}
	}
	for _, steps := range [][]smokeStep{c.Smoke.Steps, c.Smoke.Specialist} {
		for _, s := range steps {
			if s.Fixture != "" && !ids[s.Fixture] {
				return fmt.Errorf("smoke uses missing fixture %s", s.Fixture)
			}
		}
	}
	if len(c.Smoke.Steps) == 0 || !json.Valid(c.Smoke.Extraction) {
		return fmt.Errorf("missing smoke script")
	}
	return nil
}
