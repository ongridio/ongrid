package topology

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	topologymodel "github.com/ongridio/ongrid/internal/cmdb/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The saved topology views: CRUD, ordering, and the graph a view draws.
//
// A VIEW STORES A WALK, NOT A GRAPH. The graph is assembled from the current
// relations whenever it is opened or previewed, so an edge added after the view
// was saved appears in it — which is the difference between a saved view and a
// screenshot.

// Topology errors.
var (
	ErrGroupNotFound = fmt.Errorf("%w: topology group does not exist", errs.ErrNotFound)
	ErrViewNotFound  = fmt.Errorf("%w: topology view does not exist", errs.ErrNotFound)
	ErrNameTaken     = fmt.Errorf("%w: a topology view or group has that name", errs.ErrConflict)
	ErrGroupNotEmpty = fmt.Errorf("%w: the group still has views", errs.ErrConflict)
)

// Repo is the topology store.
type Repo interface {
	ListGroups(ctx context.Context) ([]topologymodel.ViewGroup, error)
	LoadGroup(ctx context.Context, id model.ID) (*topologymodel.ViewGroup, error)
	FindGroupByName(ctx context.Context, name string) (*topologymodel.ViewGroup, error)
	CreateGroup(ctx context.Context, name string) (*topologymodel.ViewGroup, error)
	UpdateGroup(ctx context.Context, id model.ID, name string) (*topologymodel.ViewGroup, error)
	DeleteGroup(ctx context.Context, id model.ID) error
	ReorderGroups(ctx context.Context, ids []model.ID) error

	ListViews(ctx context.Context) ([]topologymodel.View, error)
	LoadView(ctx context.Context, id model.ID) (*topologymodel.View, error)
	FindViewByName(ctx context.Context, name string) (*topologymodel.View, error)
	CreateView(ctx context.Context, in ViewWrite) (*topologymodel.View, error)
	UpdateView(ctx context.Context, id model.ID, in ViewWrite) (*topologymodel.View, error)
	DeleteView(ctx context.Context, id model.ID) error
	ReorderViews(ctx context.Context, ids []model.ID) error
	AssignViewsToGroup(ctx context.Context, groupID model.ID, viewIDs []model.ID) error
}

// ModelRelations answers the path picker's question: which models a model may
// be walked to, in each direction.
type ModelRelations interface {
	RelatedTypes(ctx context.Context, typeRef string) (children, parents []RelatedModel, err error)
}

// RelatedModel is one model at the other end of a declared relation.
type RelatedModel struct {
	TypeID         model.ID `json:"type_id"`
	Name           string   `json:"name"`
	Alias          string   `json:"alias"`
	RelationTypeID model.ID `json:"relation_type_id"`
	Constraint     string   `json:"constraint"`
}

// Usecase serves the saved views.
type Usecase struct {
	repo   Repo
	graph  GraphQueries
	models ModelRelations
}

// NewUsecase builds the use case.
func NewUsecase(repo Repo) *Usecase { return &Usecase{repo: repo} }

// WithGraph supplies the instance-side graph queries.
func (u *Usecase) WithGraph(q GraphQueries) *Usecase {
	u.graph = q
	return u
}

// WithModelRelations supplies the model-relation graph.
func (u *Usecase) WithModelRelations(m ModelRelations) *Usecase {
	u.models = m
	return u
}

// --- views ---

// Group is a sidebar folder with its views.
type Group struct {
	ID    model.ID `json:"id"`
	Name  string   `json:"name"`
	Order int      `json:"order"`
	Views []View   `json:"views"`
}

