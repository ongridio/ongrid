package llm_wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// plan is the page structure the LLM proposes for one build.
type plan struct {
	Pages []planPage `json:"pages"`
}

// planPage is one page the planner wants written.
type planPage struct {
	PageID   string        `json:"page_id"`
	Title    string        `json:"title"`
	Sections []planSection `json:"sections"`
}

// planSection is one section of a planned page. SourceIDs point at digest items
// so the evidence resolver can trace the section back to raw documents.
type planSection struct {
	Heading   string `json:"heading"`
	Content   string `json:"content"`
	SourceIDs []int  `json:"source_ids"`
}

// planResponseSchema is the strict JSON Schema the planner asks providers for.
// Providers that cannot honor it fall back to json_object mode.
const planResponseSchema = `{
	"type": "object",
	"properties": {
		"pages": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"page_id": {
						"type": "string",
						"description": "Unique identifier for the page, using kebab-case"
					},
					"title": {
						"type": "string",
						"description": "Human-readable page title"
					},
					"sections": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"heading": {
									"type": "string",
									"description": "Section heading"
								},
								"content": {
									"type": "string",
									"description": "Section content in Markdown format"
								},
								"source_ids": {
									"type": "array",
									"items": {"type": "integer", "minimum": 0},
									"description": "Zero-based Digest source_id indices that provide source material; these are not database IDs"
								}
							},
							"required": ["heading", "content", "source_ids"]
						}
					}
				},
				"required": ["page_id", "title", "sections"]
			}
		}
	},
	"required": ["pages"]
}`

// plannerSystemPrompt is the system prompt for the planning stage.
const plannerSystemPrompt = `You are a Wiki structure planner for an organization's knowledge base.

Your task is to analyze a digest of documents and create a well-organized Wiki structure.

The material may be of any kind: technical references, policies, meeting notes, contracts, reports, research, or general prose. Let the material itself decide the structure instead of assuming a technical manual — the same rules apply to a process guide, a policy, and an API reference.

Guidelines:
1. Each page should cover a coherent, self-contained topic
2. Pages should be logically ordered and cross-referenced where appropriate
3. Sections within a page should follow a logical flow
4. Preserve what the material itself treats as important — names, dates, amounts, terms, roles, decisions, steps and examples — along with any code or configuration it contains
5. Use clear, descriptive page IDs in kebab-case format
6. source_ids MUST contain only the zero-based "Digest source_id" values printed in the digest; never invent or use database IDs
7. Ground every heading and every line of content in what the digest states. Never invent an API, command, parameter, value, date, name, role or step, and never present a guess as something the material says — a plan that leaves a gap is correct, a plan that fills a gap with invention is not

` + sourceLanguageRule + `

Output valid JSON only. Do not include any explanations or markdown.`

// planner turns the reduced corpus digest into the page structure of a build.
type planner struct {
	llm CompilerLLM
	log *slog.Logger
}

// newPlanner creates a Wiki structure planner.
func newPlanner(llm CompilerLLM, log *slog.Logger) *planner {
	return &planner{llm: llm, log: log}
}

// Plan asks the LLM for a page structure and validates it locally. It asks for a
// strict JSON Schema first and falls back to json_object mode when the provider
// refuses the schema.
func (p *planner) Plan(ctx context.Context, digest string) (*plan, error) {
	p.log.InfoContext(ctx, "planner: planning Wiki structure", slog.Int("digest_length", len(digest)))

	created, err := p.planWithJSONSchema(ctx, digest)
	if err != nil {
		p.log.WarnContext(ctx, "planner: JSON schema planning failed, trying json_object",
			slog.String("error", err.Error()))
		created, err = p.planWithJSONObject(ctx, digest)
		if err != nil {
			return nil, fmt.Errorf("planner failed: %w", err)
		}
	}

	if err := validatePlan(created); err != nil {
		return nil, fmt.Errorf("invalid plan: %w", err)
	}

	p.log.InfoContext(ctx, "planner: plan created", slog.Int("pages", len(created.Pages)))
	return created, nil
}

