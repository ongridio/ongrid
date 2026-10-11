package apm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
	receivers := map[string]*Receiver{}
	var workers sync.WaitGroup
	receiverCtx, cancelReceivers := ingestionLookupContext(ctx)
	// The 100-instance limit bounds fan-out; every distinct receiver gets the same deadline.
	for _, instance := range instances {
		key := ingestionReceiverKey(instance)
		if _, ok := receivers[key]; ok {
			continue
		}
		receiver := &Receiver{}
		receivers[key] = receiver
		workers.Go(func() {
			resolved, err := s.receivers.ResolveReceiver(receiverCtx, q.Identity(), instance)
			if err != nil {
				s.log.WarnContext(ctx, "APM ingestion receiver unavailable", "device_id", instance.DeviceID, "cluster_id", instance.ClusterID, "instance_id", instance.InstanceID, "error", err)
				resolved = Receiver{Reason: "receiver_unavailable"}
			}
			*receiver = resolved
		})
	}
	workers.Wait()
	cancelReceivers()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	type versionResult struct {
		field      string
		unverified bool
	}
	versions := map[[3]string]*versionResult{}
	versionCtx, cancelVersions := ingestionLookupContext(ctx)
	for _, instance := range instances {
		key := ingestionReceiverKey(instance)
		receiver := receivers[key]
		versionKey := [3]string{key, instance.Namespace, instance.Pod}
		if _, ok := versions[versionKey]; ok || receiver.Location != "kubernetes" || receiver.Endpoint == "" {
			continue
		}
		version := &versionResult{}
		versions[versionKey] = version
		workers.Go(func() {
			field, err := s.receivers.ResolveVersionField(versionCtx, instance)
			if err != nil {
				// Optional Pod metadata must not consume the request's remaining deadline.
				s.log.WarnContext(ctx, "APM ingestion version metadata unavailable", "pod", instance.Pod, "namespace", instance.Namespace, "error", err)
				version.unverified = true
				return
			}
			version.field = field
		})
	}
	workers.Wait()
	cancelVersions()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := &Ingestion{Identity: q.Identity(), Targets: []IngestionTarget{}}
	deferred := []IngestionTarget{}
	for _, instance := range instances {
		key := ingestionReceiverKey(instance)
		target := IngestionTarget{Instance: instance, Receiver: *receivers[key]}
		if version := versions[[3]string{key, instance.Namespace, instance.Pod}]; version != nil {
			target.VersionFieldPath = version.field
			if version.unverified {
				deferred = append(deferred, target)
				continue
			}
		}
		out.Targets = append(out.Targets, target)
	}
	out.Targets = append(out.Targets, deferred...)
	return out, nil
}

func ingestionReceiverKey(instance Instance) string {
	if instance.K8sClusterID != "" {
		return "k8s:" + instance.K8sClusterID
	}
	if instance.Pod != "" {
		return "k8s:" + instance.ClusterID
	}
	return "host:" + instance.DeviceID + ":" + instance.InstanceID
}

func ingestionLookupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := 8 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		// Leave time for the next phase and for returning healthy results.
		budget = min(budget, time.Until(deadline)/2)
	}
	return context.WithTimeout(ctx, budget)
}
