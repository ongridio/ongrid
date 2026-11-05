package model

// Domain constants ported from cantor's api/lib/cmdb/const.py.
//
// They are string codes because that is what cantor persists in the enum
// columns; changing them would break adoption of an existing cantor database.
//
// None of these are enforced with a database CHECK constraint, deliberately.
// Ongrid's own migrations had to drop and re-create CHECK constraints by hand
// (see internal/manager/data/device/store/migrate.go::refreshDeviceRolesConstraint)
// because AutoMigrate never rewrites an existing constraint, and SQLite
// cannot alter one without rebuilding the table. Validation therefore lives in
// the biz layer where it can produce a useful error instead of a driver error.

// CIStatus is cantor's CIStatusEnum: a CI is either pending review or
// validated.
type CIStatus string

const (
	CIStatusReview   CIStatus = "0"
	CIStatusValidate CIStatus = "1"
)

// Valid reports whether s is a known CI status.
func (s CIStatus) Valid() bool { return s == CIStatusReview || s == CIStatusValidate }

// OperateType is cantor's OperateType, recorded on history rows.
type OperateType string

const (
	OperateAdd    OperateType = "0"
	OperateDelete OperateType = "1"
	OperateUpdate OperateType = "2"
)

// Valid reports whether o is a known operation type.
func (o OperateType) Valid() bool {
	return o == OperateAdd || o == OperateDelete || o == OperateUpdate
}

// RelationSource is cantor's RelationSourceEnum: how a relation came to exist.
type RelationSource string

const (
	// RelationFromAttributeValues marks a relation derived from a
	// reference-valued attribute rather than created by hand.
	RelationFromAttributeValues RelationSource = "0"
	// RelationFromAutoDiscovery marks a relation created when a discovery
	// candidate was accepted into the CMDB.
	RelationFromAutoDiscovery RelationSource = "1"
)

// Constraint is cantor's ConstraintEnum: the cardinality rule on a
// model-level relation (c_ci_type_relations.constraint).
type Constraint string

const (
	ConstraintOne2Many  Constraint = "0"
	ConstraintOne2One   Constraint = "1"
	ConstraintMany2Many Constraint = "2"
)

// Valid reports whether c is a known cardinality rule.
func (c Constraint) Valid() bool {
	return c == ConstraintOne2Many || c == ConstraintOne2One || c == ConstraintMany2Many
}

// CITypeOperateType is cantor's CITypeOperateType, recorded on
// c_ci_type_histories to describe model-configuration changes.
type CITypeOperateType string

const (
	CITypeOpAdd                    CITypeOperateType = "0"
	CITypeOpUpdate                 CITypeOperateType = "1"
	CITypeOpDelete                 CITypeOperateType = "2"
	CITypeOpAddAttribute           CITypeOperateType = "3"
	CITypeOpUpdateAttribute        CITypeOperateType = "4"
	CITypeOpDeleteAttribute        CITypeOperateType = "5"
	CITypeOpAddTrigger             CITypeOperateType = "6"
	CITypeOpUpdateTrigger          CITypeOperateType = "7"
	CITypeOpDeleteTrigger          CITypeOperateType = "8"
	CITypeOpAddUniqueConstraint    CITypeOperateType = "9"
	CITypeOpUpdateUniqueConstraint CITypeOperateType = "10"
	CITypeOpDeleteUniqueConstraint CITypeOperateType = "11"
	CITypeOpAddRelation            CITypeOperateType = "12"
	CITypeOpDeleteRelation         CITypeOperateType = "13"
)

// BuiltinModel is cantor's BuiltinModelEnum: the CI models the product
// creates itself at bootstrap.
//
// These matter structurally: IPAM and DCIM are NOT separate table families in
// cantor. They are ordinary CI models riding the same EAV engine, so the
// attribute/value tables are shared foundation for the model-management,
// resource-management and auto-discovery areas alike.
type BuiltinModel string

const (
	BuiltinIPAMSubnet     BuiltinModel = "ipam_subnet"
	BuiltinIPAMAddress    BuiltinModel = "ipam_address"
	BuiltinIPAMScope      BuiltinModel = "ipam_scope"
	BuiltinDCIMRegion     BuiltinModel = "dcim_region"
	BuiltinDCIMIDC        BuiltinModel = "dcim_idc"
	BuiltinDCIMServerRoom BuiltinModel = "dcim_server_room"
	BuiltinDCIMRack       BuiltinModel = "dcim_rack"
)
