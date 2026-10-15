package llm_wiki

import "fmt"

// resolvedPage is a planned page with its source references resolved.
type resolvedPage struct {
	PageID   string
	Title    string
	Sections []resolvedSection
	Sources  []resolvedSource
}

// resolvedSection is one section of a resolved page.
type resolvedSection struct {
	Heading string
	Content string
	Sources []resolvedSource
}

// resolvedSource is one source document a page cites, with the metadata the
// page footer and the build records need.
type resolvedSource struct {
	SourceID        uint64
	SourceVersionID uint64
	ChunkOrdinal    int
	ContentHash     string
	DocumentTitle   string
}

// evidenceResolver turns the digest indices a plan references back into the
// documents and chunks they came from.
type evidenceResolver struct{}

// newEvidenceResolver creates an evidence resolver.
func newEvidenceResolver() *evidenceResolver {
	return &evidenceResolver{}
}

// Resolve resolves source references for every page in the plan.
func (r *evidenceResolver) Resolve(planned *plan, c *corpus, items []digestItem) ([]*resolvedPage, error) {
	index := newCorpusIndex(c)
	pages := make([]*resolvedPage, 0, len(planned.Pages))

	for _, page := range planned.Pages {
		resolved, err := resolvePlanPage(page, index, items)
		if err != nil {
			return nil, fmt.Errorf("resolve page %s: %w", page.PageID, err)
		}
		pages = append(pages, resolved)
	}

	return pages, nil
}

// resolvePlanPage resolves one planned page: section source indices point at
// digest items, digest items point at source documents.
func resolvePlanPage(planned planPage, index *corpusIndex, items []digestItem) (*resolvedPage, error) {
	resolved := &resolvedPage{PageID: planned.PageID, Title: planned.Title}
	seenSources := make(map[uint64]struct{})

	for _, section := range planned.Sections {
		resolvedSection := resolvedSection{Heading: section.Heading, Content: section.Content}

		for _, itemIndex := range section.SourceIDs {
			sourceIDs, found := resolveDigestReference(itemIndex, items)
			if !found {
				return nil, fmt.Errorf("invalid source_id %d for section %s", itemIndex, section.Heading)
			}
			for _, sourceID := range sourceIDs {
				if _, duplicate := seenSources[sourceID]; duplicate {
					continue
				}
				seenSources[sourceID] = struct{}{}

				source, found := index.resolve(sourceID)
				if !found {
					continue
				}
				resolvedSection.Sources = append(resolvedSection.Sources, source)
				resolved.Sources = append(resolved.Sources, source)
			}
		}

		resolved.Sections = append(resolved.Sections, resolvedSection)
	}

	return resolved, nil
}

// resolveDigestReference accepts only the documented zero-based digest index.
func resolveDigestReference(reference int, items []digestItem) ([]uint64, bool) {
	if reference >= 0 && reference < len(items) {
		return items[reference].SourceIDs, true
	}
	return nil, false
}

// corpusIndex answers "which document and chunk produced this source id?" for a
// whole corpus, so resolving a plan does not rescan every document per section.
type corpusIndex struct {
	documents  map[uint64]*corpusDocument
	firstChunk map[uint64]int
}

func newCorpusIndex(c *corpus) *corpusIndex {
	index := &corpusIndex{
		documents:  make(map[uint64]*corpusDocument, len(c.Documents)),
		firstChunk: make(map[uint64]int, len(c.Documents)),
	}
	for i := range c.Documents {
		index.documents[c.Documents[i].SourceID] = &c.Documents[i]
	}
	for _, chunk := range c.Chunks {
		sourceID := c.Documents[chunk.DocumentIndex].SourceID
		if _, exists := index.firstChunk[sourceID]; !exists {
			index.firstChunk[sourceID] = chunk.Ordinal
		}
	}
	return index
}

func (i *corpusIndex) resolve(sourceID uint64) (resolvedSource, bool) {
	doc, found := i.documents[sourceID]
	if !found {
		return resolvedSource{}, false
	}
	return resolvedSource{
		SourceID:        doc.SourceID,
		SourceVersionID: doc.SourceVersionID,
		ChunkOrdinal:    i.firstChunk[sourceID],
		ContentHash:     doc.ContentHash,
		DocumentTitle:   doc.Title,
	}, true
}
