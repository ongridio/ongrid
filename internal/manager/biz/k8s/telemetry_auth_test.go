package k8s

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/passwd"
)

func TestTelemetryAuthenticatorAcceptsOnlyTelemetryCredential(t *testing.T) {
	repo := newFakeRepo()
	hash, err := passwd.Hash("ks_secret")
	if err != nil {
		t.Fatalf("passwd.Hash() error = %v", err)
	}
	repo.telemetryCredential = &model.TelemetryCredential{
		ClusterID:     7,
		AccessKeyID:   "kt_access",
		SecretKeyHash: hash,
	}
	auth := NewTelemetryAuthenticator(repo)
	clusterID, err := auth.Authenticate(context.Background(), "kt_access", "ks_secret")
	if err != nil || clusterID != 7 {
		t.Fatalf("Authenticate(valid) = (%d, %v), want (7, nil)", clusterID, err)
	}
	if _, err := auth.Authenticate(context.Background(), "kt_access", "wrong"); !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("Authenticate(wrong secret) error = %v, want unauthorized", err)
	}
	if _, err := auth.Authenticate(context.Background(), "edge_access", "edge_secret"); !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("Authenticate(edge credential) error = %v, want unauthorized", err)
	}
}

func TestTelemetryAuthCacheIsBounded(t *testing.T) {
	cache := newTelemetryAuthCache()
	now := time.Now()
	for i := 0; i < maxTelemetryAuthCacheEntries+100; i++ {
		cache.store(fmt.Sprintf("access-%d", i), fmt.Sprintf("secret-%d", i), uint64(i+1), now)
	}
	if got := len(cache.entries); got != maxTelemetryAuthCacheEntries {
		t.Fatalf("cache entries = %d, want %d", got, maxTelemetryAuthCacheEntries)
	}
}
