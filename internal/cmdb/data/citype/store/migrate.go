// Package store implements CMDB persistence for model definitions: CI types,
// attributes, their bindings and groupings, model-level relations, and the
// relation-type dictionary.
package store

import (
	"errors"

	"gorm.io/gorm"

	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

var errNilDB = errors.New("cmdb citype store: nil db")

// Migrate registers the model-definition schema.
//
// It must run BEFORE the CI-side migrator in a fresh database only by
// convention, not by constraint: CMDB declares no database-level foreign keys,
// so there is no ordering dependency for AutoMigrate to satisfy.
//
// cantor bootstraps built-in rows here too (the relation-type dictionary, the
// seven IPAM/DCIM CI models, the built-in attribute set). Seeding is
// deliberately NOT part of Migrate: seeding writes business rows, and doing it
// inside a boot-time migration makes a partially seeded database
// indistinguishable from an edited one. It belongs in an explicit command.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return errNilDB
	}
	return db.AutoMigrate(
		&citypemodel.RelationType{},
		&citypemodel.CITypeGroup{},
		&citypemodel.CITypeGroupItem{},
		&citypemodel.CIType{},
		&citypemodel.CITypeInheritance{},
		&citypemodel.CITypeRelation{},
		&citypemodel.Attribute{},
		&citypemodel.CITypeAttribute{},
		&citypemodel.CITypeAttributeGroup{},
		&citypemodel.CITypeAttributeGroupItem{},
		&citypemodel.CITypeUniqueConstraint{},
		&citypemodel.CITypeTrigger{},
		// Per-user preferences: what one person has subscribed to and how they
		// have arranged it. They carry a uid rather than a tenant, matching the
		// rest of CMDB.
		&citypemodel.PreferenceShowAttributes{},
		&citypemodel.PreferenceCITypeOrder{},
		&citypemodel.PreferenceTreeView{},
		&citypemodel.PreferenceAutoSubscriptionConfig{},
		// The service-tree definitions and saved searches. The definitions are
		// also READ by the resource-management service tree, which is why they
		// live in this package's schema rather than the resource one's.
		&citypemodel.PreferenceRelationView{},
		&citypemodel.PreferenceSearchOption{},
	)
}
