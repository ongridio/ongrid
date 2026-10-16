package k8s

import (
	"context"
	"fmt"
	"net/url"
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

// DiscoverNodeUID distinguishes a recreated Node from its previous identity.
func DiscoverNodeUID(ctx context.Context, nodeName string) (string, error) {
	if strings.TrimSpace(nodeName) == "" {
		return "", fmt.Errorf("node name is required")
	}
	client, err := newInClusterAPIClient()
	if err != nil {
		return "", err
	}
	var node struct {
		Metadata objectMeta `json:"metadata"`
	}
	if err := client.get(ctx, "/api/v1/nodes/"+url.PathEscape(nodeName), &node); err != nil {
		return "", fmt.Errorf("get kubernetes node identity: %w", err)
	}
	if strings.TrimSpace(node.Metadata.UID) == "" {
		return "", fmt.Errorf("kubernetes node UID is empty")
	}
	return node.Metadata.UID, nil
}
