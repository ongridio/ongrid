package k8s

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/passwd"
)

const (
	telemetryAuthCacheTTL        = 30 * time.Second
	maxTelemetryAuthCacheEntries = 1024
)

// TelemetryAuthenticator validates the cluster-scoped write-only identity.
// The tunnel authenticator does not use this type, which is the enforcement
// boundary preventing data-plane credentials from becoming controller
// credentials.
type TelemetryAuthenticator struct {
	repo  Repository
	cache *telemetryAuthCache
}

func NewTelemetryAuthenticator(repo Repository) *TelemetryAuthenticator {
	return &TelemetryAuthenticator{repo: repo, cache: newTelemetryAuthCache()}
}

func (a *TelemetryAuthenticator) Authenticate(ctx context.Context, accessKey, secretKey string) (uint64, error) {
	if a == nil || a.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	if clusterID, ok := a.cache.lookup(accessKey, secretKey, time.Now()); ok {
		return clusterID, nil
	}
	credential, err := a.repo.GetTelemetryCredentialByAccessKey(ctx, accessKey)
	if err != nil || credential == nil || !passwd.Verify(secretKey, credential.SecretKeyHash) {
		return 0, errs.ErrUnauthorized
	}
	a.cache.store(accessKey, secretKey, credential.ClusterID, time.Now())
	return credential.ClusterID, nil
}

type telemetryAuthCacheEntry struct {
	clusterID uint64
	expiresAt time.Time
}

type telemetryAuthCache struct {
	mu      sync.Mutex
	entries map[[32]byte]telemetryAuthCacheEntry
}

func newTelemetryAuthCache() *telemetryAuthCache {
	return &telemetryAuthCache{entries: make(map[[32]byte]telemetryAuthCacheEntry)}
}

func (c *telemetryAuthCache) lookup(accessKey, secretKey string, now time.Time) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	key := telemetryAuthCacheKey(accessKey, secretKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return 0, false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		return 0, false
	}
	return entry.clusterID, entry.clusterID != 0
}

func (c *telemetryAuthCache) store(accessKey, secretKey string, clusterID uint64, now time.Time) {
	if c == nil || clusterID == 0 {
		return
	}
	c.mu.Lock()
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) >= maxTelemetryAuthCacheEntries {
		var oldestKey [32]byte
		var oldestExpiry time.Time
		for key, entry := range c.entries {
			if oldestExpiry.IsZero() || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = entry.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[telemetryAuthCacheKey(accessKey, secretKey)] = telemetryAuthCacheEntry{
		clusterID: clusterID,
		expiresAt: now.Add(telemetryAuthCacheTTL),
	}
	c.mu.Unlock()
}

func telemetryAuthCacheKey(accessKey, secretKey string) [32]byte {
	return sha256.Sum256([]byte(accessKey + "\x00" + secretKey))
}