// planWithJSONSchema asks for the plan with a JSON Schema response format.
func (p *planner) planWithJSONSchema(ctx context.Context, digest string) (*plan, error) {
	return p.requestPlan(ctx, digest, &llm.ResponseFormat{
		Type:   llm.ResponseFormatJSONSchema,
		Name:   "wiki_plan",
		Schema: json.RawMessage(planResponseSchema),
	})
}

// planWithJSONObject asks for the plan in plain json_object mode.
func (p *planner) planWithJSONObject(ctx context.Context, digest string) (*plan, error) {
	return p.requestPlan(ctx, digest, &llm.ResponseFormat{Type: llm.ResponseFormatJSONObject})
}

// requestPlan performs one planning request and decodes the response. Both
// response-format attempts share it so they stay identical apart from the format.
func (p *planner) requestPlan(ctx context.Context, digest string, format *llm.ResponseFormat) (*plan, error) {
	resp, err := p.llm.Complete(ctx, llm.ChatReq{
		Messages: []llm.Message{
			{Role: "system", Content: plannerSystemPrompt},
			{Role: "user", Content: plannerPrompt(digest)},
		},
		ResponseFormat: format,
	})
	if err != nil {
		return nil, err
	}

	var created plan
	if err := json.Unmarshal([]byte(resp.Assistant.Content), &created); err != nil {
		return nil, fmt.Errorf("decode plan: %w", err)
	}
	return &created, nil
}

// maxPageIDLength matches the page_id column of the build page table, so an
// over-long id is rejected before it can reach the database.
const maxPageIDLength = 64

// pageIDPattern is the kebab-case whitelist the planner is asked for. It also
// keeps an id usable as one artifact file name below the build directory and as
// one segment of a Wiki tree path.
var pageIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validatePlan rejects structurally unusable plans before any page is written.
func validatePlan(created *plan) error {
	if created == nil {
		return fmt.Errorf("nil plan")
	}
	if len(created.Pages) == 0 {
		return fmt.Errorf("plan has no pages")
	}

	seenPageIDs := make(map[string]bool)
	for i, page := range created.Pages {
		if err := validatePageID(page.PageID); err != nil {
			return fmt.Errorf("page %d: %w", i, err)
		}
		if page.Title == "" {
			return fmt.Errorf("page %d has empty title", i)
		}
		if seenPageIDs[page.PageID] {
			return fmt.Errorf("duplicate page_id: %s", page.PageID)
		}
		seenPageIDs[page.PageID] = true

		if len(page.Sections) == 0 {
			return fmt.Errorf("page %s has no sections", page.PageID)
		}

		for j, section := range page.Sections {
			if section.Heading == "" {
				return fmt.Errorf("page %s section %d has empty heading", page.PageID, j)
			}
			if section.Content == "" {
				return fmt.Errorf("page %s section %d has empty content", page.PageID, j)
			}
		}
	}

	return nil
}

// validatePageID enforces the page id contract: a non-empty kebab-case id that
// fits the database column. Page ids come from the LLM, so an unvalidated id
// could otherwise escape the build artifact directory as a path component.
func validatePageID(pageID string) error {
	if pageID == "" {
		return errors.New("empty page_id")
	}
	if len(pageID) > maxPageIDLength {
		return fmt.Errorf("page_id %q is longer than %d characters", pageID, maxPageIDLength)
	}
	if !pageIDPattern.MatchString(pageID) {
		return fmt.Errorf("page_id %q is not kebab-case", pageID)
	}
	return nil
}

// plannerPrompt renders the user prompt for planning.
func plannerPrompt(digest string) string {
	return fmt.Sprintf(`Based on the following digest of an organization's documents, plan a Wiki structure.

Requirements:
- Create pages that cover distinct topics
- Each page should have well-organized sections
- Include source_ids using only the zero-based "Digest source_id" values shown below
- Each entry opens with its "Digest source_id" and, when the document has one, a "Document:" title; both are labels, not content, and the title names the document the passage was taken from
- Pages should be self-contained but may reference other pages
- %s

Digest of documents:
%s

Output a JSON object with a "pages" array. Each page has:
- "page_id": unique identifier in kebab-case
- "title": human-readable title
- "sections": array of sections, each with "heading", "content" (Markdown), and "source_ids" (array of zero-based Digest source_id integers)`, sourceLanguageRule, digest)
}
