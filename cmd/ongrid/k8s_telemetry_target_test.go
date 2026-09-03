package main

import (
	"context"
	"testing"

	managerbizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/promauth"
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
	if got := resolver.Endpoint(context.Background(), "logs"); got != "https://loki.example/loki/api/v1/push" {
		t.Fatalf("ordinary Edge logs endpoint = %q", got)
	}
	edgeLogs, err := (logsLokiTargetResolver{resolver: resolver}).ResolveLokiTarget(context.Background())
	if err != nil {
		t.Fatalf("resolve ordinary Edge logs: %v", err)
	}
	if edgeLogs.Endpoint != logs.Endpoint || edgeLogs.BasicUser != logs.BasicUser || edgeLogs.BasicPassword != logs.BasicPassword ||
		edgeLogs.TLSInsecure != logs.TLSInsecure || edgeLogs.UseEdgeCredentials {
		t.Fatalf("ordinary Edge logs target = %#v", edgeLogs)
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
	if got := resolver.Endpoint(context.Background(), "logs"); got != "https://manager.example/loki/api/v1/push" {
		t.Fatalf("ordinary Edge logs endpoint = %q", got)
	}
	if traces.Endpoint != "https://manager.example/v1/traces" || !traces.UseTelemetryCredential {
		t.Fatalf("traces target = %#v", traces)
	}
	profiles, err := resolver.ResolveTelemetryTarget(context.Background(), "profiles")
	if err != nil {
		t.Fatalf("resolve profiles: %v", err)
	}
	if profiles.Endpoint != "https://manager.example/v1development/profiles" || !profiles.UseTelemetryCredential {
		t.Fatalf("profiles target = %#v", profiles)
	}
}

func TestK8sRemoteWriteResolverPublishesPrometheusTLSSettings(t *testing.T) {
	promResolver := managerbizsetting.NewPromResolver(nil, "https://prom.example", "https://prom.example/api/v1/write", promauth.TLSConfig{
		Insecure: true,
		CAPEM:    "test-ca",
	})
	target, err := (k8sRemoteWriteResolver{
		resolver: promResolver,
		prom:     config.PromConfig{Enabled: true},
	}).ResolveRemoteWrite(context.Background())
	if err != nil {
		t.Fatalf("resolve remote write: %v", err)
	}
	if target.Endpoint != "https://prom.example/api/v1/write" || !target.TLSInsecure || target.TLSCAPEM != "test-ca" {
		t.Fatalf("remote write target = %#v", target)
	}
}
