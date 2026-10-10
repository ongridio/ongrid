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
	unique := []Instance{}
	seen := map[[5]string]int{}
	for _, instance := range instances {
		key := [5]string{instance.InstanceID, instance.DeviceID, instance.ClusterID, instance.K8sClusterID, instance.Version}
		if index, ok := seen[key]; ok {
			if unique[index].Pod == "" {
				unique[index].Pod = instance.Pod
			}
			if unique[index].Namespace == "" {
				unique[index].Namespace = instance.Namespace
			}
			if unique[index].ContainerName == "" {
				unique[index].ContainerName = instance.ContainerName
			}
			if unique[index].TargetID == "" {
				unique[index].TargetID = instance.TargetID
			}
			continue
		}
		seen[key] = len(unique)
		unique = append(unique, instance)
	}
	instances = unique
	if len(instances) > 100 {
		return nil, fmt.Errorf("%w: narrow the service resource scope", errs.ErrBudgetExceeded)
	}
	out := &Ingestion{Identity: q.Identity(), Targets: []IngestionTarget{}}
	receivers := map[string]Receiver{}
	versions := map[[3]string]string{}
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
			if err != nil {
				return nil, fmt.Errorf("resolve application receiver: %w", err)
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
			}
			versions[versionKey] = version
		}
		out.Targets = append(out.Targets, IngestionTarget{Instance: instance, Receiver: receiver, VersionFieldPath: version})
	}
	return out, nil
}
