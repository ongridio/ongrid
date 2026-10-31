package ci

import cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"

// OperateType values recorded on a data-centre operation.
//
// THEY ARE STRINGS BECAUSE THE COLUMN IS, and because the console sends them
// back as strings in the filter. cantor's enum was a `BaseEnum` over exactly
// these three, so the set is closed.
const (
	// OperateAddDevice is a device mounted in a rack.
	OperateAddDevice = "0"

	// OperateRemoveDevice is a device unmounted.
	OperateRemoveDevice = "1"

	// OperateMoveDevice is a device repositioned, or migrated to another rack.
	OperateMoveDevice = "2"
)

// DCIMOperationHistory is one recorded rack operation (c_dcim_operation_histories).
//
// APPEND-ONLY, which is why it embeds AppendOnlyBase rather than Base: an
// operation record that could be soft-deleted is not a record of an operation.
// cantor declared it with its `Model2` base class for the same reason.
//
// IT CARRIES NO ATTRIBUTE VALUES. The record says who moved which device
// between which racks; what the device is called is read from the device, so a
// rename does not rewrite history and a deleted device still leaves a trace.
type DCIMOperationHistory struct {
	cmdbmodel.AppendOnlyBase

	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_dcim_operation_histories_uid"`

	// RackID is the rack the operation was performed ON. For a migration the
	// two ends get one row each, which is what makes the history readable from
	// either rack's page.
	RackID *cmdbmodel.ID `gorm:"column:rack_id;index:idx_c_dcim_operation_histories_rack_id"`

	CIID *cmdbmodel.ID `gorm:"column:ci_id;index:idx_c_dcim_operation_histories_ci_id"`

	OperateType *string `gorm:"column:operate_type;size:1"`
}

func (DCIMOperationHistory) TableName() string { return "c_dcim_operation_histories" }
