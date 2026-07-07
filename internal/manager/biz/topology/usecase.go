package topology

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Usecase is the biz-layer facade over the four topology repos. HTTP
// handlers (and future tools) call into Usecase rather than the repos
// directly so all validation lives in one place.
type Usecase struct {
	nodes     NodeRepo
	relations RelationRepo
	types     RelationTypeRepo
	nodeTypes NodeTypeRepo
	log       *slog.Logger
}

// NewUsecase builds the usecase. Any repo may be nil — methods touching
// a nil repo return ErrNotWiredYet so callers degrade cleanly during
// boot/test wiring.
func NewUsecase(nodes NodeRepo, relations RelationRepo, types RelationTypeRepo, nodeTypes NodeTypeRepo, log *slog.Logger) *Usecase {
	return &Usecase{nodes: nodes, relations: relations, types: types, nodeTypes: nodeTypes, log: log}
}

// ---------- Node ------------------------------------------------------------

// CreateNode validates + persists a new Node. Type and Name are
// required; PropsJSON, if non-empty, must parse as JSON (we don't
// schema-validate the shape — operators are trusted on the props bag).
func (u *Usecase) CreateNode(ctx context.Context, typ, name, propsJSON string) (*model.Node, error) {
	if u.nodes == nil {
		return nil, errs.ErrNotWiredYet
	}
	typ = strings.TrimSpace(typ)
	name = strings.TrimSpace(name)
	if typ == "" || name == "" {
		return nil, fmt.Errorf("%w: type and name required", errs.ErrInvalid)
	}
	if propsJSON != "" {
		if !json.Valid([]byte(propsJSON)) {
			return nil, fmt.Errorf("%w: props_jsonb is not valid JSON", errs.ErrInvalid)
		}
	}
	n := &model.Node{Type: typ, Name: name, PropsJSON: propsJSON}
	if err := u.nodes.Create(ctx, n); err != nil {
		return nil, err
	}
	if u.log != nil {
		u.log.Info("node created", slog.Uint64("id", n.ID), slog.String("type", n.Type), slog.String("name", n.Name))
	}
	return n, nil
}

