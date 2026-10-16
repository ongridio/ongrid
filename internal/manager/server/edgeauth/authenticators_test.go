package edgeauth

import (
	"context"
	"errors"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	k8sbiz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/passwd"
	"reflect"
	"testing"
	"time"
)

type authEdgeRepo struct {
	edgebiz.Repo
	calls *[]string
	edge  *edgemodel.Edge
}

func (r authEdgeRepo) GetByAccessKey(context.Context, string) (*edgemodel.Edge, error) {
	*r.calls = append(*r.calls, "edge")
	return r.edge, nil
}
func (r authEdgeRepo) UpdateStatus(context.Context, uint64, string, time.Time) error { return nil }

type authTelemetryRepo struct {
	k8sbiz.Repository
	calls      *[]string
	credential *k8smodel.TelemetryCredential
}

func (r authTelemetryRepo) GetTelemetryCredentialByAccessKey(context.Context, string) (*k8smodel.TelemetryCredential, error) {
	*r.calls = append(*r.calls, "telemetry")
	return r.credential, nil
}

func TestDataPlaneCredentialFallbackOrder(t *testing.T) {
	hash, err := passwd.Hash("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key           string
		edgeOK, telemetryOK bool
		want                Identity
		calls               []string
	}{
		{"telemetry first", "kt_test", true, true, Identity{ClusterID: 7}, []string{"telemetry"}},
		{"edge first", "edge_test", true, true, Identity{EdgeID: 42}, []string{"edge"}},
		{"telemetry fallback to edge", "kt_test", true, false, Identity{EdgeID: 42}, []string{"telemetry", "edge"}},
		{"edge fallback to telemetry", "edge_test", false, true, Identity{ClusterID: 7}, []string{"edge", "telemetry"}},
		{"both reject", "kt_test", false, false, Identity{}, []string{"telemetry", "edge"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			var edge *edgemodel.Edge
			if tc.edgeOK {
				edge = &edgemodel.Edge{ID: 42, SecretKeyHash: hash}
			}
			var credential *k8smodel.TelemetryCredential
			if tc.telemetryOK {
				credential = &k8smodel.TelemetryCredential{ClusterID: 7, SecretKeyHash: hash}
			}
			auth := NewDataPlaneAuthenticator(edgebiz.NewAccessKeyAuthenticator(authEdgeRepo{calls: &calls, edge: edge}, nil), k8sbiz.NewTelemetryAuthenticator(authTelemetryRepo{calls: &calls, credential: credential}))
			got, err := auth.AuthenticateDataPlane(context.Background(), tc.key, "test-secret")
			if got != tc.want || !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("identity %v / calls %v, want %v / %v", got, calls, tc.want, tc.calls)
			}
			if tc.want == (Identity{}) {
				if !errors.Is(err, errs.ErrUnauthorized) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	// A backend that is not wired must not fall back to another identity scope.
	auth := NewDataPlaneAuthenticator(edgebiz.NewAccessKeyAuthenticator(nil, nil), k8sbiz.NewTelemetryAuthenticator(nil))
	for _, key := range []string{"kt_test", "edge_test"} {
		if _, err := auth.AuthenticateDataPlane(context.Background(), key, "test-secret"); !errors.Is(err, errs.ErrNotWiredYet) {
			t.Fatalf("key %s: %v", key, err)
		}
	}
}
