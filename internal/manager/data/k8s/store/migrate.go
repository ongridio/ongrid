package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate registers the Kubernetes onboarding tables.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.Cluster{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.Cluster{}, "idx_k8s_clusters_uid_deleted"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(
		&model.Cluster{},
		&model.Node{},
		&model.Workload{},
		&model.Pod{},
		&model.Event{},
		&model.Installation{},
		&model.TelemetryCredential{},
	); err != nil {
		return err
	}
	return dbx.BackfillDeleteMarker(db, model.Cluster{}.TableName())
}
