package autoapm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

// MetricsScope is a Manager-resolved allowlist; an empty scope collects no Pods.
type MetricsScope struct {
	Namespaces []string `json:"namespaces"`
	PodUIDs    []string `json:"pod_uids"`
}

func (s MetricsScope) Allows(namespace, uid string) bool {
	return slices.Contains(s.Namespaces, namespace) || (uid != "" && slices.Contains(s.PodUIDs, uid))
}

func ParseMetricsScope(raw []byte) (MetricsScope, error) {
	var scope MetricsScope
	if len(bytes.TrimSpace(raw)) == 0 {
		return scope, nil // Older Managers do not publish a scope; never collect all.
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&scope); err != nil {
		return MetricsScope{}, fmt.Errorf("decode application metrics scope: %w", err)
	}
	if err := dec.Decode(new(interface{})); err != io.EOF {
		return MetricsScope{}, fmt.Errorf("application metrics scope has trailing data")
	}
	for _, namespace := range scope.Namespaces {
		if !kubeName(namespace, 63) {
			return MetricsScope{}, fmt.Errorf("invalid application metrics namespace")
		}
	}
	for _, uid := range scope.PodUIDs {
		if !kubeName(uid, 253) {
			return MetricsScope{}, fmt.Errorf("invalid application metrics Pod UID")
		}
	}
	return scope, nil
}
