package device

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type Environment struct {
	Environment          string `json:"environment"`
	EffectiveEnvironment string `json:"effective_environment"`
	InheritedEnvironment string `json:"inherited_environment"`
	ClusterName          string `json:"cluster_name"`
	Source               string `json:"source"`
}

func (u *Usecase) SetEnvironmentProvider(provider func(context.Context, uint64) (string, string, error), changed func(context.Context, uint64)) {
	u.clusterEnvironment, u.environmentChanged = provider, changed
}

func (u *Usecase) ResolveEnvironment(ctx context.Context, id uint64) (*Environment, error) {
	d, err := u.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &Environment{Source: "unset"}
	if d.Environment != nil {
		out.Environment = *d.Environment
	}
	if d.NodeID != nil && *d.NodeID != 0 && u.clusterEnvironment != nil {
		out.InheritedEnvironment, out.ClusterName, err = u.clusterEnvironment(ctx, *d.NodeID)
		if err != nil {
			return nil, fmt.Errorf("resolve device cluster environment: %w", err)
		}
	}
	out.EffectiveEnvironment = out.Environment
	if out.Environment != "" {
		out.Source = "device"
	} else if out.InheritedEnvironment != "" {
		out.EffectiveEnvironment, out.Source = out.InheritedEnvironment, "cluster"
	}
	return out, nil
}

func (u *Usecase) SetEnvironment(ctx context.Context, id uint64, value string) (*Environment, error) {
	if len(value) > 256 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n$") {
		return nil, fmt.Errorf("%w: invalid device environment", errs.ErrInvalid)
	}
	value = strings.TrimSpace(value)
	out, err := u.ResolveEnvironment(ctx, id)
	if err != nil {
		return nil, err
	}
	var links []*model.EdgeDevice
	if u.links != nil {
		links, err = u.links.ListEdgesForDevice(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("list device collectors: %w", err)
		}
	}
	if err := u.repo.UpdateEnvironment(ctx, id, value); err != nil {
		return nil, fmt.Errorf("save device environment: %w", err)
	}
	out.Environment, out.EffectiveEnvironment, out.Source = value, value, "device"
	if value == "" {
		out.EffectiveEnvironment, out.Source = out.InheritedEnvironment, "cluster"
		if out.InheritedEnvironment == "" {
			out.Source = "unset"
		}
	}
	if u.log != nil {
		u.log.InfoContext(ctx, "device environment updated", "device_id", id, "environment", value, "source", out.Source)
	}
	if u.environmentChanged != nil {
		for _, link := range links {
			if link.Type == model.EdgeDeviceRelationHost {
				u.environmentChanged(ctx, link.EdgeID)
			}
		}
	}
	return out, nil
}
