package llm_wiki

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidatePlan_RejectsUnsafePageIDs — page ids come from the LLM and are
// used as artifact file names, so anything outside the kebab-case whitelist must
// be refused before a page is written.
func TestValidatePlan_RejectsUnsafePageIDs(t *testing.T) {
	tests := []struct {
		name   string
		pageID string
	}{
		{name: "empty", pageID: ""},
		{name: "parent traversal", pageID: "../../../builds/7/pages/other"},
		{name: "nested path", pageID: "topics/dns"},
		{name: "space", pageID: "dns overview"},
		{name: "uppercase", pageID: "DNS-Overview"},
		{name: "dot segment", pageID: ".."},
		{name: "leading dash", pageID: "-dns"},
		{name: "trailing dash", pageID: "dns-"},
		{name: "extension", pageID: "dns.md"},
		{name: "longer than the column", pageID: strings.Repeat("a", maxPageIDLength+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePlan(&plan{Pages: []planPage{{
				PageID:   tt.pageID,
				Title:    "DNS",
				Sections: []planSection{{Heading: "Overview", Content: "body"}},
			}}})
			require.Error(t, err)
		})
	}
}

func TestValidatePlan_AcceptsKebabCasePageIDs(t *testing.T) {
	err := validatePlan(&plan{Pages: []planPage{
		{PageID: "dns-overview", Title: "DNS", Sections: []planSection{{Heading: "h", Content: "c"}}},
		{PageID: "2024", Title: "Archive", Sections: []planSection{{Heading: "h", Content: "c"}}},
		{PageID: strings.Repeat("a", maxPageIDLength), Title: "Long", Sections: []planSection{{Heading: "h", Content: "c"}}},
	}})
	require.NoError(t, err)
}

func TestValidatePlan_RejectsDuplicatePageIDs(t *testing.T) {
	page := planPage{PageID: "dns-overview", Title: "DNS", Sections: []planSection{{Heading: "h", Content: "c"}}}
	err := validatePlan(&plan{Pages: []planPage{page, page}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate page_id")
}
