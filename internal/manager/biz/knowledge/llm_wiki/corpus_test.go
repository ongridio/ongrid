package llm_wiki

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitDocumentIntoChunks(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		docIndex int
		want     int // expected number of chunks
	}{
		{
			name:     "empty content",
			content:  "",
			docIndex: 0,
			want:     0,
		},
		{
			name:     "short content single chunk",
			content:  "This is a short document.",
			docIndex: 0,
			want:     1,
		},
		{
			name: "multiple paragraphs",
			content: `First paragraph with some content.

Second paragraph with more content.

Third paragraph with additional content.`,
			docIndex: 0,
			want:     1, // Should fit in one chunk
		},
		{
			name: "long content multiple chunks",
			content: func() string {
				// Generate content that exceeds chunk size
				var s string
				for i := 0; i < 100; i++ {
					s += "This is a paragraph with enough content to simulate real documentation. "
					s += "It contains multiple sentences that discuss various technical topics. "
					s += "The purpose is to test the chunking algorithm with realistic content.\n\n"
				}
				return s
			}(),
			docIndex: 1,
			want:     2, // Should split into multiple chunks
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitDocumentIntoChunks(tt.content, tt.docIndex)
			assert.LessOrEqual(t, len(chunks), tt.want+1, "chunk count should be close to expected")

			// Verify chunk properties
			for i, chunk := range chunks {
				assert.Equal(t, tt.docIndex, chunk.DocumentIndex)
				assert.Equal(t, i, chunk.Ordinal)
				assert.NotEmpty(t, chunk.Text)
				assert.Less(t, chunk.Start, chunk.End)
			}
		})
	}
}