// UpdateNode rewrites Name and/or PropsJSON. Type is immutable — moving
// a row between types breaks any references downstream entities have
// pegged to it (device.node_id presumes node.type='device').
func (u *Usecase) UpdateNode(ctx context.Context, id uint64, name, propsJSON string) error {
	if u.nodes == nil {
		return errs.ErrNotWiredYet
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	if propsJSON != "" && !json.Valid([]byte(propsJSON)) {
		return fmt.Errorf("%w: props_jsonb is not valid JSON", errs.ErrInvalid)
	}
	return u.nodes.Update(ctx, id, name, propsJSON)
}

// GetNode returns one node by id.
func (u *Usecase) GetNode(ctx context.Context, id uint64) (*model.Node, error) {
	if u.nodes == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.nodes.Get(ctx, id)
}

// ListNodes returns nodes matching f.
func (u *Usecase) ListNodes(ctx context.Context, f NodeListFilter) ([]*model.Node, int64, error) {
	if u.nodes == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	items, err := u.nodes.List(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	total, err := u.nodes.Count(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// DeleteNode removes a node. Caller is expected to have already cut
// any inbound / outbound relations — we don't cascade because the
// concrete entity table (device / service / ...) might still have a
// node_id pointing at this row and orphaning that FK is worse than
// surfacing the error.
func (u *Usecase) DeleteNode(ctx context.Context, id uint64) error {
	if u.nodes == nil {
		return errs.ErrNotWiredYet
	}
	return u.nodes.Delete(ctx, id)
}

// ---------- Relation --------------------------------------------------------

// CreateRelation validates + persists a new edge. Both endpoints must
// exist (we look them up) and the relation type must be registered
// (built-in or operator-added).
func (u *Usecase) CreateRelation(ctx context.Context, srcID, dstID uint64, typ, propsJSON string) (*model.Relation, error) {
	if u.relations == nil || u.nodes == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	typ = strings.TrimSpace(typ)
	if srcID == 0 || dstID == 0 || typ == "" {
		return nil, fmt.Errorf("%w: src_id, dst_id, type required", errs.ErrInvalid)
	}
	if srcID == dstID {
		return nil, fmt.Errorf("%w: self-edge not allowed", errs.ErrInvalid)
	}
	if propsJSON != "" && !json.Valid([]byte(propsJSON)) {
		return nil, fmt.Errorf("%w: props_jsonb is not valid JSON", errs.ErrInvalid)
	}
	// Endpoints must exist.
	endpoints, err := u.nodes.GetMany(ctx, []uint64{srcID, dstID})
	if err != nil {
		return nil, err
	}
	if _, ok := endpoints[srcID]; !ok {
		return nil, fmt.Errorf("%w: src node %d", errs.ErrNotFound, srcID)
	}
	if _, ok := endpoints[dstID]; !ok {
		return nil, fmt.Errorf("%w: dst node %d", errs.ErrNotFound, dstID)
	}
	// Relation type must be registered.
	if _, err := u.types.Get(ctx, typ); err != nil {
		return nil, fmt.Errorf("relation type %q: %w", typ, err)
	}
	r := &model.Relation{SrcID: srcID, DstID: dstID, Type: typ, PropsJSON: propsJSON}
	if err := u.relations.Create(ctx, r); err != nil {
		return nil, err
	}
	if u.log != nil {
		u.log.Info("relation created",
			slog.Uint64("id", r.ID), slog.Uint64("src", srcID), slog.Uint64("dst", dstID), slog.String("type", typ))
	}
	return r, nil
}

// UpdateRelation rewrites only the props bag — the (src, dst, type)
// triple is the identity and changing it is just "delete and recreate".
func (u *Usecase) UpdateRelation(ctx context.Context, id uint64, propsJSON string) error {
	if u.relations == nil {
		return errs.ErrNotWiredYet
	}
	if propsJSON != "" && !json.Valid([]byte(propsJSON)) {
		return fmt.Errorf("%w: props_jsonb is not valid JSON", errs.ErrInvalid)
	}
	return u.relations.Update(ctx, id, propsJSON)
}

// GetRelation returns one relation by id.
func (u *Usecase) GetRelation(ctx context.Context, id uint64) (*model.Relation, error) {
	if u.relations == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.relations.Get(ctx, id)
}

// ListRelations returns relations matching f.
func (u *Usecase) ListRelations(ctx context.Context, f RelationListFilter) ([]*model.Relation, int64, error) {
	if u.relations == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	items, err := u.relations.List(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	total, err := u.relations.Count(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// DeleteRelation soft-deletes one edge.
func (u *Usecase) DeleteRelation(ctx context.Context, id uint64) error {
	if u.relations == nil {
		return errs.ErrNotWiredYet
	}
	return u.relations.Delete(ctx, id)
}

// ---------- DeviceMirror ----------------------------------------------------

// EnsureNodeForDevice is the device→topology mirror entry point. Called
// from the edge register flow (via edge.Usecase.NodeMirror) after a
// fresh device row lands. Look up an existing Node by
// (type='device', name=deviceName) — keyed on name because that's
// what the device row already carries; if not found, create one.
// Returns the node id either way; caller writes it back to
// device.node_id.
//
// Implementation note: we do NOT key the lookup on device.id (e.g. a
// props_jsonb.device_id field) because the migration backfill might
// race with a real-time write. Keying on (type, name) is good enough
// for v1 since device names are operator-edited and a duplicate name
// already means "the operator is treating these as the same logical
// thing".
func (u *Usecase) EnsureNodeForDevice(ctx context.Context, deviceID uint64, deviceName string) (uint64, error) {
	if u.nodes == nil {
		return 0, errs.ErrNotWiredYet
	}
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		return 0, fmt.Errorf("%w: device name required", errs.ErrInvalid)
	}
	rows, err := u.nodes.List(ctx, NodeListFilter{
		Type:  string(model.NodeTypeDevice),
		Q:     deviceName,
		Limit: 50,
	})
	if err != nil {
		return 0, err
	}
	for _, n := range rows {
		// Q is substring (case-insensitive); require exact match here.
		if strings.EqualFold(n.Name, deviceName) {
			return n.ID, nil
		}
	}
	n := &model.Node{Type: string(model.NodeTypeDevice), Name: deviceName}
	if err := u.nodes.Create(ctx, n); err != nil {
		return 0, err
	}
	if u.log != nil {
		u.log.Info("topology: mirrored device → node",
			slog.Uint64("device_id", deviceID), slog.Uint64("node_id", n.ID), slog.String("name", deviceName))
	}
	return n.ID, nil
}

// DeleteNodeForDevice removes the topology node owned by a deleted device.
// Device deletion is the source of truth here, so every relation touching
// the node is removed before the node itself.
func (u *Usecase) DeleteNodeForDevice(ctx context.Context, deviceID, nodeID uint64) error {
	if u.nodes == nil || u.relations == nil {
		return errs.ErrNotWiredYet
	}
	if deviceID == 0 || nodeID == 0 {
		return fmt.Errorf("%w: device_id and node_id are required", errs.ErrInvalid)
	}
	n, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if n.Type != string(model.NodeTypeDevice) {
		return fmt.Errorf("%w: topology node %d is %q, not device", errs.ErrInvalid, nodeID, n.Type)
	}
	rels, err := u.relations.List(ctx, RelationListFilter{SrcOrDstID: nodeID})
	if err != nil {
		return err
	}
	for _, rel := range rels {
		if rel == nil {
			continue
		}
		if err := u.relations.Delete(ctx, rel.ID); err != nil && !errors.Is(err, errs.ErrNotFound) {
			return err
		}
	}
	if err := u.nodes.Delete(ctx, nodeID); err != nil {
		return err
	}
	if u.log != nil {
		u.log.Info("topology: removed deleted device node",
			slog.Uint64("device_id", deviceID),
			slog.Uint64("node_id", nodeID),
			slog.Int("relations", len(rels)))
	}
	return nil
}

// EnsureKubernetesCluster mirrors one onboarded Kubernetes cluster into the
// generic topology graph as a type=cluster node. currentNodeID is the stable
// k8s_clusters.node_id pointer when the k8s table already has one.
func (u *Usecase) EnsureKubernetesCluster(ctx context.Context, clusterID uint64, currentNodeID *uint64, name, uid, mode, status string) (uint64, error) {
	if u.nodes == nil {
		return 0, errs.ErrNotWiredYet
	}
	if clusterID == 0 {
		return 0, fmt.Errorf("%w: cluster_id required", errs.ErrInvalid)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("%w: cluster name required", errs.ErrInvalid)
	}
	propsJSON, err := kubernetesClusterPropsJSON(clusterID, uid, mode, status)
	if err != nil {
		return 0, err
	}
	if currentNodeID != nil && *currentNodeID != 0 {
		n, err := u.nodes.Get(ctx, *currentNodeID)
		if err == nil && n.Type == string(model.NodeTypeCluster) {
			if n.Name != name || n.PropsJSON != propsJSON {
				if err := u.nodes.Update(ctx, n.ID, name, propsJSON); err != nil {
					return 0, err
				}
			}
			return n.ID, nil
		}
		if err != nil && !errors.Is(err, errs.ErrNotFound) {
			return 0, err
		}
	}
	if n, err := u.findKubernetesClusterNode(ctx, clusterID, name); err != nil {
		return 0, err
	} else if n != nil {
		if n.Name != name || n.PropsJSON != propsJSON {
			if err := u.nodes.Update(ctx, n.ID, name, propsJSON); err != nil {
				return 0, err
			}
		}
		return n.ID, nil
	}
	n := &model.Node{Type: string(model.NodeTypeCluster), Name: name, PropsJSON: propsJSON}
	if err := u.nodes.Create(ctx, n); err != nil {
		return 0, err
	}
	if u.log != nil {
		u.log.Info("topology: mirrored kubernetes cluster",
			slog.Uint64("k8s_cluster_id", clusterID), slog.Uint64("node_id", n.ID), slog.String("name", name))
	}
	return n.ID, nil
}

func (u *Usecase) findKubernetesClusterNode(ctx context.Context, clusterID uint64, name string) (*model.Node, error) {
	rows, err := u.nodes.List(ctx, NodeListFilter{
		Type:  string(model.NodeTypeCluster),
		Q:     name,
		Limit: 100,
	})
	if err != nil {
		return nil, err
	}
	var fallback *model.Node
	for _, n := range rows {
		if !strings.EqualFold(n.Name, name) {
			continue
		}
		match, owned := topologyPropsMatchKubernetesCluster(n.PropsJSON, clusterID)
		if match {
			return n, nil
		}
		if !owned && fallback == nil {
			fallback = n
		}
	}
	return fallback, nil
}

// EnsureKubernetesNodeMembership mirrors one Kubernetes node's backing device
// into the graph as device --member_of--> Kubernetes cluster.
func (u *Usecase) EnsureKubernetesNodeMembership(ctx context.Context, clusterNodeID, deviceNodeID, clusterID, deviceID uint64, nodeName, nodeUID string) error {
	if u.nodes == nil || u.relations == nil || u.types == nil {
		return errs.ErrNotWiredYet
	}
	if clusterNodeID == 0 || deviceNodeID == 0 || clusterID == 0 || deviceID == 0 {
		return fmt.Errorf("%w: cluster_node_id, device_node_id, cluster_id and device_id required", errs.ErrInvalid)
	}
	propsJSON, err := kubernetesNodeMembershipPropsJSON(clusterID, deviceID, nodeName, nodeUID)
	if err != nil {
		return err
	}
	existing, err := u.relations.List(ctx, RelationListFilter{
		SrcID: deviceNodeID,
		DstID: clusterNodeID,
		Type:  model.RelMemberOf,
		Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if existing[0].PropsJSON != propsJSON {
			return u.relations.Update(ctx, existing[0].ID, propsJSON)
		}
		return nil
	}
	_, err = u.CreateRelation(ctx, deviceNodeID, clusterNodeID, model.RelMemberOf, propsJSON)
	return err
}

// PruneKubernetesNodeMemberships removes stale Kubernetes-owned member_of
// edges under a mirrored Kubernetes cluster. Manual member_of edges are left
// untouched because they do not carry source=kubernetes in props.
func (u *Usecase) PruneKubernetesNodeMemberships(ctx context.Context, clusterNodeID, clusterID uint64, keepDeviceNodeIDs []uint64) error {
	if u.relations == nil {
		return errs.ErrNotWiredYet
	}
	if clusterNodeID == 0 || clusterID == 0 {
		return fmt.Errorf("%w: cluster_node_id and cluster_id required", errs.ErrInvalid)
	}
	keep := make(map[uint64]struct{}, len(keepDeviceNodeIDs))
	for _, id := range keepDeviceNodeIDs {
		if id != 0 {
			keep[id] = struct{}{}
		}
	}
	rels, err := u.relations.List(ctx, RelationListFilter{
		DstID: clusterNodeID,
		Type:  model.RelMemberOf,
	})
	if err != nil {
		return err
	}
	for _, rel := range rels {
		if _, ok := keep[rel.SrcID]; ok {
			continue
		}
		if match, owned := topologyPropsMatchKubernetesCluster(rel.PropsJSON, clusterID); owned && match {
			if err := u.relations.Delete(ctx, rel.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func kubernetesClusterPropsJSON(clusterID uint64, uid, mode, status string) (string, error) {
	props := struct {
		Source       string `json:"source"`
		K8sClusterID uint64 `json:"k8s_cluster_id"`
		ClusterUID   string `json:"k8s_cluster_uid,omitempty"`
		Mode         string `json:"mode,omitempty"`
		Status       string `json:"status,omitempty"`
	}{
		Source:       "kubernetes",
		K8sClusterID: clusterID,
		ClusterUID:   strings.TrimSpace(uid),
		Mode:         strings.TrimSpace(mode),
		Status:       strings.TrimSpace(status),
	}
	b, err := json.Marshal(props)
	if err != nil {
		return "", fmt.Errorf("marshal k8s cluster topology props: %w", err)
	}
	return string(b), nil
}

func kubernetesNodeMembershipPropsJSON(clusterID, deviceID uint64, nodeName, nodeUID string) (string, error) {
	props := struct {
		Source       string `json:"source"`
		K8sClusterID uint64 `json:"k8s_cluster_id"`
		DeviceID     uint64 `json:"device_id"`
		NodeName     string `json:"k8s_node_name,omitempty"`
		NodeUID      string `json:"k8s_node_uid,omitempty"`
	}{
		Source:       "kubernetes",
		K8sClusterID: clusterID,
		DeviceID:     deviceID,
		NodeName:     strings.TrimSpace(nodeName),
		NodeUID:      strings.TrimSpace(nodeUID),
	}
	b, err := json.Marshal(props)
	if err != nil {
		return "", fmt.Errorf("marshal k8s membership topology props: %w", err)
	}
	return string(b), nil
}

func topologyPropsMatchKubernetesCluster(propsJSON string, clusterID uint64) (match bool, owned bool) {
	propsJSON = strings.TrimSpace(propsJSON)
	if propsJSON == "" {
		return false, false
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		return false, false
	}
	source, _ := props["source"].(string)
	if source != "kubernetes" {
		return false, false
	}
	id, ok := jsonUint64(props["k8s_cluster_id"])
	return ok && id == clusterID, true
}

func jsonUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case float64:
		if n < 0 || n != float64(uint64(n)) {
			return 0, false
		}
		return uint64(n), true
	case int:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case uint64:
		return n, true
	case string:
		var out uint64
		if _, err := fmt.Sscanf(n, "%d", &out); err != nil {
			return 0, false
		}
		return out, true
	default:
		return 0, false
	}
}

// ---------- RelationType ----------------------------------------------------

// RegisterRelationType is the operator entry point for adding new
// relation kinds. Mandatory: name, direction, semantics_tag. Built-in
// types are owned by the migrator; the validator rejects collisions
// with the seed set.
func (u *Usecase) RegisterRelationType(ctx context.Context, rt model.RelationType) (*model.RelationType, error) {
	if u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	rt.Name = strings.TrimSpace(rt.Name)
	rt.DisplayName = strings.TrimSpace(rt.DisplayName)
	rt.DisplayNameEN = strings.TrimSpace(rt.DisplayNameEN)
	rt.Direction = strings.TrimSpace(rt.Direction)
	rt.SemanticsTag = strings.TrimSpace(rt.SemanticsTag)
	if rt.Name == "" {
		return nil, fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	if !model.IsValidDirection(rt.Direction) {
		return nil, fmt.Errorf("%w: direction must be one of src_to_dst|dst_to_src|bidirectional", errs.ErrInvalid)
	}
	if !model.IsValidSemanticsTag(rt.SemanticsTag) {
		return nil, fmt.Errorf("%w: semantics_tag must be one of the canonical buckets (hard_dep / runtime_dep / aggregation / redundancy / observation / traffic / annotation)", errs.ErrInvalid)
	}
	// Reject overwriting built-in rows. Lookup; if existing and
	// Builtin=true, refuse.
	existing, err := u.types.Get(ctx, rt.Name)
	if err == nil && existing != nil && existing.Builtin {
		return nil, fmt.Errorf("%w: %q is a built-in relation type", errs.ErrConflict, rt.Name)
	}
	// Operator-registered rows are never Builtin.
	rt.Builtin = false
	if err := u.types.Upsert(ctx, &rt); err != nil {
		return nil, err
	}
	if u.log != nil {
		u.log.Info("relation type registered",
			slog.String("name", rt.Name), slog.String("direction", rt.Direction), slog.String("semantics_tag", rt.SemanticsTag))
	}
	return &rt, nil
}

// ListRelationTypes returns every registered relation type (built-in
// + operator-added) sorted by name.
func (u *Usecase) ListRelationTypes(ctx context.Context) ([]*model.RelationType, error) {
	if u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.types.List(ctx)
}

// GetRelationType returns one relation type by name.
func (u *Usecase) GetRelationType(ctx context.Context, name string) (*model.RelationType, error) {
	if u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.types.Get(ctx, name)
}

// DeleteRelationType removes an operator-registered type. Built-in rows
// can't be deleted (returned as ErrConflict); types with existing
// relations referencing them can't either (deleting would orphan
// `relations.type → relation_types.name`).
func (u *Usecase) DeleteRelationType(ctx context.Context, name string) error {
	if u.types == nil {
		return errs.ErrNotWiredYet
	}
	existing, err := u.types.Get(ctx, name)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return fmt.Errorf("%w: %q is a built-in relation type", errs.ErrConflict, name)
	}
	refs, err := u.types.CountRelationsByType(ctx, name)
	if err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("%w: %d relation(s) still reference %q", errs.ErrConflict, refs, name)
	}
	return u.types.Delete(ctx, name)
}

// ---------- NodeType --------------------------------------------------------

// RegisterNodeType is the operator entry for adding new node kinds.
// Mandatory: name, display_name. Tier defaults to 99 (catch-all
// bottom band) if 0 isn't explicitly intended — operators meaning
// "top tier" should pass tier=0 explicitly.
func (u *Usecase) RegisterNodeType(ctx context.Context, nt model.NodeType) (*model.NodeType, error) {
	if u.nodeTypes == nil {
		return nil, errs.ErrNotWiredYet
	}
	nt.Name = strings.TrimSpace(nt.Name)
	nt.DisplayName = strings.TrimSpace(nt.DisplayName)
	nt.DisplayNameEN = strings.TrimSpace(nt.DisplayNameEN)
	if nt.Name == "" {
		return nil, fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	if nt.DisplayName == "" {
		// Reasonable fallback: display_name = name. Operators wanting
		// localised label should pass it explicitly; this just makes
		// "minimum viable" registration painless.
		nt.DisplayName = nt.Name
	}
	if nt.Tier < 0 {
		nt.Tier = 99
	}
	// Reject overwriting a builtin row — those are the migrator's
	// territory. Custom rows are upsertable (Update is just re-register).
	existing, err := u.nodeTypes.Get(ctx, nt.Name)
	if err == nil && existing != nil && existing.Builtin {
		return nil, fmt.Errorf("%w: %q is a built-in node type", errs.ErrConflict, nt.Name)
	}
	nt.Builtin = false
	if err := u.nodeTypes.Upsert(ctx, &nt); err != nil {
		return nil, err
	}
	if u.log != nil {
		u.log.Info("node type registered",
			slog.String("name", nt.Name), slog.String("display_name", nt.DisplayName), slog.Int("tier", nt.Tier))
	}
	return &nt, nil
}

// ListNodeTypes returns every registered node type (builtin +
// operator) sorted by tier then name. UI uses this to build the chip
// bar labels — display_name shows in the chip, name stays the
// canonical key the rest of the system filters on.
func (u *Usecase) ListNodeTypes(ctx context.Context) ([]*model.NodeType, error) {
	if u.nodeTypes == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.nodeTypes.List(ctx)
}

// GetNodeType returns one type by name.
func (u *Usecase) GetNodeType(ctx context.Context, name string) (*model.NodeType, error) {
	if u.nodeTypes == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.nodeTypes.Get(ctx, name)
}

// DeleteNodeType removes an operator-registered type. Refuses if the
// type is builtin or if any `nodes` rows still reference it (would
// orphan them and break the chip filter logic).
func (u *Usecase) DeleteNodeType(ctx context.Context, name string) error {
	if u.nodeTypes == nil {
		return errs.ErrNotWiredYet
	}
	existing, err := u.nodeTypes.Get(ctx, name)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return fmt.Errorf("%w: %q is a built-in node type", errs.ErrConflict, name)
	}
	refs, err := u.nodeTypes.CountNodesByType(ctx, name)
	if err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("%w: %d node(s) still reference %q — delete them first", errs.ErrConflict, refs, name)
	}
	return u.nodeTypes.Delete(ctx, name)
}
