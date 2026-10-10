package main

import (
	"context"
	"encoding/json"
	"testing"

	apm "github.com/ongridio/ongrid/internal/manager/biz/apm"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type receiverClusters struct{ got uint64 }

func (c *receiverClusters) GetCluster(_ context.Context, id uint64) (*k8smodel.Cluster, error) {
	c.got = id
	edge := uint64(63)
	return &k8smodel.Cluster{ID: id, ControllerEdgeID: &edge, ControllerNamespace: "actual-namespace"}, nil
}

type receiverHosts struct{ got uint64 }

func (h *receiverHosts) LookupEdgeForDevice(_ context.Context, id uint64, relation devicemodel.EdgeDeviceRelationType) (uint64, error) {
	h.got = id
	if relation != devicemodel.EdgeDeviceRelationHost {
		panic("wrong relationship")
	}
	return 64, nil
}

type receiverCaller func(context.Context, uint64, string, []byte) ([]byte, error)

func (c receiverCaller) Call(ctx context.Context, id uint64, method string, body []byte) ([]byte, error) {
	return c(ctx, id, method, body)
}
func TestAPMReceiverReadsActualClusterServicePort(t *testing.T) {
	clusters := &receiverClusters{}
	resolver := apmReceiverResolver{clusters: clusters, caller: receiverCaller(func(_ context.Context, id uint64, method string, body []byte) ([]byte, error) {
		var req tunnel.KubernetesDescribeResourceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if id != 63 || method != tunnel.MethodDescribeK8sResource || req.ClusterID != 50 || req.Namespace != "actual-namespace" || req.Name != "ongrid-edge-telemetry-gateway" || req.IncludeEvents {
			t.Fatalf("unexpected read: %d %s %+v", id, method, req)
		}
		return json.Marshal(tunnel.KubernetesDescribeResourceResponse{ClusterID: 50, Object: json.RawMessage(`{"metadata":{"name":"ongrid-edge-telemetry-gateway","namespace":"actual-namespace","labels":{"app.kubernetes.io/component":"telemetry-gateway"}},"spec":{"type":"ClusterIP","ports":[{"name":"otlp-http","port":18418,"protocol":"TCP"}]}}`)})
	})}
	out, err := resolver.ResolveReceiver(t.Context(), apm.Identity{ServiceName: "orders", ServiceNamespace: "shop", Environment: "test"}, apm.Instance{ClusterID: "132", K8sClusterID: "50", DeviceID: "42"})
	if err != nil || clusters.got != 50 || out.Endpoint != "http://ongrid-edge-telemetry-gateway.actual-namespace.svc:18418" || !out.Metrics {
		t.Fatalf("out=%+v cluster=%d err=%v", out, clusters.got, err)
	}
}
func TestAPMReceiverDoesNotConfuseBareUnifiedClusterIDWithKubernetes(t *testing.T) {
	hosts := &receiverHosts{}
	resolver := apmReceiverResolver{hosts: hosts, caller: receiverCaller(func(_ context.Context, id uint64, method string, body []byte) ([]byte, error) {
		var req tunnel.ApplicationReceiverRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if id != 64 || method != tunnel.MethodGetApplicationReceiver || req.ProcessID != 0 || req.InstanceID != "host:123" || req.ServiceName != "orders" || req.Namespace != "shop" || req.Environment != "test" {
			t.Fatalf("unexpected read: %d %s %+v", id, method, req)
		}
		return json.Marshal(tunnel.ApplicationReceiverResponse{Endpoint: "http://172.23.0.1:4318", Location: "docker", Metrics: true})
	})}
	out, err := resolver.ResolveReceiver(t.Context(), apm.Identity{ServiceName: "orders", ServiceNamespace: "shop", Environment: "test"}, apm.Instance{ClusterID: "132", DeviceID: "42", InstanceID: "host:123"})
	if err != nil || hosts.got != 42 || out.Endpoint != "http://172.23.0.1:4318" || out.Location != "docker" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	for _, instance := range []apm.Instance{{ClusterID: "132"}, {DeviceID: "42"}} {
		out, err := resolver.ResolveReceiver(t.Context(), apm.Identity{ServiceName: "orders", ServiceNamespace: "shop", Environment: "test"}, instance)
		if err != nil || out.Endpoint != "" || out.Reason == "" {
			t.Fatalf("invented receiver: %+v %v", out, err)
		}
	}
}

func TestAPMReceiverUsesContainerNameInsteadOfAnExpiredHostPID(t *testing.T) {
	hosts := &receiverHosts{}
	resolver := apmReceiverResolver{hosts: hosts, caller: receiverCaller(func(_ context.Context, id uint64, method string, body []byte) ([]byte, error) {
		var req tunnel.ApplicationReceiverRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if id != 64 || method != tunnel.MethodGetApplicationReceiver || req.ProcessID != 0 || req.ContainerName != "app-blue" {
			t.Fatalf("unexpected container read: %d %s %+v", id, method, req)
		}
		return json.Marshal(tunnel.ApplicationReceiverResponse{Endpoint: "http://172.23.0.1:4318", Location: "docker"})
	})}
	for _, instanceID := range []string{"host:123", "business.api.app-blue"} {
		out, err := resolver.ResolveReceiver(t.Context(), apm.Identity{ServiceName: "orders", ServiceNamespace: "shop", Environment: "test"}, apm.Instance{DeviceID: "42", InstanceID: instanceID, ContainerName: "app-blue"})
		if err != nil || hosts.got != 42 || out.Endpoint != "http://172.23.0.1:4318" {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	}
}

func TestAPMVersionUsesPodLabelReferenceInsteadOfObservedValue(t *testing.T) {
	for _, version := range []string{"1.0.0", "2.0.0", ""} {
		t.Run(version, func(t *testing.T) {
			resolver := apmReceiverResolver{clusters: &receiverClusters{}, caller: receiverCaller(func(_ context.Context, edge uint64, method string, body []byte) ([]byte, error) {
				var req tunnel.KubernetesDescribeResourceRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatal(err)
				}
				if edge != 63 || method != tunnel.MethodDescribeK8sResource || req.ClusterID != 50 || req.Kind != "Pod" || req.Namespace != "business" || req.Name != "api-1" || req.IncludeEvents {
					t.Fatalf("unexpected Pod read: %d %s %+v", edge, method, req)
				}
				pod, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": "api-1", "namespace": "business", "labels": map[string]string{"app.kubernetes.io/version": version}}})
				return json.Marshal(tunnel.KubernetesDescribeResourceResponse{ClusterID: 50, Object: pod})
			})}
			field, err := resolver.ResolveVersionField(t.Context(), apm.Instance{ClusterID: "132", K8sClusterID: "50", Pod: "api-1", Namespace: "business", Version: "old-observed-version"})
			want := ""
			if version != "" {
				want = "metadata.labels['app.kubernetes.io/version']"
			}
			if err != nil || field != want {
				t.Fatalf("field=%q err=%v", field, err)
			}
		})
	}
}
