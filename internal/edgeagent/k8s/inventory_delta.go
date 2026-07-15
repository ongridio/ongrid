package k8s

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	inventorySyncFull  = "full"
	inventorySyncDelta = "delta"

	watchResourceNodes     = "nodes"
	watchResourceWorkloads = "workloads"
	watchResourcePods      = "pods"
	watchResourceEvents    = "events"
)

type inventoryCache struct {
	mu        sync.Mutex
	scope     string
	namespace string
	nodes     map[string]tunnel.KubernetesNodeSnapshot
	workloads map[string]tunnel.KubernetesWorkloadSnapshot
	pods      map[string]tunnel.KubernetesPodSnapshot
	events    map[string]tunnel.KubernetesEventSnapshot
}

func newInventoryCache(snap *inventorySnapshot) *inventoryCache {
	c := &inventoryCache{}
	c.reset(snap)
	return c
}

func (c *inventoryCache) reset(snap *inventorySnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scope = ""
	c.namespace = ""
	c.nodes = map[string]tunnel.KubernetesNodeSnapshot{}
	c.workloads = map[string]tunnel.KubernetesWorkloadSnapshot{}
	c.pods = map[string]tunnel.KubernetesPodSnapshot{}
	c.events = map[string]tunnel.KubernetesEventSnapshot{}
	if snap == nil {
		return
	}
	c.scope = strings.TrimSpace(snap.scope)
	c.namespace = strings.TrimSpace(snap.namespace)
	for _, item := range snap.nodes {
		if key := nodeSnapshotKey(item); key != "" {
			c.nodes[key] = item
		}
	}
	for _, item := range snap.workloads {
		if key := workloadSnapshotKey(item); key != "" {
			c.workloads[key] = item
		}
	}
	for _, item := range snap.pods {
		if key := podSnapshotKey(item); key != "" {
			c.pods[key] = item
		}
	}
	for _, item := range snap.events {
		if key := eventSnapshotKey(item); key != "" {
			c.events[key] = item
		}
	}
}

func (c *inventoryCache) applyWatchEvent(spec inventoryWatchSpec, event k8sWatchEvent, observedAt time.Time) (inventoryWatchTrigger, error) {
	eventType := strings.ToUpper(strings.TrimSpace(event.Type))
	if eventType == "" || eventType == "BOOKMARK" {
		return inventoryWatchTrigger{}, nil
	}
	trigger := newInventoryWatchTrigger(spec.name+":"+eventType, observedAt)
	trigger.syncType = inventorySyncDelta
	if rv := eventResourceVersion(event); rv != "" {
		trigger.resourceVersion = rv
		trigger.resourceVersions = map[string]string{spec.name: rv}
	}
	switch eventType {
	case "ADDED", "MODIFIED":
		return c.applyWatchUpsert(spec, event, trigger)
	case "DELETED":
		return c.applyWatchDelete(spec, event, trigger)
	default:
		return inventoryWatchTrigger{}, nil
	}
}

func (c *inventoryCache) applyWatchUpsert(spec inventoryWatchSpec, event k8sWatchEvent, trigger inventoryWatchTrigger) (inventoryWatchTrigger, error) {
	switch spec.resource {
	case watchResourceNodes:
		var item nodeItem
		if err := unmarshalWatchObject(event, &item); err != nil {
			return inventoryWatchTrigger{}, err
		}
		snap := nodeSnapshotFromItem(item)
		if nodeSnapshotKey(snap) == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		c.nodes[nodeSnapshotKey(snap)] = snap
		c.mu.Unlock()
		trigger.nodes = []tunnel.KubernetesNodeSnapshot{snap}
	case watchResourcePods:
		var item podItem
		if err := unmarshalWatchObject(event, &item); err != nil {
			return inventoryWatchTrigger{}, err
		}
		snap := podSnapshotFromItem(item)
		if podSnapshotKey(snap) == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		c.pods[podSnapshotKey(snap)] = snap
		c.mu.Unlock()
		trigger.pods = []tunnel.KubernetesPodSnapshot{snap}
	case watchResourceEvents:
		var item eventItem
		if err := unmarshalWatchObject(event, &item); err != nil {
			return inventoryWatchTrigger{}, err
		}
		snap := eventSnapshotFromItem(item)
		if eventSnapshotKey(snap) == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		c.events[eventSnapshotKey(snap)] = snap
		c.mu.Unlock()
		trigger.events = []tunnel.KubernetesEventSnapshot{snap}
	case watchResourceWorkloads:
		var item workloadItem
		if err := unmarshalWatchObject(event, &item); err != nil {
			return inventoryWatchTrigger{}, err
		}
		snap := workloadSnapshotFromItem(spec.workloadKind, item)
		if workloadSnapshotKey(snap) == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		c.workloads[workloadSnapshotKey(snap)] = snap
		c.mu.Unlock()
		trigger.workloads = []tunnel.KubernetesWorkloadSnapshot{snap}
	default:
		return inventoryWatchTrigger{}, nil
	}
	return trigger, nil
}

