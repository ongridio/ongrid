package agenttrace

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type ReceiverTarget struct {
	Location      string `json:"location"`
	ClusterID     string `json:"cluster_id"`
	DeviceID      string `json:"device_id"`
	InstanceID    string `json:"instance_id"`
	ContainerName string `json:"container_name"`
}

type Receiver struct {
	Endpoint string `json:"endpoint"`
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

type ReceiverResolver interface {
	ResolveReceiver(context.Context, Identity, ReceiverTarget) (Receiver, error)
}

func (s *Service) WithReceivers(resolver ReceiverResolver) *Service { s.receivers = resolver; return s }

type IngestionRequest struct {
	SDK      string
	Identity Identity
	Endpoint string
	Target   ReceiverTarget
}

type Ingestion struct {
	SDK         string            `json:"sdk"`
	Identity    Identity          `json:"identity"`
	Receiver    Receiver          `json:"receiver"`
	Environment map[string]string `json:"environment"`
}

// Ingestion resolves a real receiver without requiring existing APM metrics.
// External endpoints are validated and returned, never contacted by Manager.
func (s *Service) Ingestion(ctx context.Context, req IngestionRequest) (*Ingestion, error) {
	if req.Identity.ServiceName == "" || req.Identity.ServiceNamespace == "" || req.Identity.Environment == "" {
		return nil, fmt.Errorf("%w: application, namespace and environment are required", errs.ErrInvalid)
	}
	for _, value := range []string{req.Identity.ServiceName, req.Identity.ServiceNamespace, req.Identity.Environment, req.Target.InstanceID, req.Target.ContainerName} {
		if len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("%w: invalid identity", errs.ErrInvalid)
		}
	}
	switch req.SDK {
	case "langsmith", "openinference", "openllmetry", "langfuse":
	default:
		return nil, fmt.Errorf("%w: unsupported SDK", errs.ErrInvalid)
	}
	out := &Ingestion{SDK: req.SDK, Identity: req.Identity, Environment: map[string]string{}}
	switch req.Target.Location {
	case "external":
		out.Receiver = Receiver{Endpoint: req.Endpoint, Location: "external"}
	case "kubernetes", "host":
		id := req.Target.ClusterID
		if req.Target.Location == "host" {
			id = req.Target.DeviceID
		}
		if n, err := strconv.ParseUint(id, 10, 64); err != nil || n == 0 {
			return nil, fmt.Errorf("%w: resource ID is required", errs.ErrInvalid)
		}
		if s.receivers == nil {
			return nil, errs.ErrNotWiredYet
		}
		var err error
		out.Receiver, err = s.receivers.ResolveReceiver(ctx, req.Identity, req.Target)
		if err != nil {
			return nil, err
		}
		if out.Receiver.Endpoint == "" {
			return out, nil
		}
	default:
		return nil, fmt.Errorf("%w: unsupported deployment location", errs.ErrInvalid)
	}
	endpoint, err := url.Parse(out.Receiver.Endpoint)
	if err != nil || len(out.Receiver.Endpoint) > 2048 || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("%w: provide an HTTP(S) Collector URL without credentials, query or fragment", errs.ErrInvalid)
	}
	endpoint.Path = strings.TrimSuffix(strings.TrimRight(endpoint.Path, "/"), "/v1/traces") + "/v1/traces"
	endpoint.RawPath = ""
	out.Receiver.Endpoint = endpoint.String()
	out.Environment = map[string]string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": out.Receiver.Endpoint,
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/protobuf",
		"OTEL_SERVICE_NAME":                  req.Identity.ServiceName,
		"OTEL_RESOURCE_ATTRIBUTES":           "service.namespace=" + url.PathEscape(req.Identity.ServiceNamespace) + ",deployment.environment.name=" + url.PathEscape(req.Identity.Environment),
	}
	return out, nil
}
