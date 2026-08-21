// Package logs owns the external log backend control plane and selected-backend
// query routing. It never receives log payloads from Edge collectors.
package logs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

const (
	DefaultBackendName      = "external-elasticsearch"
	SecretSlotESAPIKey      = "elasticsearch_api_key"
	managedLogsSecretPrefix = "ongrid-managed-logs-es-"
	elasticsearchCredType   = "elasticsearch"
	maxAPIKeyBytes          = 16 << 10
	maxCAPEMBytes           = 256 << 10
	maxBackendEndpoints     = 8
	managedSecretCleanupTTL = 5 * time.Second
)

var (
	datasetRE   = regexp.MustCompile(`^ongrid\.[a-z0-9][a-z0-9._-]{0,91}$`)
	namespaceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)
)

type Repo interface {
	SaveBackend(ctx context.Context, backend *model.Backend) error
	GetBackend(ctx context.Context, id uint64) (*model.Backend, error)
	LatestBackend(ctx context.Context) (*model.Backend, error)
	SelectedBackend(ctx context.Context) (*model.Backend, error)
	SelectBackend(ctx context.Context, id uint64, version string, testedAt time.Time) error
	SelectLoki(ctx context.Context) error
	GetAssignment(ctx context.Context, backendID, edgeID uint64) (*model.BackendAssignment, error)
	UpsertAssignment(ctx context.Context, assignment *model.BackendAssignment) error
	ListAssignments(ctx context.Context, backendID uint64) ([]*model.BackendAssignment, error)
}

// SecretResolver is deliberately read-only. Credential values are decrypted
// only while constructing an Elasticsearch client or answering the dedicated
// authenticated plugin-secret RPC.
type SecretResolver interface {
	ResolveFields(ctx context.Context, name string) (map[string]string, error)
}

// ManagedSecretStore is implemented by Manager's encrypted credential vault.
// It is intentionally an in-process capability rather than an HTTP API: direct
// API keys submitted with a log backend are write-only request fields and are
// converted to ordinary credential references before the backend is persisted.
type ManagedSecretStore interface {
	SecretResolver
	CreateManaged(ctx context.Context, name, credType, description string, fields map[string]string) error
	DeleteManaged(ctx context.Context, name string) error
}

type BackendChangeNotifier interface {
	NotifyLogsBackendChanged(ctx context.Context) error
}

// GrafanaSyncer is an optional, best-effort observer of selected log backend
// changes. Grafana failures never alter the selected backend.
type GrafanaSyncer interface {
	SyncElasticsearch(ctx context.Context, config pkggrafana.ElasticsearchDatasourceConfig) error
	SyncLoki(ctx context.Context) error
}

type HostDeviceResolver interface {
	LookupHostDevice(ctx context.Context, edgeID uint64) (uint64, error)
}

type ConnectionEdge struct {
	EdgeID uint64
	Name   string
	Online bool
}

// ConnectionEdgeInventory returns every Edge on which logs are enabled,
// including disconnected identities. It is used only by the explicitly
// requested connection check and never by backend selection.
type ConnectionEdgeInventory interface {
	ListConnectionEdges(ctx context.Context) ([]ConnectionEdge, error)
}

type SaveInput struct {
	Name               string   `json:"name"`
	WriteEndpoints     []string `json:"write_endpoints"`
	QueryEndpoint      string   `json:"query_endpoint"`
	Dataset            string   `json:"dataset"`
	Namespace          string   `json:"namespace"`
	WriteCredentialRef string   `json:"write_credential_ref,omitempty"`
	QueryCredentialRef string   `json:"query_credential_ref,omitempty"`
	WriteAPIKey        string   `json:"write_api_key,omitempty"`
	QueryAPIKey        string   `json:"query_api_key,omitempty"`
	ReuseWriteAPIKey   bool     `json:"reuse_write_api_key,omitempty"`
	CAPEM              string   `json:"ca_pem,omitempty"`
	PreserveCA         bool     `json:"preserve_ca,omitempty"`
	KibanaURL          string   `json:"kibana_url,omitempty"`
	TLSInsecure        bool     `json:"tls_insecure"`
}