func (c *inventoryCache) applyWatchDelete(spec inventoryWatchSpec, event k8sWatchEvent, trigger inventoryWatchTrigger) (inventoryWatchTrigger, error) {
	ref, err := watchObjectRefFromEvent(spec, event)
	if err != nil {
		return inventoryWatchTrigger{}, err
	}
	switch spec.resource {
	case watchResourceNodes:
		nodeRef := tunnel.KubernetesNodeRef{Name: ref.name, UID: ref.uid}
		if nodeRef.Name == "" && nodeRef.UID == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		delete(c.nodes, nodeRefKey(nodeRef))
		c.mu.Unlock()
		trigger.deletedNodes = []tunnel.KubernetesNodeRef{nodeRef}
	case watchResourcePods:
		podRef := tunnel.KubernetesPodRef{Namespace: ref.namespace, Name: ref.name, UID: ref.uid}
		if podRef.Name == "" && podRef.UID == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		delete(c.pods, podRefKey(podRef))
		c.mu.Unlock()
		trigger.deletedPods = []tunnel.KubernetesPodRef{podRef}
	case watchResourceEvents:
		eventRef := tunnel.KubernetesEventRef{Namespace: ref.namespace, Name: ref.name, UID: ref.uid}
		if eventRef.Name == "" && eventRef.UID == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		delete(c.events, eventRefKey(eventRef))
		c.mu.Unlock()
		trigger.deletedEvents = []tunnel.KubernetesEventRef{eventRef}
	case watchResourceWorkloads:
		workloadRef := tunnel.KubernetesWorkloadRef{Kind: spec.workloadKind, Namespace: ref.namespace, Name: ref.name, UID: ref.uid}
		if workloadRef.Kind == "" || workloadRef.Name == "" {
			return inventoryWatchTrigger{}, nil
		}
		c.mu.Lock()
		delete(c.workloads, workloadRefKey(workloadRef))
		c.mu.Unlock()
		trigger.deletedWorkloads = []tunnel.KubernetesWorkloadRef{workloadRef}
	default:
		return inventoryWatchTrigger{}, nil
	}
	return trigger, nil
}

type watchObjectRef struct {
	namespace string
	name      string
	uid       string
}

func watchObjectRefFromEvent(spec inventoryWatchSpec, event k8sWatchEvent) (watchObjectRef, error) {
	var item struct {
		Metadata objectMeta `json:"metadata"`
	}
	if err := unmarshalWatchObject(event, &item); err != nil {
		return watchObjectRef{}, err
	}
	return watchObjectRef{
		namespace: strings.TrimSpace(item.Metadata.Namespace),
		name:      strings.TrimSpace(item.Metadata.Name),
		uid:       strings.TrimSpace(item.Metadata.UID),
	}, nil
}

func unmarshalWatchObject(event k8sWatchEvent, dst any) error {
	if len(event.Object) == 0 {
		return fmt.Errorf("kubernetes watch event object is empty")
	}
	if err := json.Unmarshal(event.Object, dst); err != nil {
		return fmt.Errorf("decode kubernetes watch object: %w", err)
	}
	return nil
}

func nodeSnapshotFromItem(item nodeItem) tunnel.KubernetesNodeSnapshot {
	return tunnel.KubernetesNodeSnapshot{
		Name:           item.Metadata.Name,
		UID:            item.Metadata.UID,
		ProviderID:     item.Spec.ProviderID,
		Labels:         item.Metadata.Labels,
		Taints:         item.Spec.Taints,
		Conditions:     conditionMaps(item.Status.Conditions),
		Capacity:       item.Status.Capacity,
		Allocatable:    item.Status.Allocatable,
		KubeletVersion: item.Status.NodeInfo.KubeletVersion,
	}
}

