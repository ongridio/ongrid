package ci

import (
	"time"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// IPAM operation types, as cantor numbered them.
//
// THE VALUES ARE STRINGS BECAUSE THE COLUMN IS, and because the console sends
// them back as strings in the history filter.
const (
	OperateAddScope      = "0"
	OperateUpdateScope   = "1"
	OperateDeleteScope   = "2"
	OperateAddSubnet     = "3"
	OperateUpdateSubnet  = "4"
	OperateDeleteSubnet  = "5"
	OperateAssignAddress = "6"
	OperateRevokeAddress = "7"
	OperateMoveSubnet    = "8"
)

// IPAMOperationTypes is the closed set, for validating a caller's filter.
var IPAMOperationTypes = []string{
	OperateAddScope, OperateUpdateScope, OperateDeleteScope,
	OperateAddSubnet, OperateUpdateSubnet, OperateDeleteSubnet,
	OperateAssignAddress, OperateRevokeAddress, OperateMoveSubnet,
}

// IPAMSubnetScan is one subnet's scan configuration (c_ipam_subnet_scans).
//
// IT IS A SEPARATE ROW RATHER THAN SUBNET ATTRIBUTES, and cantor's shape is
// worth keeping: the scan is executed by an AGENT on the other side of a
// tunnel, so it carries the agent's id and a cron expression — neither of which
// is a property of an address range. A subnet's range can exist without any
// scan at all.
//
// The soft-delete base rather than the append-only one: a rule is configuration,
// and configuration is deleted.
type IPAMSubnetScan struct {
	cmdbmodel.Base

	// CIID is the subnet the rule belongs to. Indexed because the whole point
	// of the table is asking "does this subnet have a rule".
	CIID cmdbmodel.ID `gorm:"column:ci_id;not null;index:idx_c_ipam_subnet_scans_ci_id"`

	ScanEnabled bool `gorm:"column:scan_enabled;not null;default:true"`

	// RuleUpdatedAt is when the rule last changed. The scan-rules endpoint hands
	// it to an agent so the agent can ask "has anything changed since" and get
	// an empty list rather than the whole configuration.
	RuleUpdatedAt *time.Time `gorm:"column:rule_updated_at"`

	LastScanTime *time.Time `gorm:"column:last_scan_time"`

	// AgentID is the executor. It is a STRING because cantor declared it one —
	// it is the edge agent's own identifier, not a row in this database.
	AgentID *string `gorm:"column:agent_id;size:64;index:idx_c_ipam_subnet_scans_agent_id"`

	Cron *string `gorm:"column:cron;size:128"`
}

func (IPAMSubnetScan) TableName() string { return "c_ipam_subnet_scans" }

// IPAMSubnetScanHistory is one scan's result (c_ipam_subnet_scan_histories).
//
// APPEND-ONLY: a scan happened or it did not.
type IPAMSubnetScanHistory struct {
	cmdbmodel.AppendOnlyBase

	SubnetScanID *cmdbmodel.ID `gorm:"column:subnet_scan_id;index:idx_c_ipam_subnet_scan_histories_subnet_scan_id"`

	// ExecID is the agent's execution identifier, which is how a later report
	// of the same run is correlated.
	ExecID *string `gorm:"column:exec_id;size:64;index:idx_c_ipam_subnet_scan_histories_exec_id"`

	CIDR *string `gorm:"column:cidr;size:18;index:idx_c_ipam_subnet_scan_histories_cidr"`

	StartAt *time.Time `gorm:"column:start_at"`
	EndAt   *time.Time `gorm:"column:end_at"`

	// Status is 0 for success; cantor's enum had no other named member.
	Status int `gorm:"column:status;not null;default:0"`

	Stdout *string `gorm:"column:stdout;type:text"`

	IPNum *cmdbmodel.ID `gorm:"column:ip_num"`

	// IPs is the addresses the scan observed, capped by the reporter to the
	// last handful rather than the whole range.
	IPs map[string]any `gorm:"column:ips;serializer:json"`
}

func (IPAMSubnetScanHistory) TableName() string { return "c_ipam_subnet_scan_histories" }

// IPAMOperationHistory is one recorded IPAM operation (c_ipam_operation_histories).
//
// IT CARRIES NO INSTANCE ID, unlike the data centre's log: an IPAM operation is
// recorded against a RANGE, because the subnet it concerns may have been
// deleted by the time somebody reads the log — and a range outlives a row.
type IPAMOperationHistory struct {
	cmdbmodel.AppendOnlyBase

	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_ipam_operation_histories_uid"`

	// CIDR is the range the operation concerned, at the time it happened.
	CIDR *string `gorm:"column:cidr;size:18;index:idx_c_ipam_operation_histories_cidr"`

	OperateType *string `gorm:"column:operate_type;size:1"`

	Description *string `gorm:"column:description;type:text"`
}

func (IPAMOperationHistory) TableName() string { return "c_ipam_operation_histories" }