type BackendView struct {
	ID                 uint64              `json:"id"`
	Name               string              `json:"name"`
	Type               model.BackendType   `json:"type"`
	CurrentBackend     string              `json:"current_backend"`
	CurrentBackendID   uint64              `json:"current_backend_id,omitempty"`
	Status             model.BackendStatus `json:"status"`
	Generation         uint64              `json:"generation"`
	WriteEndpoints     []string            `json:"write_endpoints"`
	QueryEndpoint      string              `json:"query_endpoint"`
	Dataset            string              `json:"dataset"`
	Namespace          string              `json:"namespace"`
	IndexPattern       string              `json:"index_pattern"`
	WriteCredentialRef string              `json:"write_credential_ref"`
	QueryCredentialRef string              `json:"query_credential_ref"`
	HasCustomCA        bool                `json:"has_custom_ca"`
	KibanaURL          string              `json:"kibana_url,omitempty"`
	TLSInsecure        bool                `json:"tls_insecure"`
	DetectedVersion    string              `json:"detected_version,omitempty"`
	LastTestAt         *time.Time          `json:"last_test_at,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

type BackendTestResult struct {
	Status          string    `json:"status"`
	DetectedVersion string    `json:"detected_version"`
	TestedAt        time.Time `json:"tested_at"`
}

type ConnectionStatus string

const (
	ConnectionStatusNotChecked ConnectionStatus = "not_checked"
	ConnectionStatusPending    ConnectionStatus = "pending"
	ConnectionStatusVerified   ConnectionStatus = "verified"
	ConnectionStatusFailed     ConnectionStatus = "failed"
	ConnectionStatusOffline    ConnectionStatus = "offline"
)

type EdgeConnection struct {
	EdgeID            uint64           `json:"edge_id"`
	EdgeName          string           `json:"edge_name,omitempty"`
	Online            bool             `json:"online"`
	Status            ConnectionStatus `json:"status"`
	DesiredGeneration uint64           `json:"desired_generation"`
	AppliedGeneration uint64           `json:"applied_generation"`
	LastCheckedAt     *time.Time       `json:"last_checked_at,omitempty"`
	LastError         string           `json:"last_error,omitempty"`
}

type BackendConnectionCheck struct {
	BackendID         uint64           `json:"backend_id"`
	Backend           string           `json:"backend"`
	Generation        uint64           `json:"generation"`
	ObservedAt        time.Time        `json:"observed_at"`
	Total             int              `json:"total"`
	Online            int              `json:"online"`
	Verified          int              `json:"verified"`
	Pending           int              `json:"pending"`
	Failed            int              `json:"failed"`
	Offline           int              `json:"offline"`
	AllOnlineVerified bool             `json:"all_online_verified"`
	Edges             []EdgeConnection `json:"edges"`
}

// RuntimeConfig is the non-sensitive overlay merged into the Edge logs plugin
// snapshot. APIKeyFile is added locally by Edge after secret materialization.
type RuntimeConfig struct {
	BackendID      uint64
	Backend        string
	Generation     uint64
	WriteEndpoints []string
	Dataset        string
	Namespace      string
	CAPEM          string
	TLSInsecure    bool
}

type PluginSecret struct {
	Generation uint64 `json:"generation"`
	Content    string `json:"content"`
	SHA256     string `json:"sha256"`
}

type Service struct {
	repo    Repo
	secrets SecretResolver
	loki    logquery.Searcher
	log     *slog.Logger

	operationMu sync.Mutex
	mu          sync.RWMutex
	notifier    BackendChangeNotifier
	grafana     GrafanaSyncer
	devices     HostDeviceResolver
	inventory   ConnectionEdgeInventory
	selectGuard func(context.Context) error
	cacheKey    string
	cachedES    *logquery.ElasticsearchClient
	lokiCheck   *lokiConnectionCheckSession
}

// lokiConnectionCheckSession is an ephemeral, Manager-issued real-write
// probe. Loki is built in rather than represented by a persisted Backend row,
// so the check only needs to live for the lifetime of the interactive
// operation. Its random generation makes every Edge re-materialize a fresh
// probe file and acknowledge the exact check the operator started.
type lokiConnectionCheckSession struct {
	Generation  uint64
	Assignments map[uint64]*model.BackendAssignment
}

func NewService(repo Repo, secrets SecretResolver, loki logquery.Searcher, loggers ...*slog.Logger) *Service {
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &Service{repo: repo, secrets: secrets, loki: loki, log: logger}
}

func (s *Service) SetBackendChangeNotifier(notifier BackendChangeNotifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifier = notifier
}

func (s *Service) SetGrafanaSyncer(syncer GrafanaSyncer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grafana = syncer
}

func (s *Service) SetHostDeviceResolver(resolver HostDeviceResolver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices = resolver
}

func (s *Service) SetConnectionEdgeInventory(inventory ConnectionEdgeInventory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inventory = inventory
}

// SetSelectGuard installs product-level preconditions that must pass before
// selecting an Elasticsearch configuration as the global log backend.
func (s *Service) SetSelectGuard(guard func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectGuard = guard
}

func (s *Service) Save(ctx context.Context, input SaveInput) (view *BackendView, retErr error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	createdManagedRefs := make([]string, 0, 2)
	backendPersisted := false
	defer func() {
		if retErr == nil || backendPersisted || len(createdManagedRefs) == 0 {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedSecretCleanupTTL)
		defer cancel()
		if cleanupErr := s.deleteManagedAPIKeys(cleanupCtx, createdManagedRefs); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleanup managed Elasticsearch credentials: %w", cleanupErr))
		}
	}()

	normalized, err := normalizeSaveInput(input)
	if err != nil {
		return nil, err
	}
	previous, loadErr := s.repo.LatestBackend(ctx)
	if loadErr != nil && !errors.Is(loadErr, errs.ErrNotFound) {
		return nil, loadErr
	}

	generation := uint64(1)
	editableInPlace := false
	if loadErr == nil {
		generation = previous.Generation
		switch previous.Status {
		case model.BackendStatusUnselected:
			// An unselected configuration remains editable in place. The
			// selected backend keeps serving reads and writes.
			editableInPlace = true
		default:
			generation++
		}
	}

	writeRef := normalized.WriteCredentialRef
	queryRef := normalized.QueryCredentialRef
	if loadErr == nil {
		// Direct key fields are write-only. An empty value means "keep the
		// current key", so clients never need to fetch plaintext to edit the
		// non-sensitive parts of an existing backend.
		if writeRef == "" {
			writeRef = previous.WriteCredentialRef
		}
		if queryRef == "" {
			queryRef = previous.QueryCredentialRef
		}
	}
	writeKey := normalized.WriteAPIKey
	if writeKey == "" {
		if writeRef == "" {
			return nil, fmt.Errorf("%w: write API key or credential ref is required", errs.ErrInvalid)
		}
		writeKey, err = s.apiKey(ctx, writeRef)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid write credential", errs.ErrInvalid)
		}
	}
	queryKey := normalized.QueryAPIKey
	if normalized.ReuseWriteAPIKey {
		queryKey = writeKey
	} else if queryKey == "" {
		if queryRef == "" {
			return nil, fmt.Errorf("%w: query API key or credential ref is required", errs.ErrInvalid)
		}
		queryKey, err = s.apiKey(ctx, queryRef)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid query credential", errs.ErrInvalid)
		}
	}
	// Resolve and compare every effective key before mutating a managed vault
	// row. A rejected request must not rotate a credential still referenced by
	// the currently saved configuration.
	if !normalized.ReuseWriteAPIKey && subtle.ConstantTimeCompare([]byte(writeKey), []byte(queryKey)) == 1 {
		return nil, fmt.Errorf("%w: write and query API keys must differ unless reuse_write_api_key is enabled", errs.ErrInvalid)
	}

	if normalized.WriteAPIKey != "" {
		writeRef, err = s.storeManagedAPIKey(ctx, "write", writeKey, generation)
		if err != nil {
			return nil, err
		}
		createdManagedRefs = append(createdManagedRefs, writeRef)
	}
	if normalized.ReuseWriteAPIKey || normalized.QueryAPIKey != "" {
		queryRef, err = s.storeManagedAPIKey(ctx, "query", queryKey, generation)
		if err != nil {
			return nil, err
		}
		createdManagedRefs = append(createdManagedRefs, queryRef)
	}
	if writeRef == queryRef {
		return nil, fmt.Errorf("%w: write and query credentials must be different", errs.ErrInvalid)
	}

	endpointsJSON, err := json.Marshal(normalized.WriteEndpoints)
	if err != nil {
		return nil, fmt.Errorf("encode Elasticsearch endpoints: %w", err)
	}
	backend := &model.Backend{
		Name:               normalized.Name,
		Type:               model.BackendTypeElasticsearch,
		Status:             model.BackendStatusUnselected,
		Generation:         generation,
		WriteEndpointsJSON: string(endpointsJSON),
		QueryEndpoint:      normalized.QueryEndpoint,
		Dataset:            normalized.Dataset,
		Namespace:          normalized.Namespace,
		IndexPattern:       indexPattern(normalized.Namespace),
		WriteCredentialRef: writeRef,
		QueryCredentialRef: queryRef,
		CAPEM:              normalized.CAPEM,
		KibanaURL:          normalized.KibanaURL,
		TLSInsecure:        normalized.TLSInsecure,
	}
	if normalized.PreserveCA && normalized.CAPEM == "" {
		if loadErr != nil {
			if errors.Is(loadErr, errs.ErrNotFound) {
				return nil, fmt.Errorf("%w: cannot preserve CA without an existing backend", errs.ErrInvalid)
			}
			return nil, loadErr
		}
		backend.CAPEM = previous.CAPEM
	}
	if editableInPlace {
		backend.ID = previous.ID
		backend.CreatedAt = previous.CreatedAt
	}
	if err := s.repo.SaveBackend(ctx, backend); err != nil {
		return nil, err
	}
	backendPersisted = true
	if editableInPlace {
		s.cleanupSupersededManagedAPIKeys(ctx, previous, backend)
	}
	s.invalidateCache()
	return s.view(ctx, backend)
}

func (s *Service) Get(ctx context.Context) (*BackendView, error) {
	backend, err := s.repo.LatestBackend(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, backend)
}

// Test validates the saved Elasticsearch query/write endpoints and their API
// key privileges without notifying Edge or changing the
// selected log backend. Select repeats these checks before switching the global
// read/write backend. Edge connectivity is checked independently after selection.
func (s *Service) Test(ctx context.Context, id uint64) (*BackendTestResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	backend, err := s.repo.GetBackend(ctx, id)
	if err != nil {
		return nil, err
	}
	switch backend.Status {
	case model.BackendStatusUnselected, model.BackendStatusSelected:
	default:
		return nil, fmt.Errorf("%w: wait for the current log backend operation to finish", errs.ErrConflict)
	}
	version, err := s.probeBackend(ctx, backend)
	if err != nil {
		return nil, err
	}
	return &BackendTestResult{
		Status:          "ok",
		DetectedVersion: version,
		TestedAt:        time.Now().UTC(),
	}, nil
}

func (s *Service) Select(ctx context.Context, id uint64) (*BackendView, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	backend, err := s.repo.GetBackend(ctx, id)
	if err != nil {
		return nil, err
	}
	if backend.Status == model.BackendStatusSelected {
		return s.view(ctx, backend)
	}
	if backend.Status != model.BackendStatusUnselected {
		return nil, fmt.Errorf("%w: Elasticsearch configuration cannot be selected", errs.ErrConflict)
	}
	if err := s.checkSelectGuard(ctx); err != nil {
		return nil, err
	}
	version, err := s.probeBackend(ctx, backend)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	backend.DetectedVersion = version
	backend.LastTestAt = &now
	// The Manager-side endpoint and privilege probe is the complete selection
	// gate. Device convergence is deliberately checked by a separate action.
	if err := s.selectBackend(ctx, backend); err != nil {
		return nil, err
	}
	backend, err = s.repo.GetBackend(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, backend)
}

// StartConnectionCheck creates a fresh, generation-bound probe for every Host
// Edge with logs enabled. It never changes the selected backend. Offline Edges
// retain a pending assignment and complete it after reconnecting.
func (s *Service) StartConnectionCheck(ctx context.Context, id uint64) (*BackendConnectionCheck, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if id == 0 {
		selected, err := s.repo.SelectedBackend(ctx)
		if err == nil {
			id = selected.ID
		} else if errors.Is(err, errs.ErrNotFound) {
			return s.startLokiConnectionCheck(ctx)
		} else {
			return nil, err
		}
	}

	backend, edges, err := s.selectedBackendConnectionInventory(ctx, id)
	if err != nil {
		return nil, err
	}
	assignments := make([]*model.BackendAssignment, 0, len(edges))
	for _, edge := range edges {
		probeID, probeErr := newProbeID()
		if probeErr != nil {
			return nil, probeErr
		}
		assignments = append(assignments, &model.BackendAssignment{
			BackendID: backend.ID, EdgeID: edge.EdgeID, DesiredGeneration: backend.Generation,
			Status: model.AssignmentStatusPending, ProbeID: probeID,
		})
	}
	for _, assignment := range assignments {
		if err := s.repo.UpsertAssignment(ctx, assignment); err != nil {
			return nil, err
		}
	}
	s.invalidateCache()
	if err := s.notify(ctx); err != nil {
		return nil, fmt.Errorf("notify Edge connection check: %w", err)
	}
	return s.connectionCheck(ctx, backend, edges)
}

// ConnectionCheck returns current online state plus the last generation-bound
// application and real-write result. Reading status never initiates a probe.
func (s *Service) ConnectionCheck(ctx context.Context, id uint64) (*BackendConnectionCheck, error) {
	if id == 0 {
		selected, err := s.repo.SelectedBackend(ctx)
		if err == nil {
			id = selected.ID
		} else if errors.Is(err, errs.ErrNotFound) {
			return s.lokiConnectionCheck(ctx)
		} else {
			return nil, err
		}
	}
	backend, edges, err := s.selectedBackendConnectionInventory(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.connectionCheck(ctx, backend, edges)
}

func (s *Service) selectedBackendConnectionInventory(ctx context.Context, id uint64) (*model.Backend, []ConnectionEdge, error) {
	backend, err := s.repo.GetBackend(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	selected, err := s.repo.SelectedBackend(ctx)
	if err != nil {
		return nil, nil, err
	}
	if backend.ID != selected.ID || backend.Generation != selected.Generation || backend.Status != model.BackendStatusSelected {
		return nil, nil, fmt.Errorf("%w: connection checks require the selected Elasticsearch backend", errs.ErrConflict)
	}
	edges, err := s.connectionEdges(ctx)
	if err != nil {
		return nil, nil, err
	}
	return backend, edges, nil
}

func (s *Service) connectionCheck(ctx context.Context, backend *model.Backend, edges []ConnectionEdge) (*BackendConnectionCheck, error) {
	assignments, err := s.repo.ListAssignments(ctx, backend.ID)
	if err != nil {
		return nil, err
	}
	byEdge := make(map[uint64]*model.BackendAssignment, len(assignments))
	for _, assignment := range assignments {
		if assignment != nil {
			byEdge[assignment.EdgeID] = assignment
		}
	}
	return summarizeConnectionCheck(backend.ID, string(backend.Type), backend.Generation, edges, byEdge), nil
}

func (s *Service) startLokiConnectionCheck(ctx context.Context) (*BackendConnectionCheck, error) {
	if s.loki == nil {
		return nil, fmt.Errorf("%w: built-in Loki query path is not configured", errs.ErrConflict)
	}
	edges, err := s.connectionEdges(ctx)
	if err != nil {
		return nil, err
	}
	generation, err := newConnectionCheckGeneration()
	if err != nil {
		return nil, err
	}
	assignments := make(map[uint64]*model.BackendAssignment, len(edges))
	for _, edge := range edges {
		probeID, probeErr := newProbeID()
		if probeErr != nil {
			return nil, probeErr
		}
		assignments[edge.EdgeID] = &model.BackendAssignment{
			EdgeID: edge.EdgeID, DesiredGeneration: generation,
			Status: model.AssignmentStatusPending, ProbeID: probeID,
		}
	}
	s.mu.Lock()
	s.lokiCheck = &lokiConnectionCheckSession{Generation: generation, Assignments: assignments}
	s.mu.Unlock()
	if err := s.notify(ctx); err != nil {
		return nil, fmt.Errorf("notify Edge Loki connection check: %w", err)
	}
	return summarizeConnectionCheck(0, "loki", generation, edges, assignments), nil
}

func (s *Service) lokiConnectionCheck(ctx context.Context) (*BackendConnectionCheck, error) {
	edges, err := s.connectionEdges(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	session := cloneLokiConnectionCheckSession(s.lokiCheck)
	s.mu.RUnlock()
	if session == nil {
		return nil, errs.ErrNotFound
	}
	return summarizeConnectionCheck(0, "loki", session.Generation, edges, session.Assignments), nil
}

func summarizeConnectionCheck(backendID uint64, backend string, generation uint64, edges []ConnectionEdge, byEdge map[uint64]*model.BackendAssignment) *BackendConnectionCheck {
	result := &BackendConnectionCheck{
		BackendID: backendID, Backend: backend, Generation: generation,
		ObservedAt: time.Now().UTC(), Total: len(edges), Edges: make([]EdgeConnection, 0, len(edges)),
	}
	for _, edge := range edges {
		item := EdgeConnection{
			EdgeID: edge.EdgeID, EdgeName: edge.Name, Online: edge.Online,
			Status: ConnectionStatusNotChecked, DesiredGeneration: generation,
		}
		assignment := byEdge[edge.EdgeID]
		if assignment != nil {
			item.DesiredGeneration = assignment.DesiredGeneration
			item.AppliedGeneration = assignment.AppliedGeneration
			item.LastError = assignment.LastError
			checkedAt := assignment.UpdatedAt
			if assignment.LastProbeAt != nil {
				checkedAt = *assignment.LastProbeAt
			}
			if assignment.LastWriteSuccessAt != nil {
				checkedAt = *assignment.LastWriteSuccessAt
			}
			if !checkedAt.IsZero() {
				item.LastCheckedAt = &checkedAt
			}
		}
		if !edge.Online {
			item.Status = ConnectionStatusOffline
			result.Offline++
		} else {
			result.Online++
			switch {
			case assignment == nil || assignment.DesiredGeneration != generation:
				item.Status = ConnectionStatusNotChecked
				result.Pending++
			case assignment.Status == model.AssignmentStatusFailed:
				item.Status = ConnectionStatusFailed
				result.Failed++
			case assignment.Status == model.AssignmentStatusVerified &&
				assignment.AppliedGeneration == generation && assignment.LastWriteSuccessAt != nil:
				item.Status = ConnectionStatusVerified
				result.Verified++
			default:
				item.Status = ConnectionStatusPending
				result.Pending++
			}
		}
		result.Edges = append(result.Edges, item)
	}
	result.AllOnlineVerified = result.Online > 0 && result.Verified == result.Online && result.Pending == 0 && result.Failed == 0
	return result
}

// SelectLoki switches the authoritative log backend to the built-in Loki path.
// It performs no device validation; connection checks are an independent,
// explicitly requested operation.
func (s *Service) SelectLoki(ctx context.Context) (*BackendView, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	if s.loki == nil {
		return nil, fmt.Errorf("%w: built-in Loki query path is not configured", errs.ErrConflict)
	}
	backend, err := s.repo.SelectedBackend(ctx)
	if errors.Is(err, errs.ErrNotFound) {
		latest, latestErr := s.repo.LatestBackend(ctx)
		if latestErr != nil {
			return nil, latestErr
		}
		return s.view(ctx, latest)
	}
	if err != nil {
		return nil, err
	}
	if err := s.repo.SelectLoki(ctx); err != nil {
		return nil, err
	}
	s.invalidateCache()
	s.syncGrafanaLokiAsync(ctx)
	if err := s.notify(ctx); err != nil {
		s.log.WarnContext(ctx, "Loki backend is selected but Edge notification failed", slog.Any("error", err))
	}
	backend, err = s.repo.GetBackend(ctx, backend.ID)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, backend)
}

func (s *Service) SelectedRuntime(ctx context.Context) (*RuntimeConfig, error) {
	backend, err := s.repo.SelectedBackend(ctx)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return runtimeConfig(backend)
}

// SelectedElasticsearchDatasource returns the selected Elasticsearch query
// configuration. A nil result means Loki is selected. Only the
// read-only query API key is resolved.
func (s *Service) SelectedElasticsearchDatasource(ctx context.Context) (*pkggrafana.ElasticsearchDatasourceConfig, error) {
	backend, err := s.repo.SelectedBackend(ctx)
	if errors.Is(err, errs.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.elasticsearchDatasourceConfig(ctx, backend)
}

// PluginRuntimeOverlay implements edge.PluginRuntimeOverlayProvider without
// importing the edge bounded context. Values are non-sensitive; the API key is
// fetched separately by the authenticated Edge and materialized as a file.
func (s *Service) PluginRuntimeOverlay(ctx context.Context, edgeID uint64, plugin string) (map[string]interface{}, error) {
	if plugin != "logs" {
		return nil, nil
	}
	backend, assignment, err := s.runtimeBackendForEdge(ctx, edgeID)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		overlay := map[string]interface{}{
			"backend":            "builtin_loki",
			"backend_generation": uint64(0),
		}
		if assignment := s.lokiConnectionAssignment(edgeID); assignment != nil {
			overlay["backend_id"] = uint64(0)
			overlay["backend_generation"] = assignment.DesiredGeneration
			overlay["log_probe_id"] = assignment.ProbeID
		}
		return overlay, nil
	}
	runtime, err := runtimeConfig(backend)
	if err != nil {
		return nil, err
	}
	overlay := map[string]interface{}{
		"backend":                                "external_elasticsearch",
		"backend_id":                             runtime.BackendID,
		"backend_generation":                     runtime.Generation,
		"elasticsearch_endpoints":                append([]string(nil), runtime.WriteEndpoints...),
		"elasticsearch_dataset":                  runtime.Dataset,
		"elasticsearch_namespace":                runtime.Namespace,
		"elasticsearch_ca_pem":                   runtime.CAPEM,
		"elasticsearch_tls_insecure_skip_verify": runtime.TLSInsecure,
		"elasticsearch_secret_slot":              SecretSlotESAPIKey,
	}
	if assignment != nil && assignment.ProbeID != "" {
		overlay["log_probe_id"] = assignment.ProbeID
	}
	return overlay, nil
}

// PluginSecretForEdge is called only from the authenticated tunnel handler.
// During apply, only explicitly assigned Edges may obtain the saved
// generation. Once selected, every authenticated Edge may obtain that selected
// generation. The request can never select an arbitrary vault entry.
func (s *Service) PluginSecretForEdge(ctx context.Context, edgeID uint64, plugin, slot string, generation uint64) (*PluginSecret, error) {
	if edgeID == 0 || plugin != "logs" || slot != SecretSlotESAPIKey {
		return nil, errs.ErrForbidden
	}
	backend, assignment, err := s.backendForEdgeGeneration(ctx, edgeID, generation)
	if err != nil {
		return nil, err
	}
	if generation == 0 || generation != backend.Generation {
		return nil, fmt.Errorf("%w: stale log backend generation", errs.ErrConflict)
	}
	fields, err := s.secrets.ResolveFields(ctx, backend.WriteCredentialRef)
	if err != nil {
		return nil, fmt.Errorf("resolve Elasticsearch write credential: %w", err)
	}
	apiKey := strings.TrimSpace(fields["api_key"])
	if apiKey == "" {
		return nil, fmt.Errorf("%w: Elasticsearch credential has no api_key", errs.ErrInvalid)
	}
	if assignment != nil {
		assignment.DesiredGeneration = backend.Generation
		// Pulling the same secret again is normal after an Edge restart. Do
		// not erase a previously acknowledged connection check or move it
		// back to pending.
		if assignment.AppliedGeneration != backend.Generation && assignment.Status != model.AssignmentStatusFailed {
			assignment.Status = model.AssignmentStatusPending
		}
		if err := s.repo.UpsertAssignment(ctx, assignment); err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256([]byte(apiKey))
	return &PluginSecret{Generation: backend.Generation, Content: apiKey, SHA256: hex.EncodeToString(sum[:])}, nil
}

// MarkApplied closes the real-path check: a successful local Collector start
// is not enough. Manager searches the target backend for the unique probe
// emitted by that Edge before marking the device verified.
func (s *Service) lokiConnectionAssignment(edgeID uint64) *model.BackendAssignment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lokiCheck == nil {
		return nil
	}
	assignment := s.lokiCheck.Assignments[edgeID]
	if assignment == nil || assignment.Status == model.AssignmentStatusFailed {
		return nil
	}
	return cloneBackendAssignment(assignment)
}

func (s *Service) markLokiConnectionApplied(ctx context.Context, edgeID, generation uint64, probeID, applyErr string) (bool, error) {
	s.mu.Lock()
	if s.lokiCheck == nil || s.lokiCheck.Generation != generation {
		s.mu.Unlock()
		return false, nil
	}
	assignment := s.lokiCheck.Assignments[edgeID]
	if assignment == nil || probeID == "" || assignment.ProbeID != probeID {
		s.mu.Unlock()
		return false, nil
	}
	now := time.Now().UTC()
	assignment.AppliedGeneration = generation
	assignment.Status = model.AssignmentStatusApplied
	assignment.LastProbeAt = &now
	assignment.UpdatedAt = now
	assignment.LastError = ""
	if strings.TrimSpace(applyErr) != "" {
		assignment.AppliedGeneration = 0
		assignment.Status = model.AssignmentStatusFailed
		assignment.LastError = truncate(strings.TrimSpace(applyErr), 1024)
		s.mu.Unlock()
		return true, nil
	}
	probe := cloneBackendAssignment(assignment)
	s.mu.Unlock()

	if err := s.verifyLokiEdgeProbe(ctx, nil, probe); err != nil {
		s.mu.Lock()
		if current := s.currentLokiConnectionAssignment(generation, edgeID, probeID); current != nil {
			current.Status = model.AssignmentStatusFailed
			current.LastError = safeProbeError(err)
			current.UpdatedAt = time.Now().UTC()
		}
		s.mu.Unlock()
		s.log.WarnContext(ctx, "selected Loki Edge connection probe failed",
			slog.Uint64("edge_id", edgeID), slog.Any("error", err))
		return true, nil
	}
	verifiedAt := time.Now().UTC()
	s.mu.Lock()
	if current := s.currentLokiConnectionAssignment(generation, edgeID, probeID); current != nil {
		current.Status = model.AssignmentStatusVerified
		current.LastWriteSuccessAt = &verifiedAt
		current.LastError = ""
		current.UpdatedAt = verifiedAt
	}
	s.mu.Unlock()
	return true, nil
}

// currentLokiConnectionAssignment must be called while s.mu is held.
func (s *Service) currentLokiConnectionAssignment(generation, edgeID uint64, probeID string) *model.BackendAssignment {
	if s.lokiCheck == nil || s.lokiCheck.Generation != generation {
		return nil
	}
	assignment := s.lokiCheck.Assignments[edgeID]
	if assignment == nil || assignment.ProbeID != probeID {
		return nil
	}
	return assignment
}

func (s *Service) MarkApplied(ctx context.Context, edgeID, generation uint64, probeID, applyErr string) error {
	if handled, err := s.markLokiConnectionApplied(ctx, edgeID, generation, probeID, applyErr); handled {
		return err
	}
	backend, assignment, err := s.backendForEdgeGeneration(ctx, edgeID, generation)
	if err != nil {
		return err
	}
	if assignment == nil || generation != backend.Generation || probeID == "" || probeID != assignment.ProbeID {
		return fmt.Errorf("%w: stale log backend generation", errs.ErrConflict)
	}
	now := time.Now().UTC()
	assignment.DesiredGeneration = generation
	assignment.AppliedGeneration = generation
	assignment.Status = model.AssignmentStatusApplied
	assignment.LastProbeAt = &now
	assignment.LastError = ""
	if strings.TrimSpace(applyErr) != "" {
		assignment.AppliedGeneration = 0
		assignment.Status = model.AssignmentStatusFailed
		assignment.LastError = truncate(strings.TrimSpace(applyErr), 1024)
		if err := s.repo.UpsertAssignment(ctx, assignment); err != nil {
			return err
		}
		return nil
	}
	if err := s.repo.UpsertAssignment(ctx, assignment); err != nil {
		return err
	}
	if err := s.verifyEdgeProbe(ctx, backend, assignment); err != nil {
		assignment.Status = model.AssignmentStatusFailed
		assignment.LastError = safeProbeError(err)
		if saveErr := s.repo.UpsertAssignment(ctx, assignment); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		s.log.WarnContext(ctx, "selected Elasticsearch Edge connection probe failed",
			slog.Uint64("backend_id", backend.ID), slog.Uint64("edge_id", edgeID), slog.Any("error", err))
		return nil
	}
	verifiedAt := time.Now().UTC()
	assignment.Status = model.AssignmentStatusVerified
	assignment.LastWriteSuccessAt = &verifiedAt
	assignment.LastError = ""
	return s.repo.UpsertAssignment(ctx, assignment)
}

func (s *Service) runtimeBackendForEdge(ctx context.Context, edgeID uint64) (*model.Backend, *model.BackendAssignment, error) {
	selected, err := s.repo.SelectedBackend(ctx)
	if errors.Is(err, errs.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if edgeID != 0 {
		assignment, assignmentErr := s.repo.GetAssignment(ctx, selected.ID, edgeID)
		if assignmentErr == nil && assignment.DesiredGeneration == selected.Generation && assignment.Status != model.AssignmentStatusFailed {
			return selected, assignment, nil
		}
		if assignmentErr != nil && !errors.Is(assignmentErr, errs.ErrNotFound) {
			return nil, nil, assignmentErr
		}
	}
	return selected, nil, nil
}

func (s *Service) backendForEdgeGeneration(ctx context.Context, edgeID, generation uint64) (*model.Backend, *model.BackendAssignment, error) {
	if edgeID == 0 || generation == 0 {
		return nil, nil, errs.ErrForbidden
	}
	selected, err := s.repo.SelectedBackend(ctx)
	if err != nil {
		return nil, nil, err
	}
	if selected.Generation != generation {
		return nil, nil, fmt.Errorf("%w: stale log backend generation", errs.ErrConflict)
	}
	assignment, assignmentErr := s.repo.GetAssignment(ctx, selected.ID, edgeID)
	if assignmentErr != nil && !errors.Is(assignmentErr, errs.ErrNotFound) {
		return nil, nil, assignmentErr
	}
	if assignmentErr == nil && assignment.DesiredGeneration == generation && assignment.Status != model.AssignmentStatusFailed {
		return selected, assignment, nil
	}
	return selected, nil, nil
}

func runtimeConfig(backend *model.Backend) (*RuntimeConfig, error) {
	if backend == nil {
		return nil, nil
	}
	endpoints, err := decodeEndpoints(backend.WriteEndpointsJSON)
	if err != nil {
		return nil, err
	}
	return &RuntimeConfig{
		BackendID: backend.ID, Backend: string(backend.Type), Generation: backend.Generation,
		WriteEndpoints: endpoints, Dataset: backend.Dataset, Namespace: backend.Namespace,
		CAPEM: backend.CAPEM, TLSInsecure: backend.TLSInsecure,
	}, nil
}

func (s *Service) verifyEdgeProbe(ctx context.Context, backend *model.Backend, assignment *model.BackendAssignment) error {
	s.mu.RLock()
	deviceResolver := s.devices
	s.mu.RUnlock()
	if deviceResolver == nil {
		return errors.New("host device resolver is not configured")
	}
	deviceID, err := deviceResolver.LookupHostDevice(ctx, assignment.EdgeID)
	if err != nil {
		return fmt.Errorf("resolve host device for Edge %d: %w", assignment.EdgeID, err)
	}
	if deviceID == 0 {
		return fmt.Errorf("resolve host device for Edge %d: empty device id", assignment.EdgeID)
	}
	queryKey, err := s.apiKey(ctx, backend.QueryCredentialRef)
	if err != nil {
		return err
	}
	client, err := s.newESClient(backend.QueryEndpoint, backend.IndexPattern, queryKey, backend)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	started := time.Now().UTC()
	if assignment.LastProbeAt != nil {
		started = assignment.LastProbeAt.Add(-time.Minute)
	}
	var lastErr error
	for {
		result, searchErr := client.Search(probeCtx, logquery.SearchRequest{
			Start: started, End: time.Now().UTC().Add(10 * time.Second), Limit: 10,
			Direction: logquery.SortBackward,
			Scope:     logquery.Scope{DeviceIDs: []uint64{deviceID}},
			Keywords:  logquery.Keywords{Include: []string{assignment.ProbeID}, Mode: logquery.MatchPhrase},
		})
		if searchErr == nil {
			for _, record := range result.Records {
				if strings.Contains(record.Message, assignment.ProbeID) {
					return nil
				}
			}
			lastErr = errors.New("probe log is not visible yet")
		} else {
			lastErr = searchErr
		}
		select {
		case <-probeCtx.Done():
			return fmt.Errorf("Elasticsearch Edge write probe %q not found: %w", assignment.ProbeID, lastErr)
		case <-time.After(750 * time.Millisecond):
		}
	}
}

func (s *Service) verifyLokiEdgeProbe(ctx context.Context, _ *model.Backend, assignment *model.BackendAssignment) error {
	s.mu.RLock()
	deviceResolver := s.devices
	loki := s.loki
	s.mu.RUnlock()
	if deviceResolver == nil {
		return errors.New("host device resolver is not configured")
	}
	if loki == nil {
		return errors.New("built-in Loki query path is not configured")
	}
	deviceID, err := deviceResolver.LookupHostDevice(ctx, assignment.EdgeID)
	if err != nil {
		return fmt.Errorf("resolve host device for Edge %d: %w", assignment.EdgeID, err)
	}
	if deviceID == 0 {
		return fmt.Errorf("resolve host device for Edge %d: empty device id", assignment.EdgeID)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	started := time.Now().UTC()
	if assignment.LastProbeAt != nil {
		started = assignment.LastProbeAt.Add(-time.Minute)
	}
	var lastErr error
	for {
		result, searchErr := loki.Search(probeCtx, logquery.SearchRequest{
			Start: started, End: time.Now().UTC().Add(10 * time.Second), Limit: 10,
			Direction: logquery.SortBackward,
			Scope:     logquery.Scope{DeviceIDs: []uint64{deviceID}},
			Keywords:  logquery.Keywords{Include: []string{assignment.ProbeID}, Mode: logquery.MatchPhrase},
		})
		if searchErr == nil {
			for _, record := range result.Records {
				if strings.Contains(record.Message, assignment.ProbeID) {
					return nil
				}
			}
			lastErr = errors.New("probe log is not visible yet")
		} else {
			lastErr = searchErr
		}
		select {
		case <-probeCtx.Done():
			return fmt.Errorf("built-in Loki Edge write probe %q not found: %w", assignment.ProbeID, lastErr)
		case <-time.After(750 * time.Millisecond):
		}
	}
}

func (s *Service) selectBackend(ctx context.Context, backend *model.Backend) error {
	if err := s.repo.SelectBackend(ctx, backend.ID, backend.DetectedVersion, time.Now().UTC()); err != nil {
		return err
	}
	s.invalidateCache()
	s.syncGrafanaElasticsearchAsync(ctx, backend)
	if err := s.notify(ctx); err != nil {
		s.log.WarnContext(ctx, "Elasticsearch backend is selected but Edge notification failed", slog.Any("error", err))
	}
	return nil
}

func (s *Service) elasticsearchDatasourceConfig(ctx context.Context, backend *model.Backend) (*pkggrafana.ElasticsearchDatasourceConfig, error) {
	if backend == nil {
		return nil, errors.New("selected Elasticsearch backend is nil")
	}
	queryKey, err := s.apiKey(ctx, backend.QueryCredentialRef)
	if err != nil {
		return nil, fmt.Errorf("resolve Elasticsearch query credential for Grafana: %w", err)
	}
	endpoints, err := decodeEndpoints(backend.WriteEndpointsJSON)
	if err != nil {
		return nil, err
	}
	return &pkggrafana.ElasticsearchDatasourceConfig{
		// QueryEndpoint is explicitly Manager-scoped and may be a loopback
		// address. Grafana runs in a separate process/container, so point it at
		// the first endpoint already verified for direct Edge access instead.
		URL:          endpoints[0],
		IndexPattern: backend.IndexPattern,
		APIKey:       queryKey,
		CAPEM:        backend.CAPEM,
		TLSInsecure:  backend.TLSInsecure,
	}, nil
}

func (s *Service) syncGrafanaElasticsearchAsync(ctx context.Context, backend *model.Backend) {
	s.mu.RLock()
	syncer := s.grafana
	s.mu.RUnlock()
	if syncer == nil || backend == nil {
		return
	}
	backendCopy := *backend
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("Grafana Elasticsearch datasource sync panicked", slog.Any("panic", recovered))
			}
		}()
		syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		config, err := s.elasticsearchDatasourceConfig(syncCtx, &backendCopy)
		if err == nil {
			err = syncer.SyncElasticsearch(syncCtx, *config)
		}
		if err != nil {
			s.log.WarnContext(syncCtx, "Grafana Elasticsearch datasource sync failed; log backend remains selected",
				slog.Uint64("backend_id", backendCopy.ID), slog.Any("error", err))
		}
	}()
}

func (s *Service) syncGrafanaLokiAsync(ctx context.Context) {
	s.mu.RLock()
	syncer := s.grafana
	s.mu.RUnlock()
	if syncer == nil {
		return
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("Grafana Loki datasource sync panicked", slog.Any("panic", recovered))
			}
		}()
		syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if err := syncer.SyncLoki(syncCtx); err != nil {
			s.log.WarnContext(syncCtx, "Grafana Loki datasource sync failed; Loki remains selected", slog.Any("error", err))
		}
	}()
}

func (s *Service) connectionEdges(ctx context.Context) ([]ConnectionEdge, error) {
	s.mu.RLock()
	inventory := s.inventory
	s.mu.RUnlock()
	if inventory == nil {
		return nil, errors.New("log connection Edge inventory is not configured")
	}
	edges, err := inventory.ListConnectionEdges(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Edges for log connection check: %w", err)
	}
	if len(edges) > 10_000 {
		return nil, fmt.Errorf("%w: log connection inventory exceeds 10000 Edges", errs.ErrInvalid)
	}
	byID := make(map[uint64]ConnectionEdge, len(edges))
	for _, edge := range edges {
		if edge.EdgeID == 0 {
			return nil, fmt.Errorf("%w: log connection inventory contains an empty Edge id", errs.ErrInvalid)
		}
		if existing, ok := byID[edge.EdgeID]; ok {
			existing.Online = existing.Online || edge.Online
			if existing.Name == "" {
				existing.Name = edge.Name
			}
			byID[edge.EdgeID] = existing
			continue
		}
		byID[edge.EdgeID] = edge
	}
	result := make([]ConnectionEdge, 0, len(byID))
	for _, edge := range byID {
		result = append(result, edge)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EdgeID < result[j].EdgeID })
	return result, nil
}

func (s *Service) validateProbeEdges(ctx context.Context, edgeIDs []uint64) error {
	if len(edgeIDs) == 0 {
		return nil
	}
	s.mu.RLock()
	resolver := s.devices
	s.mu.RUnlock()
	if resolver == nil {
		return errors.New("host device resolver is not configured")
	}
	for _, edgeID := range edgeIDs {
		deviceID, err := resolver.LookupHostDevice(ctx, edgeID)
		if err != nil {
			return fmt.Errorf("resolve host device for Edge %d: %w", edgeID, err)
		}
		if deviceID == 0 {
			return fmt.Errorf("resolve host device for Edge %d: empty device id", edgeID)
		}
	}
	return nil
}

func cloneBackendAssignment(in *model.BackendAssignment) *model.BackendAssignment {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneLokiConnectionCheckSession(in *lokiConnectionCheckSession) *lokiConnectionCheckSession {
	if in == nil {
		return nil
	}
	out := &lokiConnectionCheckSession{
		Generation:  in.Generation,
		Assignments: make(map[uint64]*model.BackendAssignment, len(in.Assignments)),
	}
	for edgeID, assignment := range in.Assignments {
		out.Assignments[edgeID] = cloneBackendAssignment(assignment)
	}
	return out
}

func newProbeID() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate Edge log probe id: %w", err)
	}
	return "ongrid-log-probe-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func newConnectionCheckGeneration() (uint64, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return 0, fmt.Errorf("generate Loki connection check generation: %w", err)
	}
	// Keep the value exactly representable by browser JavaScript numbers while
	// retaining enough entropy to distinguish concurrent/restarted checks.
	generation := binary.BigEndian.Uint64(raw) & ((1 << 52) - 1)
	if generation == 0 {
		generation = 1
	}
	return generation, nil
}

func (s *Service) probeBackend(ctx context.Context, backend *model.Backend) (string, error) {
	queryKey, err := s.apiKey(ctx, backend.QueryCredentialRef)
	if err != nil {
		return "", err
	}
	queryClient, err := s.newESClient(backend.QueryEndpoint, backend.IndexPattern, queryKey, backend)
	if err != nil {
		return "", err
	}
	if err := queryClient.RequirePrivileges(ctx, []string{"monitor"}, []string{"read", "view_index_metadata"}); err != nil {
		return "", fmt.Errorf("Elasticsearch query privileges: %w", err)
	}
	version, err := queryClient.Probe(ctx)
	if err != nil {
		return "", fmt.Errorf("Elasticsearch query probe: %w", err)
	}
	writeKey, err := s.apiKey(ctx, backend.WriteCredentialRef)
	if err != nil {
		return "", err
	}
	endpoints, err := decodeEndpoints(backend.WriteEndpointsJSON)
	if err != nil {
		return "", err
	}
	for i, endpoint := range endpoints {
		writeClient, clientErr := s.newESClient(endpoint, backend.IndexPattern, writeKey, backend)
		if clientErr != nil {
			return "", clientErr
		}
		if privilegeErr := writeClient.RequirePrivileges(ctx, nil, []string{"auto_configure", "create_doc"}); privilegeErr != nil {
			return "", fmt.Errorf("Elasticsearch write endpoint %d privileges: %w", i+1, privilegeErr)
		}

		// Use the Manager-only query credential for the version check. The
		// runtime write credential sent to Edge deliberately has no cluster
		// monitor permission.
		writeEndpointProbe, clientErr := s.newESClient(endpoint, backend.IndexPattern, queryKey, backend)
		if clientErr != nil {
			return "", clientErr
		}
		writeVersion, probeErr := writeEndpointProbe.Probe(ctx)
		if probeErr != nil {
			return "", fmt.Errorf("Elasticsearch write endpoint %d probe: %w", i+1, probeErr)
		}
		if writeVersion != version {
			return "", fmt.Errorf("Elasticsearch write endpoint %d reports version %s, query endpoint reports %s", i+1, writeVersion, version)
		}
	}
	return version, nil
}

func (s *Service) apiKey(ctx context.Context, ref string) (string, error) {
	if s.secrets == nil {
		return "", errors.New("log backend secret resolver is disabled")
	}
	fields, err := s.secrets.ResolveFields(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve Elasticsearch credential: %w", err)
	}
	key := strings.TrimSpace(fields["api_key"])
	if key == "" {
		return "", errors.New("Elasticsearch credential has no api_key")
	}
	return key, nil
}

func (s *Service) storeManagedAPIKey(ctx context.Context, role, apiKey string, generation uint64) (string, error) {
	store, ok := s.secrets.(ManagedSecretStore)
	if !ok {
		return "", errors.New("encrypted log backend credential storage is unavailable")
	}
	// A directly pasted key always gets a new isolated reference. The backend
	// row is switched only after both keys validate and are stored, so a failed
	// save cannot rotate the credential referenced by the current generation.
	name, err := newManagedLogsSecretName(role, generation)
	if err != nil {
		return "", fmt.Errorf("generate managed Elasticsearch credential name: %w", err)
	}
	description := fmt.Sprintf("Managed by Ongrid logs backend (%s key, generation %d)", role, generation)
	if err := store.CreateManaged(ctx, name, elasticsearchCredType, description, map[string]string{"api_key": apiKey}); err != nil {
		return "", fmt.Errorf("store managed Elasticsearch %s credential: %w", role, err)
	}
	return name, nil
}

func (s *Service) deleteManagedAPIKeys(ctx context.Context, refs []string) error {
	store, ok := s.secrets.(ManagedSecretStore)
	if !ok {
		return errors.New("encrypted log backend credential storage is unavailable")
	}
	var cleanupErr error
	for i := len(refs) - 1; i >= 0; i-- {
		if err := store.DeleteManaged(ctx, refs[i]); err != nil && !errors.Is(err, errs.ErrNotFound) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete managed credential: %w", err))
		}
	}
	return cleanupErr
}

func (s *Service) cleanupSupersededManagedAPIKeys(ctx context.Context, previous, current *model.Backend) {
	refs := supersededManagedAPIKeyRefs(previous, current)
	if len(refs) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedSecretCleanupTTL)
	defer cancel()
	if err := s.deleteManagedAPIKeys(cleanupCtx, refs); err != nil {
		s.log.WarnContext(cleanupCtx, "failed to delete superseded managed Elasticsearch credentials",
			slog.Int("credential_count", len(refs)), slog.Any("error", err))
	}
}

func supersededManagedAPIKeyRefs(previous, current *model.Backend) []string {
	if previous == nil || current == nil {
		return nil
	}
	retained := map[string]struct{}{
		current.WriteCredentialRef: {},
		current.QueryCredentialRef: {},
	}
	seen := make(map[string]struct{}, 2)
	refs := make([]string, 0, 2)
	for _, ref := range []string{previous.WriteCredentialRef, previous.QueryCredentialRef} {
		if !strings.HasPrefix(ref, managedLogsSecretPrefix) {
			continue
		}
		if _, ok := retained[ref]; ok {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func newManagedLogsSecretName(role string, generation uint64) (string, error) {
	if role != "write" && role != "query" {
		return "", fmt.Errorf("unsupported Elasticsearch credential role %q", role)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s-g%d-%s", managedLogsSecretPrefix, role, generation, hex.EncodeToString(random)), nil
}

func (s *Service) newESClient(endpoint, pattern, apiKey string, backend *model.Backend) (*logquery.ElasticsearchClient, error) {
	httpClient, err := backendHTTPClient(backend)
	if err != nil {
		return nil, err
	}
	return logquery.NewElasticsearchClient(logquery.ElasticsearchConfig{
		Endpoint:          endpoint,
		IndexPattern:      pattern,
		APIKey:            apiKey,
		AllowInsecureHTTP: backend.TLSInsecure,
	}, httpClient, nil)
}

func (s *Service) view(ctx context.Context, backend *model.Backend) (*BackendView, error) {
	endpoints, err := decodeEndpoints(backend.WriteEndpointsJSON)
	if err != nil {
		return nil, err
	}
	currentBackend := "loki"
	var currentBackendID uint64
	selected, selectedErr := s.repo.SelectedBackend(ctx)
	if selectedErr == nil && selected != nil {
		currentBackend = string(selected.Type)
		currentBackendID = selected.ID
	} else if selectedErr != nil && !errors.Is(selectedErr, errs.ErrNotFound) {
		return nil, selectedErr
	}
	return &BackendView{
		ID: backend.ID, Name: backend.Name, Type: backend.Type, CurrentBackend: currentBackend, CurrentBackendID: currentBackendID, Status: backend.Status,
		Generation: backend.Generation, WriteEndpoints: endpoints, QueryEndpoint: backend.QueryEndpoint,
		Dataset: backend.Dataset, Namespace: backend.Namespace, IndexPattern: backend.IndexPattern,
		WriteCredentialRef: backend.WriteCredentialRef, QueryCredentialRef: backend.QueryCredentialRef,
		HasCustomCA: strings.TrimSpace(backend.CAPEM) != "", KibanaURL: backend.KibanaURL,
		TLSInsecure:     backend.TLSInsecure,
		DetectedVersion: backend.DetectedVersion,
		LastTestAt:      backend.LastTestAt,
		CreatedAt:       backend.CreatedAt, UpdatedAt: backend.UpdatedAt,
	}, nil
}

func (s *Service) notify(ctx context.Context) error {
	s.mu.RLock()
	notifier := s.notifier
	s.mu.RUnlock()
	if notifier == nil {
		return nil
	}
	return notifier.NotifyLogsBackendChanged(ctx)
}

func (s *Service) checkSelectGuard(ctx context.Context) error {
	s.mu.RLock()
	guard := s.selectGuard
	s.mu.RUnlock()
	if guard == nil {
		return nil
	}
	if err := guard(ctx); err != nil {
		return fmt.Errorf("%w: %v", errs.ErrConflict, err)
	}
	return nil
}

func (s *Service) invalidateCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheKey = ""
	s.cachedES = nil
}

func normalizeSaveInput(input SaveInput) (SaveInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		input.Name = DefaultBackendName
	}
	if len(input.Name) > 128 {
		return SaveInput{}, fmt.Errorf("%w: backend name too long", errs.ErrInvalid)
	}
	if len(input.WriteEndpoints) == 0 || len(input.WriteEndpoints) > maxBackendEndpoints {
		return SaveInput{}, fmt.Errorf("%w: write_endpoints must contain 1..%d entries", errs.ErrInvalid, maxBackendEndpoints)
	}
	seen := make(map[string]struct{}, len(input.WriteEndpoints))
	endpoints := make([]string, 0, len(input.WriteEndpoints))
	for _, raw := range input.WriteEndpoints {
		endpoint, err := normalizeHTTPSURL(raw, input.TLSInsecure, false)
		if err != nil {
			return SaveInput{}, fmt.Errorf("%w: invalid write endpoint", errs.ErrInvalid)
		}
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		endpoints = append(endpoints, endpoint)
	}
	input.WriteEndpoints = endpoints
	if strings.TrimSpace(input.QueryEndpoint) == "" {
		input.QueryEndpoint = endpoints[0]
	}
	queryEndpoint, err := normalizeHTTPSURL(input.QueryEndpoint, input.TLSInsecure, false)
	if err != nil {
		return SaveInput{}, fmt.Errorf("%w: invalid query endpoint", errs.ErrInvalid)
	}
	input.QueryEndpoint = queryEndpoint
	input.Dataset = strings.ToLower(strings.TrimSpace(input.Dataset))
	if input.Dataset == "" {
		input.Dataset = "ongrid.generic"
	}
	if !datasetRE.MatchString(input.Dataset) {
		return SaveInput{}, fmt.Errorf("%w: dataset must match ongrid.<safe-slug>", errs.ErrInvalid)
	}
	input.Namespace = strings.ToLower(strings.TrimSpace(input.Namespace))
	if input.Namespace == "" {
		input.Namespace = "default"
	}
	if !namespaceRE.MatchString(input.Namespace) {
		return SaveInput{}, fmt.Errorf("%w: invalid data stream namespace", errs.ErrInvalid)
	}
	input.WriteCredentialRef = strings.TrimSpace(input.WriteCredentialRef)
	input.QueryCredentialRef = strings.TrimSpace(input.QueryCredentialRef)
	if len(input.WriteCredentialRef) > 128 || len(input.QueryCredentialRef) > 128 {
		return SaveInput{}, fmt.Errorf("%w: credential ref too long", errs.ErrInvalid)
	}
	input.WriteAPIKey, err = normalizeDirectAPIKey(input.WriteAPIKey)
	if err != nil {
		return SaveInput{}, fmt.Errorf("%w: invalid write_api_key", errs.ErrInvalid)
	}
	input.QueryAPIKey, err = normalizeDirectAPIKey(input.QueryAPIKey)
	if err != nil {
		return SaveInput{}, fmt.Errorf("%w: invalid query_api_key", errs.ErrInvalid)
	}
	if input.ReuseWriteAPIKey && input.QueryAPIKey != "" {
		return SaveInput{}, fmt.Errorf("%w: query_api_key must be empty when reuse_write_api_key is enabled", errs.ErrInvalid)
	}
	input.CAPEM = strings.TrimSpace(input.CAPEM)
	if len(input.CAPEM) > maxCAPEMBytes {
		return SaveInput{}, fmt.Errorf("%w: CA bundle too large", errs.ErrInvalid)
	}
	if input.CAPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(input.CAPEM)) {
			return SaveInput{}, fmt.Errorf("%w: invalid CA PEM", errs.ErrInvalid)
		}
	}
	input.KibanaURL = strings.TrimSpace(input.KibanaURL)
	if input.KibanaURL != "" {
		input.KibanaURL, err = normalizeHTTPSURL(input.KibanaURL, false, true)
		if err != nil {
			return SaveInput{}, fmt.Errorf("%w: invalid Kibana URL", errs.ErrInvalid)
		}
	}
	return input, nil
}

func normalizeDirectAPIKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if len(key) >= len("ApiKey ") && strings.EqualFold(key[:len("ApiKey ")], "ApiKey ") {
		key = strings.TrimSpace(key[len("ApiKey "):])
	}
	if len(key) > maxAPIKeyBytes {
		return "", errors.New("API key too large")
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return "", errors.New("API key contains whitespace")
	}
	return key, nil
}

func normalizeHTTPSURL(raw string, allowHTTP, allowPath bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid URL")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return "", errors.New("HTTPS required")
	}
	if !allowPath && parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("path not allowed")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func backendHTTPClient(backend *model.Backend) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if strings.TrimSpace(backend.CAPEM) != "" && !pool.AppendCertsFromPEM([]byte(backend.CAPEM)) {
		return nil, errors.New("invalid Elasticsearch CA PEM")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		RootCAs:            pool,
		InsecureSkipVerify: backend.TLSInsecure, //nolint:gosec // explicit admin-only compatibility switch
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("Elasticsearch redirects are disabled")
		},
	}, nil
}

func decodeEndpoints(raw string) ([]string, error) {
	var endpoints []string
	if err := json.Unmarshal([]byte(raw), &endpoints); err != nil {
		return nil, fmt.Errorf("decode Elasticsearch endpoints: %w", err)
	}
	if len(endpoints) == 0 {
		return nil, errors.New("Elasticsearch endpoints are empty")
	}
	return endpoints, nil
}

func indexPattern(namespace string) string { return "logs-ongrid.*.otel-" + namespace }

func safeProbeError(err error) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), 1024)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
