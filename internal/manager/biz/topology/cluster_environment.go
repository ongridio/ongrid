package topology

import (
	"context"
	"encoding/json"
	"fmt"
)

// ClusterEnvironment reads the environment of a registered cluster node.
func (u *Usecase) ClusterEnvironment(ctx context.Context, nodeID uint64) (string, error) {
	node, err := u.GetNode(ctx, nodeID)
	if err != nil {
		return "", err
	}
	var props struct {
		Environment string `json:"environment"`
	}
	if node.PropsJSON != "" {
		if err := json.Unmarshal([]byte(node.PropsJSON), &props); err != nil {
			return "", fmt.Errorf("decode cluster environment: %w", err)
		}
	}
	return props.Environment, nil
}
