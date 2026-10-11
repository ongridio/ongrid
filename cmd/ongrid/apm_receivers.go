package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	apm "github.com/ongridio/ongrid/internal/manager/biz/apm"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type apmReceiverResolver struct {
	clusters interface {
		GetCluster(context.Context, uint64) (*k8smodel.Cluster, error)
	}
	hosts interface {
		LookupEdgeForDevice(context.Context, uint64, devicemodel.EdgeDeviceRelationType) (uint64, error)
	}
	caller interface {
		Call(context.Context, uint64, string, []byte) ([]byte, error)
	}
}

func (r apmReceiverResolver) ResolveVersionField(ctx context.Context, instance apm.Instance) (string, error) {
	if instance.Pod == "" || instance.Namespace == "" {
		return "", nil
	}
	cluster := instance.K8sClusterID
	if cluster == "" {
		cluster = instance.ClusterID
	}
	id, err := strconv.ParseUint(cluster, 10, 64)
	if err != nil || id == 0 {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	c, err := r.clusters.GetCluster(ctx, id)
	if err != nil {
		return "", err
	}
	if c.ControllerEdgeID == nil {
		return "", nil
	}
	body, err := json.Marshal(tunnel.KubernetesDescribeResourceRequest{ClusterID: id, Kind: "Pod", Namespace: instance.Namespace, Name: instance.Pod})
	if err != nil {
		return "", err
	}
	raw, err := r.caller.Call(ctx, *c.ControllerEdgeID, tunnel.MethodDescribeK8sResource, body)
	if err != nil {
		return "", fmt.Errorf("read application Pod metadata: %w", err)
	}
	var response tunnel.KubernetesDescribeResourceResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("decode Pod response: %w", err)
	}
	var pod struct {
		Metadata struct {
			Name, Namespace string
			Labels          map[string]string
		}
	}
	if err := json.Unmarshal(response.Object, &pod); err != nil {
		return "", fmt.Errorf("decode Pod metadata: %w", err)
	}
	if response.ClusterID != id || pod.Metadata.Name != instance.Pod || pod.Metadata.Namespace != instance.Namespace {
		return "", fmt.Errorf("application Pod identity does not match the observed instance")
	}
	if pod.Metadata.Labels["app.kubernetes.io/version"] != "" {
		return "metadata.labels['app.kubernetes.io/version']", nil
	}
	return "", nil
}

func (r apmReceiverResolver) ResolveReceiver(ctx context.Context, identity apm.Identity, instance apm.Instance) (apm.Receiver, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cluster := instance.K8sClusterID
	// Only pod-labelled legacy telemetry uses cluster_id as a registration ID.
	if cluster == "" && instance.Pod != "" {
		cluster = instance.ClusterID
	}
	if cluster != "" {
		id, err := strconv.ParseUint(cluster, 10, 64)
		if err != nil || id == 0 {
			return apm.Receiver{Reason: "resource_unlinked"}, nil
		}
		c, err := r.clusters.GetCluster(ctx, id)
		if errors.Is(err, errs.ErrNotFound) {
			return apm.Receiver{Reason: "resource_unlinked"}, nil
		}
		if err != nil {
			return apm.Receiver{}, err
		}
		if c.ControllerEdgeID == nil || c.ControllerNamespace == "" {
			return apm.Receiver{Reason: "controller_unavailable"}, nil
		}
		// This is the Service name owned by the supported ongrid-edge Chart.
		const gateway = "ongrid-edge-telemetry-gateway"
		body, err := json.Marshal(tunnel.KubernetesDescribeResourceRequest{ClusterID: id, Kind: "Service", Namespace: c.ControllerNamespace, Name: gateway})
		if err != nil {
			return apm.Receiver{}, err
		}
		raw, err := r.caller.Call(ctx, *c.ControllerEdgeID, tunnel.MethodDescribeK8sResource, body)
		if err != nil {
			return apm.Receiver{}, fmt.Errorf("read cluster gateway Service: %w", err)
		}
		var response tunnel.KubernetesDescribeResourceResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			return apm.Receiver{}, fmt.Errorf("decode gateway response: %w", err)
		}
		var service struct {
			Metadata struct {
				Name      string
				Namespace string
				Labels    map[string]string
			}
			Spec struct {
				Type  string
				Ports []struct {
					Name     string
					Port     int
					Protocol string
				}
			}
		}
		if err := json.Unmarshal(response.Object, &service); err != nil {
			return apm.Receiver{}, fmt.Errorf("decode gateway Service: %w", err)
		}
		if response.ClusterID != id || service.Metadata.Name != gateway || service.Metadata.Namespace != c.ControllerNamespace || service.Metadata.Labels["app.kubernetes.io/component"] != "telemetry-gateway" || service.Spec.Type == "ExternalName" {
			return apm.Receiver{Reason: "receiver_unavailable"}, nil
		}
		for _, port := range service.Spec.Ports {
			if port.Name == "otlp-http" && port.Protocol == "TCP" && port.Port > 0 && port.Port <= 65535 {
				return apm.Receiver{Endpoint: "http://" + net.JoinHostPort(gateway+"."+c.ControllerNamespace+".svc", strconv.Itoa(port.Port)), Location: "kubernetes", Metrics: true}, nil
			}
		}
		return apm.Receiver{Reason: "receiver_unavailable"}, nil
	}
	deviceID, err := strconv.ParseUint(instance.DeviceID, 10, 64)
	if err != nil || deviceID == 0 {
		return apm.Receiver{Reason: "resource_unlinked"}, nil
	}
	if instance.InstanceID == "" {
		return apm.Receiver{Reason: "process_unlinked"}, nil
	}
	request := tunnel.ApplicationReceiverRequest{ContainerName: instance.ContainerName, InstanceID: instance.InstanceID, ServiceName: identity.ServiceName, Namespace: identity.ServiceNamespace, Environment: identity.Environment}
	edgeID, err := r.hosts.LookupEdgeForDevice(ctx, deviceID, devicemodel.EdgeDeviceRelationHost)
	if errors.Is(err, errs.ErrNotFound) {
		return apm.Receiver{Reason: "resource_unlinked"}, nil
	}
	if err != nil {
		return apm.Receiver{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return apm.Receiver{}, err
	}
	raw, err := r.caller.Call(ctx, edgeID, tunnel.MethodGetApplicationReceiver, body)
	if err != nil {
		return apm.Receiver{}, fmt.Errorf("read host application receiver: %w", err)
	}
	var receiver tunnel.ApplicationReceiverResponse
	if err := json.Unmarshal(raw, &receiver); err != nil {
		return apm.Receiver{}, fmt.Errorf("decode host application receiver: %w", err)
	}
	return apm.Receiver{Endpoint: receiver.Endpoint, Location: receiver.Location, Metrics: receiver.Metrics, Reason: receiver.Reason, TargetID: receiver.TargetID}, nil
}
