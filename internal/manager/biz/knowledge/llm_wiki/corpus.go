package llm_wiki

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// paragraphSeparator is the only boundary the chunker splits on. Keeping it in
// one place keeps paragraph splitting and chunk length accounting in sync.
const paragraphSeparator = "\n\n"

// minCorpusDocumentBytes skips trivially small snapshots: they carry no
// structure worth turning into Wiki pages.
const minCorpusDocumentBytes = 100

// corpusDocument is one deduplicated document loaded from Raw Knowledge.
type corpusDocument struct {
	SourceID        uint64
	SourceVersionID uint64
	Title           string
	Content         string
	ContentHash     string
}

// corpusChunk is a paragraph-aligned piece of a corpusDocument.
type corpusChunk struct {
	DocumentIndex int    // index into corpus.Documents
	Ordinal       int    // sequential within document
	Start         int    // byte offset in original content
	End           int    // byte offset in original content
	Text          string // the chunk text
}

// corpus is the full set of documents compiled into one Wiki build.
type corpus struct {
	Documents []corpusDocument
	Chunks    []corpusChunk
}

// loadCorpus loads documents from Raw Knowledge and deduplicates them by
// content hash. When sourceIDs is non-empty only those sources are loaded.
// Documents are returned in a deterministic order so repeated builds over the
// same sources produce the same Wiki.
func loadCorpus(ctx context.Context, repo BuildRepository, tenantID uint64, log *slog.Logger, sourceIDs ...uint64) (*corpus, error) {
	sources, err := listAllSources(ctx, repo, tenantID)
	if err != nil {
		return nil, fmt.Errorf("corpus: list sources: %w", err)
	}

	var allowSet map[uint64]struct{}
	if len(sourceIDs) > 0 {
		allowSet = make(map[uint64]struct{}, len(sourceIDs))
		for _, id := range sourceIDs {
			allowSet[id] = struct{}{}
		}
	}

	seenHashes := make(map[string]struct{}, len(sources))
	var documents []corpusDocument

	for _, source := range sources {
		if allowSet != nil {
			if _, ok := allowSet[source.ID]; !ok {
				continue
			}
		}

		if source.CurrentVersionID == nil {
			continue
		}

		version, err := repo.GetVersion(ctx, tenantID, *source.CurrentVersionID)
		if err != nil {
			log.WarnContext(ctx, "corpus: skip source with missing version",
				slog.Uint64("source_id", source.ID),
				slog.String("error", err.Error()))
			continue
		}

		if version.SizeBytes < minCorpusDocumentBytes {
			continue
		}

		if _, duplicate := seenHashes[version.SHA256]; duplicate {
			log.DebugContext(ctx, "corpus: skip duplicate document",
				slog.Uint64("source_id", source.ID),
				slog.String("hash", version.SHA256[:12]))
			continue
		}
		seenHashes[version.SHA256] = struct{}{}

		documents = append(documents, corpusDocument{
			SourceID:        source.ID,
			SourceVersionID: version.ID,
			Title:           source.RawPath,
			ContentHash:     version.SHA256,
		})
	}

	sort.Slice(documents, func(i, j int) bool {
		return documents[i].SourceID < documents[j].SourceID
	})

	log.InfoContext(ctx, "corpus: loaded documents",
		slog.Int("total_sources", len(sources)),
		slog.Int("unique_documents", len(documents)),
		slog.Any("filter_source_ids", sourceIDs))

	return &corpus{Documents: documents}, nil
}

// corpusContentLoader reads document content from the version snapshot on
// demand, so a large corpus never has to be held in memory at once.
type corpusContentLoader struct {
	repo     BuildRepository
	files    *FileStore
	tenantID uint64
}

// newCorpusContentLoader creates a loader for reading document content.
func newCorpusContentLoader(repo BuildRepository, files *FileStore, tenantID uint64) *corpusContentLoader {
	return &corpusContentLoader{
		repo:     repo,
		files:    files,
		tenantID: tenantID,
	}
}

// loadContent reads the full text of a document from its version snapshot.
func (l *corpusContentLoader) loadContent(ctx context.Context, doc corpusDocument) (string, error) {
	version, err := l.repo.GetVersion(ctx, l.tenantID, doc.SourceVersionID)
	if err != nil {
		return "", fmt.Errorf("corpus: load version %d: %w", doc.SourceVersionID, err)
	}

	body, err := l.files.Read(ctx, version.SnapshotPath, MaxSourceBytes)
	if err != nil {
		return "", fmt.Errorf("corpus: read snapshot %s: %w", version.SnapshotPath, err)
	}

	return string(body), nil
}

// prepareCorpusChunks loads every document and splits it into Wiki-sized chunks.
func prepareCorpusChunks(ctx context.Context, c *corpus, loader *corpusContentLoader, log *slog.Logger) error {
	var totalChunks int

	for i := range c.Documents {
		content, err := loader.loadContent(ctx, c.Documents[i])
		if err != nil {
			return fmt.Errorf("corpus: load document %d: %w", c.Documents[i].SourceID, err)
		}
		c.Documents[i].Content = content

		chunks := splitDocumentIntoChunks(content, i)
		c.Chunks = append(c.Chunks, chunks...)
		totalChunks += len(chunks)
	}

	log.InfoContext(ctx, "corpus: prepared chunks",
		slog.Int("documents", len(c.Documents)),
		slog.Int("total_chunks", totalChunks))

	return nil
}

// splitDocumentIntoChunks divides content into token-bounded chunks. Chunks end
// on a paragraph boundary whenever one is available inside the size limit.
func splitDocumentIntoChunks(content string, docIndex int) []corpusChunk {
	if len(content) == 0 {
		return nil
	}

	var chunks []corpusChunk
	ordinal := 0
	paragraphs := strings.Split(content, paragraphSeparator)

	var currentChunk strings.Builder
	chunkStart := 0
	currentOffset := 0
	maxChunkBytes := TargetChunkTokens * charsPerToken

	for _, paragraph := range paragraphs {
		paragraphLen := len(paragraph) + len(paragraphSeparator)

		if currentChunk.Len() > 0 && currentChunk.Len()+paragraphLen > maxChunkBytes {
			chunks = append(chunks, newCorpusChunk(docIndex, ordinal, chunkStart, currentOffset, currentChunk.String()))
			ordinal++
			chunkStart = currentOffset
			currentChunk.Reset()
		}

		if currentChunk.Len() > 0 {
			currentChunk.WriteString(paragraphSeparator)
		}
		currentChunk.WriteString(paragraph)
		currentOffset += paragraphLen
	}

	if currentChunk.Len() > 0 {
		chunks = append(chunks, newCorpusChunk(docIndex, ordinal, chunkStart, currentOffset, currentChunk.String()))
	}

	return chunks
}

func newCorpusChunk(docIndex, ordinal, start, end int, text string) corpusChunk {
	return corpusChunk{
		DocumentIndex: docIndex,
		Ordinal:       ordinal,
		Start:         start,
		End:           end,
		Text:          strings.TrimSpace(text),
	}
}
