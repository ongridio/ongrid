package k8s

import (
	"context"
	"fmt"
	"strings"
)

const clusterIdentityNamespace = "kube-system"

// DiscoverClusterUID returns the immutable UID of the kube-system Namespace.
// It identifies the physical cluster independently of the manager-side record.
func DiscoverClusterUID(ctx context.Context) (string, error) {
	client, err := newInClusterAPIClient()
	if err != nil {
		return "", err
	}
	return client.clusterUID(ctx)
}

func (c *apiClient) clusterUID(ctx context.Context) (string, error) {
	var namespace struct {
		Metadata objectMeta `json:"metadata"`
	}
	if err := c.get(ctx, "/api/v1/namespaces/"+clusterIdentityNamespace, &namespace); err != nil {
		return "", fmt.Errorf("get kubernetes cluster identity: %w", err)
	}
	uid := strings.TrimSpace(namespace.Metadata.UID)
	if uid == "" {
		return "", fmt.Errorf("get kubernetes cluster identity: kube-system namespace UID is empty")
	}
	return uid, nil
}
