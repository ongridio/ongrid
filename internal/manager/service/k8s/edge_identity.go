package k8s

import (
	"context"

	managerbizk8s "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	managersvcedge "github.com/ongridio/ongrid/internal/manager/service/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type EdgeIdentityIssuer struct {
	svc *managersvcedge.Service
}

func (i EdgeIdentityIssuer) CreateEdgeIdentity(ctx context.Context, name string, createdBy *uint64) (*managerbizk8s.EdgeCredential, error) {
	if i.svc == nil {
		return nil, errs.ErrNotWiredYet
	}
	out, err := i.svc.Create(ctx, name, createdBy)
	if err != nil {
		return nil, err
	}
	return &managerbizk8s.EdgeCredential{
		EdgeID:    out.Edge.ID,
		AccessKey: out.AccessKey,
		SecretKey: out.SecretKey,
	}, nil
}

func (i EdgeIdentityIssuer) RotateEdgeSecret(ctx context.Context, edgeID uint64) (*managerbizk8s.EdgeCredential, error) {
	if i.svc == nil {
		return nil, errs.ErrNotWiredYet
	}
	edge, err := i.svc.Get(ctx, edgeID)
	if err != nil {
		return nil, err
	}
	secret, err := i.svc.RotateManagedSecret(ctx, edgeID)
	if err != nil {
		return nil, err
	}
	return &managerbizk8s.EdgeCredential{
		EdgeID:    edgeID,
		AccessKey: edge.AccessKeyID,
		SecretKey: secret,
	}, nil
}

func (i EdgeIdentityIssuer) DeleteEdge(ctx context.Context, edgeID uint64) error {
	if i.svc == nil {
		return errs.ErrNotWiredYet
	}
	return i.svc.DeleteManaged(ctx, edgeID)
}

func NewEdgeIdentityIssuer(svc *managersvcedge.Service) EdgeIdentityIssuer {
	return EdgeIdentityIssuer{svc: svc}
}
