package main

import (
	"context"
	"testing"
)

type staticTelemetryBackendResolver struct {
	url         string
	user        string
	password    string
	tlsInsecure bool
}

func (r staticTelemetryBackendResolver) URL(context.Context) string { return r.url }

func (r staticTelemetryBackendResolver) Auth(context.Context) (string, string) {
	return r.user, r.password
}

func (r staticTelemetryBackendResolver) TLSInsecure(context.Context) bool {
	return r.tlsInsecure
}

func TestPluginEndpointResolverPublishesExternalSignalSettings(t *testing.T) {
	resolver := pluginEndpointResolver{
		publicURL: "https://manager.example",
		loki: staticTelemetryBackendResolver{
			url:         "https://loki.example",
			user:        "loki-user",
			password:    "loki-pass",
			tlsInsecure: true,
		},
		tempo: staticTelemetryBackendResolver{
			url:      "https://tempo.example/v1/traces",
			user:     "tempo-user",
			password: "tempo-pass",
		},
	}

	logs, err := resolver.ResolveTelemetryTarget(context.Background(), "logs")
	if err != nil {
		t.Fatalf("resolve logs: %v", err)
	}
	if logs.Endpoint != "https://loki.example/otlp/v1/logs" || logs.BasicUser != "loki-user" || logs.BasicPassword != "loki-pass" || !logs.TLSInsecure || logs.UseTelemetryCredential {
		t.Fatalf("logs target = %#v", logs)
	}
	traces, err := resolver.ResolveTelemetryTarget(context.Background(), "traces")
	if err != nil {
		t.Fatalf("resolve traces: %v", err)
	}
	if traces.Endpoint != "https://tempo.example/v1/traces" || traces.BasicUser != "tempo-user" || traces.BasicPassword != "tempo-pass" || traces.TLSInsecure || traces.UseTelemetryCredential {
		t.Fatalf("traces target = %#v", traces)
	}
}

func TestPluginEndpointResolverFallsBackToManagerForInternalSeeds(t *testing.T) {
	resolver := pluginEndpointResolver{
		publicURL: "https://manager.example/",
		loki:      staticTelemetryBackendResolver{url: "http://loki:3100"},
		tempo:     staticTelemetryBackendResolver{url: "http://tempo:4318"},
	}

	logs, err := resolver.ResolveTelemetryTarget(context.Background(), "logs")
	if err != nil {
		t.Fatalf("resolve logs: %v", err)
	}
	traces, err := resolver.ResolveTelemetryTarget(context.Background(), "traces")
	if err != nil {
		t.Fatalf("resolve traces: %v", err)
	}
	if logs.Endpoint != "https://manager.example/loki/otlp/v1/logs" || !logs.UseTelemetryCredential {
		t.Fatalf("logs target = %#v", logs)
	}
	if traces.Endpoint != "https://manager.example/v1/traces" || !traces.UseTelemetryCredential {
		t.Fatalf("traces target = %#v", traces)
	}
}
