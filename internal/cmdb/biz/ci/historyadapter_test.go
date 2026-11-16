package ci

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// History names instances with the show attribute, falling back to the unique
// one. A password, link or file attribute must never supply that name: the
// history page is readable by any authenticated caller, and the normal read
// path hides these values for the same reason.
func TestHistoryNameAttributeSkipsSensitiveAttributes(t *testing.T) {
	safe := AttributeSpec{ID: 1, Name: "hostname"}
	secret := AttributeSpec{ID: 2, Name: "password", IsPassword: true}

	cases := []struct {
		name   string
		show   AttributeSpec
		unique AttributeSpec
		want   AttributeSpec
	}{
		{name: "safe show wins", show: safe, unique: AttributeSpec{ID: 3}, want: safe},
		{name: "password show falls back to the unique attribute", show: secret, unique: safe, want: safe},
		{name: "file show is sensitive too", show: AttributeSpec{ID: 4, IsFile: true}, unique: safe, want: safe},
		{name: "link show is sensitive too", show: AttributeSpec{ID: 5, IsLink: true}, unique: safe, want: safe},
		{name: "missing show falls back", show: AttributeSpec{}, unique: safe, want: safe},
		{name: "both sensitive yield no name", show: secret, unique: AttributeSpec{ID: 6, IsPassword: true}, want: AttributeSpec{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := historyNameAttribute(tc.show, tc.unique); got.ID != tc.want.ID {
				t.Fatalf("attribute = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A page of history names up to two hundred instances. Loading them one by one
// is an N+1 the topology graph multiplies to two thousand; the adapter uses the
// batch read.
func TestHistoryCINamesLoadsInstancesInOneQuery(t *testing.T) {
	repo := &readRepoStub{}
	one := &cimodel.CI{TypeID: 1}
	one.ID = 1
	two := &cimodel.CI{TypeID: 1}
	two.ID = 2
	repo.cis = map[model.ID]*cimodel.CI{1: one, 2: two}
	types := &resolverStub{
		ct:    citypemodel.CIType{Name: "host"},
		specs: map[model.ID]AttributeSpec{10: {ID: 10, Name: "hostname"}},
		show:  AttributeSpec{ID: 10, Name: "hostname"},
	}
	uc := NewUsecase(repo, types, nil)

	if _, err := NewHistoryCINames(uc).ResolveCINames(context.Background(), []model.ID{1, 2}); err != nil {
		t.Fatalf("ResolveCINames: %v", err)
	}
	if repo.batchLoads != 1 {
		t.Fatalf("batch loads = %d, want 1", repo.batchLoads)
	}
	if repo.singleLoads != 0 {
		t.Fatalf("single loads = %d, want 0", repo.singleLoads)
	}
}
