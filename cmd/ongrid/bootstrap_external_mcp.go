package main

import (
	"fmt"

	"github.com/go-chi/chi/v5"
	"log/slog"
	"net/url"
	"strings"

	tokenbiz "github.com/ongridio/ongrid/internal/iam/biz/integrationtoken"
	tokendata "github.com/ongridio/ongrid/internal/iam/data/integrationtoken"
	userdata "github.com/ongridio/ongrid/internal/iam/data/user/sqlite"
	iamserver "github.com/ongridio/ongrid/internal/iam/server"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	data "github.com/ongridio/ongrid/internal/manager/data/externalmcp"
	server "github.com/ongridio/ongrid/internal/manager/server/externalmcp"
	service "github.com/ongridio/ongrid/internal/manager/service/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/prometheus/client_golang/prometheus"
)

type externalMCPModule struct {
	tokens  *iamserver.TokenHandler
	handler *server.Handler
	engine  *biz.Engine
}

func bootstrapExternalMCP(cfg *config.Config, log *slog.Logger, reg *prometheus.Registry, storage *storageModule, settings *settingsModule, devices *devicesModule, tunnel *tunnelModule, aiops *aiopsModule, extensions *extensionsModule) (*externalMCPModule, error) {
	auth := tokenbiz.New(tokendata.New(storage.db), userdata.NewRepo(storage.db), settings.auditUC)
	baseURL := strings.TrimRight(cfg.PublicURL, "/")
	m := &externalMCPModule{tokens: iamserver.NewTokenHandler(auth, cfg.ExternalMCP.Enabled, cfg.ExternalMCP.WritesEnabled, baseURL+"/api/v1/integrations/mcp")}
	if !cfg.ExternalMCP.Enabled {
		return m, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return nil, fmt.Errorf("external MCP requires an HTTPS ONGRID_PUBLIC_URL (HTTP loopback is allowed for local tests)")
	}
	for _, origin := range cfg.ExternalMCP.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("invalid external MCP origin")
		}
	}
	store := data.New(storage.db)
	engine := &biz.Engine{Auth: auth, Audit: settings.auditUC, Store: store, Approvals: extensions.approvalUC, WritesEnabled: cfg.ExternalMCP.WritesEnabled, BaseURL: baseURL, Log: log.With("component", "external-mcp")}
	baseTools := aiops.toolsReg.BuildBaseTools().AllTools()
	catalog, err := service.QueryTools(baseTools)
	if err != nil {
		return nil, err
	}
	builtinWrites, err := service.BuiltinWriteTools(baseTools)
	if err != nil {
		return nil, err
	}
	catalog = append(catalog, builtinWrites...)
	resources := service.Resources{Devices: devices.deviceUC, Alerts: aiops.alertSvc, Monitor: settings.monitorSvc}
	catalog = append(catalog, resources.Tools()...)
	if !cfg.FrontierClient.Disabled {
		actions, err := service.ActionTools(tools.NewRestartServiceTool(tunnel.fbClient, devices.edgeUC, devices.deviceUC, log), tools.NewPersistentK8sActionTool(tunnel.fbClient, devices.k8sSvc, log, store))
		if err != nil {
			return nil, err
		}
		catalog = append(catalog, actions...)
	}
	catalog = append(service.ProjectTools(catalog), engine.ControlTools()...)
	engine.Catalog, err = biz.NewCatalog(catalog)
	if err != nil {
		return nil, err
	}
	extensions.approvalUC.RegisterExecutor(biz.ApprovalKind, engine.Approve)
	extensions.approvalHandler.SetExternalOperations(engine)
	m.handler = server.New(engine, cfg.ExternalMCP.AllowedOrigins, reg)
	m.engine = engine
	return m, nil
}

func (m *externalMCPModule) addAPITools(router chi.Router) error {
	tools, err := server.APITools(router, m.engine.WritesEnabled)
	if err != nil {
		return err
	}
	for _, tool := range m.engine.Catalog {
		tools = append(tools, *tool)
	}
	catalog, err := biz.NewCatalog(tools)
	if err != nil {
		return err
	}
	m.engine.Catalog = catalog
	return nil
}
