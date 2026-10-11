package apm

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type Receiver struct {
	Endpoint string `json:"endpoint"`
	Location string `json:"location"`
	Metrics  bool   `json:"metrics"`
	Reason   string `json:"reason,omitempty"`
	TargetID string `json:"target_id,omitempty"`
}

type ReceiverResolver interface {
	ResolveReceiver(context.Context, Identity, Instance) (Receiver, error)
	ResolveVersionField(context.Context, Instance) (string, error)
}

type IngestionTarget struct {
	Instance         Instance `json:"instance"`
	VersionFieldPath string   `json:"version_field_path,omitempty"`
	Receiver
}

type Ingestion struct {
	Identity Identity          `json:"identity"`
	Targets  []IngestionTarget `json:"targets"`
}

func (s *Service) WithReceivers(resolver ReceiverResolver, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s.receivers, s.log = resolver, log
	return s
}

func (s *Service) Ingestion(ctx context.Context, q Query) (*Ingestion, error) {
	if err := s.validateQuery(ctx, &q, true); err != nil {
		return nil, err
	}
	if s.receivers == nil {
		return nil, errs.ErrNotWiredYet
	}
	instances, err := s.runtimeInstances(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(instances) > 100 {
		return nil, fmt.Errorf("%w: narrow the service resource scope", errs.ErrBudgetExceeded)
	}
	out := &Ingestion{Identity: q.Identity(), Targets: []IngestionTarget{}}
	receivers := map[string]Receiver{}
	versions := map[[3]string]string{}
	unverified := map[[3]string]bool{}
	deferred := []IngestionTarget{}
	for _, instance := range instances {
		key := "host:" + instance.DeviceID + ":" + instance.InstanceID
		if instance.K8sClusterID != "" {
			key = "k8s:" + instance.K8sClusterID
		} else if instance.Pod != "" {
			key = "k8s:" + instance.ClusterID
		}
		receiver, ok := receivers[key]
		if !ok {
			receiver, err = s.receivers.ResolveReceiver(ctx, q.Identity(), instance)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				s.log.WarnContext(ctx, "APM ingestion receiver unavailable", "device_id", instance.DeviceID, "cluster_id", instance.ClusterID, "instance_id", instance.InstanceID, "error", err)
				receiver = Receiver{Reason: "receiver_unavailable"}
			}
			receivers[key] = receiver
		}
		versionKey := [3]string{key, instance.Namespace, instance.Pod}
		version, ok := versions[versionKey]
		if !ok && receiver.Location == "kubernetes" && receiver.Endpoint != "" {
			version, err = s.receivers.ResolveVersionField(ctx, instance)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				// Historical Pods can be deleted; optional version metadata must not block a verified receiver.
				s.log.WarnContext(ctx, "APM ingestion version metadata unavailable", "pod", instance.Pod, "namespace", instance.Namespace, "error", err)
				version = ""
				unverified[versionKey] = true
			}
			versions[versionKey] = version
		}
		target := IngestionTarget{Instance: instance, Receiver: receiver, VersionFieldPath: version}
		if unverified[versionKey] {
			deferred = append(deferred, target)
		} else {
			out.Targets = append(out.Targets, target)
		}
	}
	out.Targets = append(out.Targets, deferred...)
	return out, nil
}