func workloadSnapshotFromItem(kind string, item workloadItem) tunnel.KubernetesWorkloadSnapshot {
	return tunnel.KubernetesWorkloadSnapshot{
		Kind:            kind,
		Namespace:       item.Metadata.Namespace,
		Name:            item.Metadata.Name,
		UID:             item.Metadata.UID,
		DesiredReplicas: desiredReplicas(kind, item),
		ReadyReplicas:   readyReplicas(kind, item),
		Labels:          item.Metadata.Labels,
		Annotations:     item.Metadata.Annotations,
		Conditions:      conditionMaps(item.Status.Conditions),
	}
}

func podSnapshotFromItem(item podItem) tunnel.KubernetesPodSnapshot {
	ownerKind, ownerName := controllerOwner(item.Metadata.OwnerReferences)
	return tunnel.KubernetesPodSnapshot{
		Namespace:    item.Metadata.Namespace,
		Name:         item.Metadata.Name,
		UID:          item.Metadata.UID,
		NodeName:     item.Spec.NodeName,
		Phase:        item.Status.Phase,
		OwnerKind:    ownerKind,
		OwnerName:    ownerName,
		RestartCount: podRestartCount(item.Status.ContainerStatuses),
		Reason:       podReason(item.Status),
	}
}

func eventSnapshotFromItem(item eventItem) tunnel.KubernetesEventSnapshot {
	return tunnel.KubernetesEventSnapshot{
		Namespace:           item.Metadata.Namespace,
		Name:                item.Metadata.Name,
		UID:                 item.Metadata.UID,
		Type:                item.Type,
		Reason:              item.Reason,
		Message:             item.Message,
		InvolvedKind:        item.InvolvedObject.Kind,
		InvolvedNamespace:   item.InvolvedObject.Namespace,
		InvolvedName:        item.InvolvedObject.Name,
		InvolvedUID:         item.InvolvedObject.UID,
		SourceComponent:     item.Source.Component,
		SourceHost:          item.Source.Host,
		ReportingController: item.ReportingComponent,
		ReportingInstance:   item.ReportingInstance,
		Action:              item.Action,
		Count:               item.Count,
		FirstTimestamp:      item.FirstTimestamp,
		LastTimestamp:       item.LastTimestamp,
		EventTime:           item.EventTime,
	}
}

func nodeSnapshotKey(item tunnel.KubernetesNodeSnapshot) string {
	return nodeRefKey(tunnel.KubernetesNodeRef{Name: item.Name, UID: item.UID})
}

func workloadSnapshotKey(item tunnel.KubernetesWorkloadSnapshot) string {
	return workloadRefKey(tunnel.KubernetesWorkloadRef{Kind: item.Kind, Namespace: item.Namespace, Name: item.Name, UID: item.UID})
}

func podSnapshotKey(item tunnel.KubernetesPodSnapshot) string {
	return podRefKey(tunnel.KubernetesPodRef{Namespace: item.Namespace, Name: item.Name, UID: item.UID})
}

func eventSnapshotKey(item tunnel.KubernetesEventSnapshot) string {
	return eventRefKey(tunnel.KubernetesEventRef{Namespace: item.Namespace, Name: item.Name, UID: item.UID})
}

func nodeRefKey(ref tunnel.KubernetesNodeRef) string {
	if uid := strings.TrimSpace(ref.UID); uid != "" {
		return "uid:" + uid
	}
	if name := strings.TrimSpace(ref.Name); name != "" {
		return "name:" + name
	}
	return ""
}

func workloadRefKey(ref tunnel.KubernetesWorkloadRef) string {
	kind := strings.TrimSpace(ref.Kind)
	name := strings.TrimSpace(ref.Name)
	if kind == "" || name == "" {
		return ""
	}
	return kind + "|" + strings.TrimSpace(ref.Namespace) + "|" + name
}

func podRefKey(ref tunnel.KubernetesPodRef) string {
	namespace := strings.TrimSpace(ref.Namespace)
	name := strings.TrimSpace(ref.Name)
	uid := strings.TrimSpace(ref.UID)
	if uid != "" {
		return namespace + "|" + name + "|" + uid
	}
	if name != "" {
		return namespace + "|" + name
	}
	return ""
}

func eventRefKey(ref tunnel.KubernetesEventRef) string {
	if uid := strings.TrimSpace(ref.UID); uid != "" {
		return "uid:" + uid
	}
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return ""
	}
	return strings.TrimSpace(ref.Namespace) + "|" + name
}
