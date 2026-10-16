package edgeauth

import (
	"context"
	"errors"
	"strings"

	managerbizedge "github.com/ongridio/ongrid/internal/manager/biz/edge"
	managerbizk8s "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// DataPlaneAuthenticator preserves Edge and Kubernetes credential fallback order.
type DataPlaneAuthenticator struct {
	edge      *managerbizedge.AccessKeyAuthenticator
	telemetry *managerbizk8s.TelemetryAuthenticator
}

type EdgeAuthenticator struct {
	authn *managerbizedge.AccessKeyAuthenticator
}

func (a EdgeAuthenticator) AuthenticateDataPlane(ctx context.Context, accessKey, secretKey string) (Identity, error) {
	sess, err := a.authn.Authenticate(ctx, accessKey, secretKey)
	if err != nil {
		return Identity{}, err
	}
	return Identity{EdgeID: sess.EdgeID}, nil
}

type TelemetryAuthenticator struct {
	authn *managerbizk8s.TelemetryAuthenticator
}

func (a TelemetryAuthenticator) AuthenticateDataPlane(ctx context.Context, accessKey, secretKey string) (Identity, error) {
	clusterID, err := a.authn.Authenticate(ctx, accessKey, secretKey)
	if err != nil {
		return Identity{}, err
	}
	return Identity{ClusterID: clusterID}, nil
}

func (a DataPlaneAuthenticator) AuthenticateDataPlane(ctx context.Context, accessKey, secretKey string) (Identity, error) {
	telemetryFirst := strings.HasPrefix(accessKey, "kt_")
	if telemetryFirst {
		clusterID, err := a.telemetry.Authenticate(ctx, accessKey, secretKey)
		if err == nil {
			return Identity{ClusterID: clusterID}, nil
		}
		if !errors.Is(err, errs.ErrUnauthorized) {
			return Identity{}, err
		}
	}
	sess, err := a.edge.Authenticate(ctx, accessKey, secretKey)
	if err == nil {
		return Identity{EdgeID: sess.EdgeID}, nil
	}
	if !errors.Is(err, errs.ErrUnauthorized) {
		return Identity{}, err
	}
	if telemetryFirst {
		return Identity{}, errs.ErrUnauthorized
	}
	clusterID, err := a.telemetry.Authenticate(ctx, accessKey, secretKey)
	if err != nil {
		return Identity{}, err
	}
	return Identity{ClusterID: clusterID}, nil
}

func NewDataPlaneAuthenticator(edge *managerbizedge.AccessKeyAuthenticator, telemetry *managerbizk8s.TelemetryAuthenticator) DataPlaneAuthenticator {
	return DataPlaneAuthenticator{edge: edge, telemetry: telemetry}
}

func NewEdgeAuthenticator(authn *managerbizedge.AccessKeyAuthenticator) EdgeAuthenticator {
	return EdgeAuthenticator{authn: authn}
}

func NewTelemetryAuthenticator(authn *managerbizk8s.TelemetryAuthenticator) TelemetryAuthenticator {
	return TelemetryAuthenticator{authn: authn}
}
