package logs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	managerbizedge "github.com/ongridio/ongrid/internal/manager/biz/edge"
	managerbizlogs "github.com/ongridio/ongrid/internal/manager/biz/logs"
	managermodeledge "github.com/ongridio/ongrid/internal/manager/model/edge"
	"golang.org/x/sync/errgroup"
)

type pluginConfigReloadNotifier interface {
	NotifyPluginConfigsChanged(ctx context.Context, edgeID uint64) error
}

// PluginReloadBroadcaster turns a selected backend change into bounded,
// best-effort reload hints for online Edges. The 60-second Edge pull loop is
// authoritative, so one disconnected Edge never changes the selection result.
type PluginReloadBroadcaster struct {
	edges    *managerbizedge.Usecase
	notifier pluginConfigReloadNotifier
	log      *slog.Logger
}

type ConnectionEdgeInventory struct {
	edges   *managerbizedge.Usecase
	configs *managerbizedge.PluginConfigUC
}

func (i ConnectionEdgeInventory) ListConnectionEdges(ctx context.Context) ([]managerbizlogs.ConnectionEdge, error) {
	if i.edges == nil {
		return nil, nil
	}
	edges, err := i.edges.List(ctx, managerbizedge.ListFilter{})
	if err != nil {
		return nil, err
	}
	items := make([]managerbizlogs.ConnectionEdge, 0, len(edges))
	for _, edge := range edges {
		if !isHostLogsConnectionEdge(edge) {
			continue
		}
		if i.configs != nil {
			enabled, enabledErr := i.configs.IsEnabled(ctx, edge.ID, "logs")
			if enabledErr != nil {
				return nil, fmt.Errorf("resolve logs policy for Edge %d: %w", edge.ID, enabledErr)
			}
			if !enabled {
				continue
			}
		}
		items = append(items, managerbizlogs.ConnectionEdge{EdgeID: edge.ID, Name: edge.Name, Online: edge.Status == "online"})
	}
	return items, nil
}

// isHostLogsConnectionEdge excludes control-plane-only Edge identities from a
// log connection check. The real-write check scopes every probe by the linked
// Host device; an identity without that link (for example the
// Kubernetes controller) cannot emit or verify such a probe and would make a
// connection check impossible even though no logs process runs on that identity.
func isHostLogsConnectionEdge(edge *managermodeledge.Edge) bool {
	return edge != nil && edge.ID != 0 && edge.DeviceID != nil && *edge.DeviceID != 0
}

func (b *PluginReloadBroadcaster) NotifyLogsBackendChanged(ctx context.Context) error {
	if b == nil || b.edges == nil || b.notifier == nil {
		return nil
	}
	edges, err := b.edges.List(ctx, managerbizedge.ListFilter{Status: "online"})
	if err != nil {
		return fmt.Errorf("list online Edges for logs reload: %w", err)
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for _, edge := range edges {
		if edge == nil || edge.ID == 0 {
			continue
		}
		edgeID := edge.ID
		group.Go(func() error {
			callCtx, cancel := context.WithTimeout(groupCtx, 5*time.Second)
			defer cancel()
			if notifyErr := b.notifier.NotifyPluginConfigsChanged(callCtx, edgeID); notifyErr != nil && b.log != nil {
				b.log.Warn("logs plugin reload hint failed; periodic pull will retry",
					slog.Uint64("edge_id", edgeID), slog.Any("err", notifyErr))
			}
			return nil
		})
	}
	return group.Wait()
}

func NewPluginReloadBroadcaster(edges *managerbizedge.Usecase, notifier pluginConfigReloadNotifier, log *slog.Logger) *PluginReloadBroadcaster {
	return &PluginReloadBroadcaster{edges: edges, notifier: notifier, log: log}
}

func NewConnectionEdgeInventory(edges *managerbizedge.Usecase, configs *managerbizedge.PluginConfigUC) ConnectionEdgeInventory {
	return ConnectionEdgeInventory{edges: edges, configs: configs}
}
