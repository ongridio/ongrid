package edge

import (
	"context"
	"fmt"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// HostPluginConfigRepo 保存 Host 的长期配置，不以安装实例为主键。
type HostPluginConfigRepo interface {
	ListByHost(context.Context, uint64) ([]*model.PluginConfig, error)
	GetByHost(context.Context, uint64, string) (*model.PluginConfig, error)
	UpsertHost(context.Context, uint64, *model.PluginConfig) (*model.PluginConfig, error)
	DeleteHost(context.Context, uint64, string) error
}

// HostConfigTarget 同时校验 Host 存在并解析当前安装；无安装时返回零，仍允许编辑长期意图。
type HostConfigTarget func(context.Context, uint64) (uint64, *AutoAPMDefaults, error)

func (uc *PluginConfigUC) SetHostConfigTarget(resolve HostConfigTarget) { uc.hostTarget = resolve }

type hostConfigView struct {
	PluginConfigRepo
	host HostPluginConfigRepo
	id   uint64
}

func (v hostConfigView) ListByEdge(ctx context.Context, _ uint64) ([]*model.PluginConfig, error) {
	return v.host.ListByHost(ctx, v.id)
}
func (v hostConfigView) Get(ctx context.Context, _ uint64, name string) (*model.PluginConfig, error) {
	return v.host.GetByHost(ctx, v.id, name)
}
func (v hostConfigView) Upsert(ctx context.Context, in *model.PluginConfig) (*model.PluginConfig, error) {
	copy := *in
	copy.EdgeID = 0
	return v.host.UpsertHost(ctx, v.id, &copy)
}
func (v hostConfigView) Delete(ctx context.Context, _ uint64, name string) error {
	return v.host.DeleteHost(ctx, v.id, name)
}

func (uc *PluginConfigUC) forHost(ctx context.Context, id uint64) (*PluginConfigUC, uint64, error) {
	repo, ok := uc.repo.(HostPluginConfigRepo)
	if !ok || uc.hostTarget == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	edgeID, defaults, err := uc.hostTarget(ctx, id)
	if err != nil {
		return nil, 0, err
	}

	// 按请求复制无锁依赖容器，不修改共享 UC 或其 providers。
	view := *uc
	view.repo = hostConfigView{PluginConfigRepo: uc.repo, host: repo, id: id}
	view.hostScoped = true
	if edgeID == 0 {
		view.kubernetesAutoAPM = nil
		view.kubernetesLogPaths = nil
	}
	view.autoAPMEnvironment = func(context.Context, uint64) (string, string, error) {
		if defaults == nil {
			return "", "", nil
		}
		return defaults.Environment, defaults.ClusterName, nil
	}
	return &view, edgeID, nil
}
func (uc *PluginConfigUC) ListForHost(ctx context.Context, id uint64) ([]PluginRow, uint64, error) {
	view, edgeID, err := uc.forHost(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	rows, err := view.ListForUI(ctx, edgeID)
	out := make([]PluginRow, 0, len(rows))
	for _, row := range rows {
		if model.DurableHostPlugin(row.PluginName) {
			out = append(out, row)
		}
	}
	return out, edgeID, err
}
func (uc *PluginConfigUC) SetForHost(ctx context.Context, id uint64, name string, in SetInput) (*PluginRow, error) {
	if !model.DurableHostPlugin(name) {
		return nil, fmt.Errorf("%w: plugin requires an installation-scoped session", errs.ErrInvalid)
	}
	view, edgeID, err := uc.forHost(ctx, id)
	if err != nil {
		return nil, err
	}
	if edgeID == 0 && name == model.PluginNameDatabaseMetrics {
		return nil, fmt.Errorf("%w: database credentials require a current installation", errs.ErrConflict)
	}
	return view.Set(ctx, edgeID, name, in)
}
