package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	topologybiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	store "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	topologymodel "github.com/ongridio/ongrid/internal/manager/model/topology"
)

func newTopologyUC(t *testing.T) *topologybiz.Usecase {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return topologybiz.NewUsecase(
		store.NewNodeRepo(db),
		store.NewRelationRepo(db),
		store.NewRelationTypeRepo(db),
		store.NewNodeTypeRepo(db),
		nil,
	)
}

// Build the graph:
//
//	app(checkout) <-member_of-  service(order-api) -depends_on-> service(db)
//	                                                |
//	                                                v deployed_on
//	                                            device(host-1)
//
// member_of doesn't propagate failure, depends_on / deployed_on do.
func seedGraph(t *testing.T, uc *topologybiz.Usecase) (appID, orderID, dbID, hostID uint64) {
	t.Helper()
	ctx := context.Background()
	app, err := uc.CreateNode(ctx, string(topologymodel.NodeTypeApp), "checkout", "")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	order, err := uc.CreateNode(ctx, string(topologymodel.NodeTypeService), "order-api", "")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	db, err := uc.CreateNode(ctx, string(topologymodel.NodeTypeService), "db", "")
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	host, err := uc.CreateNode(ctx, string(topologymodel.NodeTypeDevice), "host-1", "")
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	if _, err := uc.CreateRelation(ctx, order.ID, app.ID, topologymodel.RelMemberOf, ""); err != nil {
		t.Fatalf("member_of: %v", err)
	}
	if _, err := uc.CreateRelation(ctx, order.ID, db.ID, topologymodel.RelDependsOn, ""); err != nil {
		t.Fatalf("depends_on: %v", err)
	}
	if _, err := uc.CreateRelation(ctx, order.ID, host.ID, topologymodel.RelDeployedOn, ""); err != nil {
		t.Fatalf("deployed_on: %v", err)
	}
	return app.ID, order.ID, db.ID, host.ID
}

func TestExpandTopologyOnlyPropagating(t *testing.T) {
	uc := newTopologyUC(t)
	_, orderID, dbID, hostID := seedGraph(t, uc)
	tool := aiopstools.NewExpandTopologyTool(uc, nil, nil)

	args, _ := json.Marshal(map[string]any{
		"node_id": orderID,
		"depth":   2,
	})
	out, err := tool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	var got struct {
		Center struct {
			NodeID uint64 `json:"node_id"`
		} `json:"center"`
		Hits []struct {
			NodeID       uint64 `json:"node_id"`
			NodeName     string `json:"node_name"`
			RelationType string `json:"relation_type"`
			Propagates   bool   `json:"propagates_failure"`
		} `json:"reachable"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got.Center.NodeID != orderID {
		t.Fatalf("wrong center: %d", got.Center.NodeID)
	}
	// Should reach db (depends_on) and host (deployed_on), NOT app
	// (member_of with only_propagating=true default).
	seen := map[uint64]string{}
	for _, h := range got.Hits {
		seen[h.NodeID] = h.RelationType
	}
	if _, ok := seen[dbID]; !ok {
		t.Errorf("expected db in hits, got %+v", seen)
	}
	if _, ok := seen[hostID]; !ok {
		t.Errorf("expected host in hits, got %+v", seen)
	}
	if rt, ok := seen[dbID]; ok && rt != topologymodel.RelDependsOn {
		t.Errorf("db reached via %q, want depends_on", rt)
	}
}

func TestExpandTopologyIncludesNonPropagating(t *testing.T) {
	uc := newTopologyUC(t)
	appID, orderID, _, _ := seedGraph(t, uc)
	tool := aiopstools.NewExpandTopologyTool(uc, nil, nil)

	args, _ := json.Marshal(map[string]any{
		"node_id":          orderID,
		"only_propagating": false,
	})
	out, err := tool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(out, `"node_id":`) {
		t.Fatalf("no hits in output: %s", out)
	}
	// app should now be reachable via member_of.
	var got struct {
		Hits []struct {
			NodeID uint64 `json:"node_id"`
		} `json:"reachable"`
	}
	_ = json.Unmarshal([]byte(out), &got)
	found := false
	for _, h := range got.Hits {
		if h.NodeID == appID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected app (%d) reachable with only_propagating=false, got %+v", appID, got.Hits)
	}
}

func TestExpandTopologyRequiresStart(t *testing.T) {
	uc := newTopologyUC(t)
	tool := aiopstools.NewExpandTopologyTool(uc, nil, nil)

	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatalf("expected error when neither node_id nor device_id supplied")
	}
}

func TestFindTopologyNodeSubstring(t *testing.T) {
	uc := newTopologyUC(t)
	_, orderID, _, _ := seedGraph(t, uc)
	tool := aiopstools.NewFindTopologyNodeTool(uc, nil)

	out, err := tool.InvokableRun(context.Background(), `{"name":"order"}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	var got struct {
		Hits []struct {
			NodeID uint64 `json:"node_id"`
			Name   string `json:"name"`
			Type   string `json:"type"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(got.Hits) != 1 || got.Hits[0].NodeID != orderID {
		t.Fatalf("want exactly order-api, got %+v", got.Hits)
	}
}
