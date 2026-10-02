package knowledge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/qdrantx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedScrollVec struct {
	QdrantClient
	calls []qdrantx.ScrollOpts
	pages map[uint64]*qdrantx.ScrollResult
	errAt *uint64
}

func (v *scriptedScrollVec) Scroll(_ context.Context, _ string, opts qdrantx.ScrollOpts) (*qdrantx.ScrollResult, error) {
	v.calls = append(v.calls, opts)
	key := uint64(0)
	if opts.Offset != nil {
		key = *opts.Offset
	}
	if v.errAt != nil && key == *v.errAt {
		return nil, errors.New("qdrant unavailable")
	}
	if page, ok := v.pages[key]; ok {
		return page, nil
	}
	return &qdrantx.ScrollResult{}, nil
}

func listDocHit(id, alias uint64, chunkIndex int, title string) qdrantx.SearchHit {
	return qdrantx.SearchHit{ID: id, Payload: map[string]any{
		"source_type": "repo",
		"title":       title,
		"content":     title,
		"id_alias":    alias,
		"chunk_index": chunkIndex,
	}}
}

func TestListDocs_AllWalksEveryPageAndDeduplicatesAcrossPages(t *testing.T) {
	first := make([]qdrantx.SearchHit, 0, 1000)
	first = append(first, listDocHit(10, 1, 1, "tail chunk"))
	for id := uint64(2); id <= 1000; id++ {
		first = append(first, listDocHit(id, id, 0, fmt.Sprintf("doc-%d", id)))
	}
	next := uint64(1000)
	vec := &scriptedScrollVec{pages: map[uint64]*qdrantx.ScrollResult{
		0:    {Points: first, NextOffset: &next},
		1000: {Points: []qdrantx.SearchHit{listDocHit(1, 1, 0, "head chunk"), listDocHit(1001, 1001, 0, "doc-1001")}},
	}}
	uc := &Usecase{vec: vec}

	docs, err := uc.ListDocs(context.Background(), ListDocsFilter{SourceType: "repo", All: true})

	require.NoError(t, err)
	require.Len(t, docs, 1001)
	assert.Equal(t, "head chunk", docs[0].Title)
	require.Len(t, vec.calls, 2)
	assert.Nil(t, vec.calls[0].Offset)
	require.NotNil(t, vec.calls[1].Offset)
	assert.Equal(t, uint64(1000), *vec.calls[1].Offset)
	assert.Equal(t, 1000, vec.calls[0].Limit)
	assert.Equal(t, map[string]any{"source_type": "repo"}, vec.calls[0].MustMatch)
}

func TestListDocs_BoundedQueryKeepsExistingLimitBehavior(t *testing.T) {
	vec := &scriptedScrollVec{pages: map[uint64]*qdrantx.ScrollResult{
		0: {Points: []qdrantx.SearchHit{
			listDocHit(1, 1, 0, "one"),
			listDocHit(2, 2, 0, "two"),
			listDocHit(3, 3, 0, "three"),
		}},
	}}
	uc := &Usecase{vec: vec}

	docs, err := uc.ListDocs(context.Background(), ListDocsFilter{SourceType: "repo", Limit: 2})

	require.NoError(t, err)
	require.Len(t, docs, 2)
	require.Len(t, vec.calls, 1)
	assert.Equal(t, 16, vec.calls[0].Limit)
}

func TestListDocs_AllPropagatesLaterPageFailure(t *testing.T) {
	next := uint64(7)
	vec := &scriptedScrollVec{
		pages: map[uint64]*qdrantx.ScrollResult{0: {Points: []qdrantx.SearchHit{listDocHit(1, 1, 0, "one")}, NextOffset: &next}},
		errAt: &next,
	}
	uc := &Usecase{vec: vec}

	docs, err := uc.ListDocs(context.Background(), ListDocsFilter{SourceType: "repo", All: true})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "scroll all")
	assert.Nil(t, docs)
}
