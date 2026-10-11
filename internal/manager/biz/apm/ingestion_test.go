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
	receiverErrors              map[string]error
	versionErrors               map[string]error
}

func (r *testReceiverResolver) ResolveReceiver(_ context.Context, _ Identity, in Instance) (Receiver, error) {
	r.calls = append(r.calls, in)
	if err := r.receiverErrors[in.DeviceID]; err != nil {
		return Receiver{}, err
	}
	return Receiver{Endpoint: "http://gateway.observability.svc:18418", Location: "kubernetes", Metrics: true}, r.receiverError
}
func (r *testReceiverResolver) ResolveVersionField(_ context.Context, in Instance) (string, error) {
	if err := r.versionErrors[in.Pod]; err != nil {
		return "", err
	}
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
		})
	}
}

func TestIngestionKeepsHealthyReceiverWhenReplicaUnavailable(t *testing.T) {
	for _, receiverErr := range []error{errors.New("edge offline"), errors.New("unsupported RPC method"), context.DeadlineExceeded} {
		t.Run(receiverErr.Error(), func(t *testing.T) {
			p := &fakeProm{result: `[{"metric":{"service_instance_id":"a-old-replica","device_id":"42"},"value":[1600,"1"]},{"metric":{"service_instance_id":"z-live-replica","device_id":"43"},"value":[1600,"1"]}]`}
			resolver := &testReceiverResolver{receiverErrors: map[string]error{"42": receiverErr}}
			service := New(p, nil, nil).WithReceivers(resolver, nil)
			out, err := service.Ingestion(t.Context(), testQuery())
			if err != nil || out == nil || len(out.Targets) != 2 || len(resolver.calls) != 2 {
				t.Fatalf("unavailable replica blocked ingestion: out=%+v calls=%+v err=%v", out, resolver.calls, err)
			}
			for _, target := range out.Targets {
				if target.Instance.DeviceID == "42" && (target.Reason != "receiver_unavailable" || target.Endpoint != "") {
					t.Fatalf("unavailable receiver not reported: %+v", target)
				}
				if target.Instance.DeviceID == "43" && target.Endpoint != "http://gateway.observability.svc:18418" {
					t.Fatalf("healthy receiver discarded: %+v", target)
				}
			}
		})
	}
}

func TestIngestionReportsUnavailableReceiverWithoutUsingPartialEndpoint(t *testing.T) {
	p := &fakeProm{result: `[{"metric":{"service_instance_id":"old-replica","device_id":"42"},"value":[1600,"1"]}]`}
	resolver := &testReceiverResolver{receiverError: errors.New("gateway unavailable")}
	out, err := New(p, nil, nil).WithReceivers(resolver, nil).Ingestion(t.Context(), testQuery())
	if err != nil || out == nil || len(out.Targets) != 1 || out.Targets[0].Reason != "receiver_unavailable" || out.Targets[0].Endpoint != "" || out.Targets[0].VersionFieldPath != "" {
		t.Fatalf("unverified receiver returned: out=%+v err=%v", out, err)
	}
}

func TestIngestionDoesNotIgnoreRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := &fakeProm{result: `[{"metric":{"service_instance_id":"old-uid","k8s_cluster_id":"50","k8s_pod_name":"old-pod"},"value":[1600,"1"]}]`}
	for _, receiverErr := range []error{nil, context.Canceled} {
		resolver := &testReceiverResolver{versionError: context.Canceled, receiverError: receiverErr}
		if _, err := New(p, nil, nil).WithReceivers(resolver, nil).Ingestion(ctx, testQuery()); !errors.Is(err, context.Canceled) {
			t.Fatalf("request cancellation ignored: %v", err)
		}
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

func TestIngestionPrefersVerifiedCurrentPodOverDeletedPod(t *testing.T) {
	p := &fakeProm{result: `[{"metric":{"service_instance_id":"a-old","k8s_cluster_id":"50","k8s_pod_name":"old","k8s_namespace_name":"app"},"value":[1600,"1"]},{"metric":{"service_instance_id":"z-live","k8s_cluster_id":"50","k8s_pod_name":"live","k8s_namespace_name":"app"},"value":[1600,"1"]},{"metric":{"service_instance_id":"z-live","k8s_cluster_id":"50","device_id":"42"},"value":[1600,"1"]}]`}
	resolver := &testReceiverResolver{versionErrors: map[string]error{"old": errors.New("pod deleted")}}
	out, err := New(p, nil, nil).WithReceivers(resolver, nil).Ingestion(t.Context(), testQuery())
	if err != nil || len(out.Targets) != 2 || out.Targets[0].Instance.Pod != "live" || out.Targets[0].VersionFieldPath == "" || out.Targets[1].Endpoint == "" {
		t.Fatalf("lost current default or historical receiver: out=%+v err=%v", out, err)
	}
}
