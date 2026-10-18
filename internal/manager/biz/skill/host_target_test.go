package skill

import (
	"context"
	"errors"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"testing"
)

type hostRouteFixture struct{}

func (hostRouteFixture) LookupHostDevice(_ context.Context, id uint64) (uint64, error) {
	if id == 7 || id == 23 {
		return 41, nil
	}
	return 0, errs.ErrNotFound
}
func (hostRouteFixture) LookupEdgeForDevice(_ context.Context, id uint64) (uint64, error) {
	if id == 41 {
		return 23, nil
	}
	return 0, errs.ErrNotFound
}
func TestResolveHostTarget(t *testing.T) {
	s := New(nil, nil, nil).WithHostRouter(hostRouteFixture{})
	for _, tc := range []struct {
		name string
		in   ExecuteInput
		want error
	}{
		{"host", ExecuteInput{DeviceID: 41}, nil},
		{"legacy explicit current edge", ExecuteInput{EdgeID: 23}, nil},
		{"matching pair", ExecuteInput{DeviceID: 41, EdgeID: 23}, nil},
		{"retired installation", ExecuteInput{EdgeID: 7}, errs.ErrInvalid},
		{"mismatching pair", ExecuteInput{DeviceID: 23, EdgeID: 23}, errs.ErrInvalid},
		{"host ID is not edge ID", ExecuteInput{DeviceID: 23}, errs.ErrNotFound},
		{"edge ID is not host ID", ExecuteInput{EdgeID: 41}, errs.ErrNotFound},
		{"missing", ExecuteInput{}, errs.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, edge, err := s.resolveHostTarget(context.Background(), tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if tc.want == nil && (host != 41 || edge != 23) {
				t.Fatalf("host=%d edge=%d", host, edge)
			}
		})
	}
}
