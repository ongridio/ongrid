package k8s

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// NodeContainerIDs resolves current container identities using the same
// read-only Pod permission as discovery. Recreate the client to rotate tokens.
func NodeContainerIDs(ctx context.Context, node string) (map[string]string, error) {
	if node == "" {
		return nil, fmt.Errorf("node name required for process resource collection")
	}
	c, err := newInClusterAPIClient()
	if err != nil {
		return nil, err
	}
	defer c.http.CloseIdleConnections()
	return c.nodeContainerIDs(ctx, node)
}

func (c *apiClient) nodeContainerIDs(ctx context.Context, node string) (map[string]string, error) {
	type status struct {
		Name  string `json:"name"`
		ID    string `json:"containerID"`
		State struct {
			Running *struct{} `json:"running"`
		} `json:"state"`
	}
	type pod struct {
		Metadata objectMeta `json:"metadata"`
		Status   struct {
			Containers []status `json:"containerStatuses"`
			Init       []status `json:"initContainerStatuses"`
			Ephemeral  []status `json:"ephemeralContainerStatuses"`
		} `json:"status"`
	}
	pods, _, err := listAllK8sItems[pod](ctx, c, "/api/v1/pods?fieldSelector="+url.QueryEscape("spec.nodeName="+node))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range pods {
		for _, statuses := range [][]status{p.Status.Containers, p.Status.Init, p.Status.Ephemeral} {
			for _, s := range statuses {
				_, id, ok := strings.Cut(s.ID, "://")
				if ok && id != "" && p.Metadata.UID != "" && s.State.Running != nil {
					out[p.Metadata.UID+"/"+s.Name] = id
				}
			}
		}
	}
	return out, nil
}