// View is one saved topology view.
type View struct {
	ID      model.ID  `json:"id"`
	Name    string    `json:"name"`
	GroupID *model.ID `json:"group_id,omitempty"`

	Category string `json:"category,omitempty"`

	CentralNodeType      model.ID `json:"central_node_type"`
	CentralNodeInstances string   `json:"central_node_instances"`

	Path   map[string][]model.ID `json:"path"`
	Order  int                   `json:"order"`
	Option map[string]any        `json:"option,omitempty"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ViewInput is the write body of a view.
type ViewInput struct {
	Name     string    `json:"name"`
	GroupID  *model.ID `json:"group_id"`
	Category string    `json:"category"`

	CentralNodeType      model.ID `json:"central_node_type"`
	CentralNodeInstances string   `json:"central_node_instances"`

	Path   map[string][]model.ID `json:"path"`
	Option map[string]any        `json:"option"`
}

// GroupInput is the write body of a group.
type GroupInput struct {
	Name     *string
	ViewIDs  []model.ID
	SetViews bool
}

// ViewWrite is what the store writes.
type ViewWrite struct {
	Name                 string
	GroupID              *model.ID
	Category             string
	CentralNodeType      model.ID
	CentralNodeInstances string
	Path                 map[string][]model.ID
	Option               map[string]any
}

// ListGroups returns the folders, each with its views.
func (u *Usecase) ListGroups(ctx context.Context) ([]Group, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	groups, err := u.repo.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	views, err := u.repo.ListViews(ctx)
	if err != nil {
		return nil, err
	}
	byGroup := map[model.ID][]View{}
	for _, row := range views {
		if row.GroupID == nil {
			continue
		}
		byGroup[*row.GroupID] = append(byGroup[*row.GroupID], renderView(row))
	}
	out := make([]Group, 0, len(groups))
	for _, row := range groups {
		got := byGroup[row.ID]
		if got == nil {
			got = []View{}
		}
		out = append(out, Group{ID: row.ID, Name: row.Name, Order: row.Order, Views: got})
	}
	return out, nil
}

// ListViews returns every saved view, including the ungrouped ones.
func (u *Usecase) ListViews(ctx context.Context) ([]View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	rows, err := u.repo.ListViews(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rows))
	for _, row := range rows {
		out = append(out, renderView(row))
	}
	return out, nil
}

// GetView returns one view.
func (u *Usecase) GetView(ctx context.Context, id model.ID) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := u.repo.LoadView(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %d", ErrViewNotFound, id)
	}
	got := renderView(*row)
	return &got, nil
}

// CreateGroup adds a folder.
func (u *Usecase) CreateGroup(ctx context.Context, name string) (*Group, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, err := validateGroupName(name)
	if err != nil {
		return nil, err
	}
	existing, err := u.repo.FindGroupByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrNameTaken, name)
	}
	row, err := u.repo.CreateGroup(ctx, name)
	if err != nil {
		return nil, err
	}
	return &Group{ID: row.ID, Name: row.Name, Order: row.Order, Views: []View{}}, nil
}

// UpdateGroup renames a folder and, when asked, sets which views it holds.
func (u *Usecase) UpdateGroup(ctx context.Context, id model.ID, in GroupInput) (*Group, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	existing, err := u.repo.LoadGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("%w: %d", ErrGroupNotFound, id)
	}
	if in.Name != nil {
		name, err := validateGroupName(*in.Name)
		if err != nil {
			return nil, err
		}
		if name != existing.Name {
			other, err := u.repo.FindGroupByName(ctx, name)
			if err != nil {
				return nil, err
			}
			if other != nil && other.ID != id {
				return nil, fmt.Errorf("%w: %s", ErrNameTaken, name)
			}
			if _, err := u.repo.UpdateGroup(ctx, id, name); err != nil {
				return nil, err
			}
		}
	}
	if in.SetViews {
		if err := u.repo.AssignViewsToGroup(ctx, id, in.ViewIDs); err != nil {
			return nil, err
		}
	}
	return u.getGroup(ctx, id)
}

func (u *Usecase) getGroup(ctx context.Context, id model.ID) (*Group, error) {
	groups, err := u.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if group.ID == id {
			return &group, nil
		}
	}
	return nil, fmt.Errorf("%w: %d", ErrGroupNotFound, id)
}

// DeleteGroup removes an empty folder.
func (u *Usecase) DeleteGroup(ctx context.Context, id model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	existing, err := u.repo.LoadGroup(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("%w: %d", ErrGroupNotFound, id)
	}
	return u.repo.DeleteGroup(ctx, id)
}

// ReorderGroups rewrites the folder order.
func (u *Usecase) ReorderGroups(ctx context.Context, ids []model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if len(ids) == 0 {
		return fmt.Errorf("%w: group_ids", errs.ErrInvalid)
	}
	return u.repo.ReorderGroups(ctx, ids)
}

// CreateView adds a saved view.
func (u *Usecase) CreateView(ctx context.Context, in ViewInput) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, err := validateView(in)
	if err != nil {
		return nil, err
	}
	existing, err := u.repo.FindViewByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrNameTaken, name)
	}
	row, err := u.repo.CreateView(ctx, ViewWrite{
		Name: name, GroupID: in.GroupID, Category: in.Category,
		CentralNodeType: in.CentralNodeType, CentralNodeInstances: in.CentralNodeInstances,
		Path: in.Path, Option: in.Option,
	})
	if err != nil {
		return nil, err
	}
	got := renderView(*row)
	return &got, nil
}

// UpdateView replaces a view's definition.
func (u *Usecase) UpdateView(ctx context.Context, id model.ID, in ViewInput) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	existing, err := u.repo.LoadView(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("%w: %d", ErrViewNotFound, id)
	}
	name, err := validateView(in)
	if err != nil {
		return nil, err
	}
	if name != existing.Name {
		other, err := u.repo.FindViewByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if other != nil && other.ID != id {
			return nil, fmt.Errorf("%w: %s", ErrNameTaken, name)
		}
	}
	row, err := u.repo.UpdateView(ctx, id, ViewWrite{
		Name: name, GroupID: in.GroupID, Category: in.Category,
		CentralNodeType: in.CentralNodeType, CentralNodeInstances: in.CentralNodeInstances,
		Path: in.Path, Option: in.Option,
	})
	if err != nil {
		return nil, err
	}
	got := renderView(*row)
	return &got, nil
}

// DeleteView removes a saved view.
func (u *Usecase) DeleteView(ctx context.Context, id model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	existing, err := u.repo.LoadView(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("%w: %d", ErrViewNotFound, id)
	}
	return u.repo.DeleteView(ctx, id)
}

// ReorderViews rewrites the view order within a folder.
func (u *Usecase) ReorderViews(ctx context.Context, ids []model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if len(ids) == 0 {
		return fmt.Errorf("%w: view_ids", errs.ErrInvalid)
	}
	return u.repo.ReorderViews(ctx, ids)
}

// Graph assembles a saved view.
func (u *Usecase) Graph(ctx context.Context, id model.ID) (*Graph, error) {
	view, err := u.GetView(ctx, id)
	if err != nil {
		return nil, err
	}
	return BuildGraph(ctx, u.graph, GraphInput{
		CentralNodeType: view.CentralNodeType, CentralNodeInstances: view.CentralNodeInstances,
		Path: map[string][]model.ID(view.Path),
	})
}

// Preview assembles a view that has not been saved.
func (u *Usecase) Preview(ctx context.Context, in ViewInput) (*Graph, error) {
	if _, err := validateView(in); err != nil {
		return nil, err
	}
	return BuildGraph(ctx, u.graph, GraphInput{
		CentralNodeType: in.CentralNodeType, CentralNodeInstances: in.CentralNodeInstances,
		Path: in.Path,
	})
}

// TypeRelations returns the models a model may be walked to.
func (u *Usecase) TypeRelations(ctx context.Context, ref string) (children, parents []RelatedModel, err error) {
	if u.models == nil {
		return nil, nil, errs.ErrNotWiredYet
	}
	return u.models.RelatedTypes(ctx, ref)
}

// The stored columns are varchar(64) for names and varchar(32) for a view's
// category. Count RUNES, not bytes: MySQL counts characters, and a Chinese
// name of 64 characters must fit.
const (
	maxNameRunes     = 64
	maxCategoryRunes = 32
)

func validateGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%w: name", errs.ErrInvalid)
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", fmt.Errorf("%w: name is longer than %d characters", errs.ErrInvalid, maxNameRunes)
	}
	return name, nil
}

func validateView(in ViewInput) (string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", fmt.Errorf("%w: name", errs.ErrInvalid)
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", fmt.Errorf("%w: name is longer than %d characters", errs.ErrInvalid, maxNameRunes)
	}
	if utf8.RuneCountInString(in.Category) > maxCategoryRunes {
		return "", fmt.Errorf("%w: category is longer than %d characters", errs.ErrInvalid, maxCategoryRunes)
	}
	if in.CentralNodeType <= 0 {
		return "", fmt.Errorf("%w: central_node_type", errs.ErrInvalid)
	}
	if _, err := levelsOf(in.Path); err != nil {
		return "", err
	}
	return name, nil
}

func renderView(row topologymodel.View) View {
	got := View{
		ID: row.ID, Name: row.Name, GroupID: row.GroupID,
		Category: row.Category, CentralNodeType: derefID(row.CentralNodeType),
		CentralNodeInstances: row.CentralNodeInstances,
		Path:                 map[string][]model.ID(row.Path), Order: row.Order, Option: row.Option,
		CreatedAt: formatTime(row.CreatedAt), UpdatedAt: formatTime(row.UpdatedAt),
	}
	if got.Path == nil {
		got.Path = map[string][]model.ID{}
	}
	return got
}

func derefID(id *model.ID) model.ID {
	if id == nil {
		return 0
	}
	return *id
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
