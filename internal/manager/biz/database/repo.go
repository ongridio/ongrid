// Package database is the business layer for database instance management.
//
// Phase 1: asset CRUD. Later phases add topology, slow query analysis,
// SQL audit, and schema migration orchestration.
package database

import (
	"context"

	model "github.com/ongridio/ongrid/internal/manager/model/database"
)

// ListFilter is the parameter object for Repo.List / Usecase.List.
// All fields are optional; empty/nil means "no filter for that dimension".
type ListFilter struct {
	DBType string
	Status string
	Name   string // substring match (LIKE %name%)
	EdgeID *uint64
	Limit  int
	Offset int
}

// Repo is the persistence contract for database instances. Implemented
// in internal/manager/data/database/store with a GORM backend.
type Repo interface {
	Create(ctx context.Context, inst *model.DatabaseInstance) error
	GetByID(ctx context.Context, id uint64) (*model.DatabaseInstance, error)
	List(ctx context.Context, f ListFilter) ([]*model.DatabaseInstance, error)
	Update(ctx context.Context, inst *model.DatabaseInstance) error
	UpdateStatus(ctx context.Context, id uint64, status string) error
	UpdateVersion(ctx context.Context, id uint64, version string) error
	Delete(ctx context.Context, id uint64) error // soft delete
	Count(ctx context.Context) (int64, error)
}
