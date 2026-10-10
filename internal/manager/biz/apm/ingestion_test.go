package apm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testReceiverResolver struct {
	calls                       []Instance
	versionError, receiverError error
}

func (r *testReceiverResolver) ResolveReceiver(_ context.Context, _ Identity, in Instance) (Receiver, error) {
	r.calls = append(r.calls, in)
	return Receiver{Endpoint: "http://gateway.observability.svc:18418", Location: "kubernetes", Metrics: true}, r.receiverError
}
func (r *testReceiverResolver) ResolveVersionField(_ context.Context, _ Instance) (string, error) {
	return "metadata.labels['app.kubernetes.io/version']", r.versionError
}

func TestIngestionKeepsVerifiedReceiverWhenOptionalPodMetadataFails(t *testing.T) {
	for _, metadataErr := range []error{errors.New("get Pod/old-pod: kubernetes api not found"), errors.New("Pod metadata forbidden"), context.DeadlineExceeded} {
		t.Run(metadataErr.Error(), func(t *testing.T) {
			p := &fakeProm{result: `[{"metric":{"service_instance_id":"old-uid","k8s_cluster_id":"50","k8s_pod_name":"old-pod","k8s_namespace_name":"business"},"value":[1600,"1"]}]`}
			resolver := &testReceiverResolver{versionError: metadataErr}
			service := New(p, nil, nil).WithReceivers(resolver, nil)
			out, err := service.Ingestion(t.Context(), testQuery())
			if err != nil || out == nil || len(out.Targets) != 1 || out.Targets[0].Endpoint != "http://gateway.observability.svc:18418" || out.Targets[0].VersionFieldPath != "" {
				t.Fatalf("optional metadata blocked receiver or invented version: out=%+v err=%v", out, err)
			}
			resolver.receiverError = errors.New("gateway unavailable")
			if _, err := service.Ingestion(t.Context(), testQuery()); !errors.Is(err, resolver.receiverError) {
				t.Fatalf("required receiver error lost: %v", err)
			}
		})
	}
}

func TestIngestionDoesNotIgnoreRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := &fakeProm{result: `[{"metric":{"service_instance_id":"old-uid","k8s_cluster_id":"50","k8s_pod_name":"old-pod"},"value":[1600,"1"]}]`}
	resolver := &testReceiverResolver{versionError: context.Canceled}
	if _, err := New(p, nil, nil).WithReceivers(resolver, nil).Ingestion(ctx, testQuery()); !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation ignored: %v", err)
	}
}
func TestIngestionPreservesScopeAndResolvesEachClusterOnce(t *testing.T) {
	p := &fakeProm{result: `[{"metric":{"service_instance_id":"pod-one","cluster_id":"132","k8s_cluster_id":"50","device_id":"42","k8s_pod_name":"pod-one"},"value":[1600,"1"]},{"metric":{"service_instance_id":"pod-two","cluster_id":"132","k8s_cluster_id":"50","device_id":"43","k8s_pod_name":"pod-two"},"value":[1600,"1"]},{"metric":{"service_instance_id":"pod-one","cluster_id":"132","k8s_cluster_id":"50","device_id":"42"},"value":[1600,"1"]}]`}
	resolver := &testReceiverResolver{}
	q := testQuery()
	q.DeviceID = "42"
	out, err := New(p, nil, nil).WithReceivers(resolver, nil).Ingestion(t.Context(), q)
	if err != nil || len(out.Targets) != 2 || len(resolver.calls) != 1 {
		t.Fatalf("out=%+v calls=%+v err=%v", out, resolver.calls, err)
	}
	if out.Identity != q.Identity() || out.Targets[0].Instance.Pod != "pod-one" || out.Targets[0].Endpoint != "http://gateway.observability.svc:18418" || resolver.calls[0].K8sClusterID != "50" || out.Targets[0].VersionFieldPath != "metadata.labels['app.kubernetes.io/version']" {
		t.Fatalf("lost identity or receiver: %+v", out)
	}
	if !strings.Contains(p.expr, `device_id="42"`) || !strings.Contains(p.expr, `service_name="orders"`) || !strings.Contains(p.expr, `deployment_environment_name="production"`) {
		t.Fatalf("lost service scope: %s", p.expr)
	}
}
func TestIngestionDoesNotInventInstances(t *testing.T) {
	resolver := &testReceiverResolver{}
	out, err := New(&fakeProm{result: `[]`}, nil, nil).WithReceivers(resolver, nil).Ingestion(t.Context(), testQuery())
	if err != nil || len(out.Targets) != 0 || len(resolver.calls) != 0 {
		t.Fatalf("unexpected target: %+v %v", out, err)
	}
}

func TestIngestionRetainsContainerMetadataAcrossMetricSources(t *testing.T) {
	p := &fakeProm{result: `[{"metric":{"service_instance_id":"business.api.app-blue","device_id":"42"},"value":[1600,"1"]},{"metric":{"service_instance_id":"business.api.app-blue","device_id":"42","container_name":"app-blue"},"value":[1600,"1"]}]`}
	resolver := &testReceiverResolver{}
	out, err := New(p, nil, nil).WithReceivers(resolver, nil).Ingestion(t.Context(), testQuery())
	if err != nil || len(out.Targets) != 1 || len(resolver.calls) != 1 || resolver.calls[0].ContainerName != "app-blue" || out.Targets[0].Instance.ContainerName != "app-blue" {
		t.Fatalf("lost container metadata: out=%+v calls=%+v err=%v", out, resolver.calls, err)
	}
}
