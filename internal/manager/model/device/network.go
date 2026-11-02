package device

import "time"

// DeviceNetwork is the network-specific profile for a Device. A network
// device still uses the shared devices.id identity; this table only stores
// fields that do not make sense for a host device.
type DeviceNetwork struct {
	DeviceID                uint64     `gorm:"column:device_id;primaryKey"`
	DeviceKind              string     `gorm:"column:device_kind;size:64;not null;default:''"`
	Vendor                  string     `gorm:"column:vendor;size:128;not null;default:''"`
	Model                   string     `gorm:"column:model;size:128;not null;default:''"`
	SerialNumber            string     `gorm:"column:serial_number;size:255;not null;default:''"`
	ManagementAddress       string     `gorm:"column:management_address;size:255;not null;default:''"`
	SysName                 string     `gorm:"column:sys_name;size:255;not null;default:''"`
	SysDescription          string     `gorm:"column:sys_description;type:text"`
	SnmpEngineID            string     `gorm:"column:snmp_engine_id;size:255;not null;default:''"`
	LldpChassisID           string     `gorm:"column:lldp_chassis_id;size:255;not null;default:''"`
	LldpChassisSubtype      string     `gorm:"column:lldp_chassis_subtype;size:32;not null;default:''"`
	BridgeBaseMAC           string     `gorm:"column:bridge_base_mac;size:32;not null;default:''"`
	CapabilitiesJSON        string     `gorm:"column:capabilities_json;type:text"`
	ReachabilityStatus      string     `gorm:"column:reachability_status;size:32;not null;default:'unknown'"`
	LastReachableAt         *time.Time `gorm:"column:last_reachable_at;index"`
	PollScannerHostDeviceID uint64     `gorm:"column:poll_scanner_host_device_id;not null;default:0;index"`
	PollEnabled             bool       `gorm:"column:poll_enabled;not null;default:false"`
	PollIntervalSeconds     uint32     `gorm:"column:poll_interval_seconds;not null;default:300"`
	PollCredentialName      string     `gorm:"column:poll_credential_name;size:128;not null;default:''"`
	PollPort                uint16     `gorm:"column:poll_port;not null;default:161"`
	LastPollAt              *time.Time `gorm:"column:last_poll_at;index"`
	LastPollError           string     `gorm:"column:last_poll_error;size:512;not null;default:''"`
	NetworkRoleSuppressed   bool       `gorm:"column:network_role_suppressed;not null;default:false"`
	CreatedAt               time.Time  `gorm:"column:created_at"`
	UpdatedAt               time.Time  `gorm:"column:updated_at"`
}

func (DeviceNetwork) TableName() string { return "device_network" }

// NetworkDiscoveryCandidate is an unclassified neighbor observation. It is
// intentionally separate from Device: ARP and a default gateway prove
// reachability, not that the peer is a switch, router, or firewall.
type NetworkDiscoveryCandidate struct {
	ID               uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	ObserverEdgeID   uint64     `gorm:"column:observer_edge_id;not null;index:idx_network_candidates_observer"`
	ObservationKey   string     `gorm:"column:observation_key;size:255;not null;uniqueIndex:idx_network_candidates_key"`
	IPAddress        string     `gorm:"column:ip_address;size:45;not null;default:'';index:idx_network_candidates_ip"`
	MAC              string     `gorm:"column:mac;size:32;not null;default:'';index:idx_network_candidates_mac"`
	InterfaceName    string     `gorm:"column:interface_name;size:255;not null;default:''"`
	Source           string     `gorm:"column:source;size:32;not null;default:''"`
	SourceDataJSON   string     `gorm:"column:source_data_json;type:text"`
	InterfacesJSON   string     `gorm:"column:interfaces_json;type:text"`
	LinksJSON        string     `gorm:"column:links_json;type:text"`
	Status           string     `gorm:"column:status;size:32;not null;default:'unknown'"`
	Confidence       uint8      `gorm:"column:confidence;not null;default:0"`
	PromotedDeviceID *uint64    `gorm:"column:promoted_device_id;index"`
	FirstSeenAt      time.Time  `gorm:"column:first_seen_at;index"`
	LastSeenAt       time.Time  `gorm:"column:last_seen_at;index"`
	ExpiresAt        *time.Time `gorm:"column:expires_at;index"`
	// Observer source metadata is hydrated from edges/devices for API
	// presentation; it is not duplicated into the candidate table.
	ObserverEdgeName     string  `gorm:"-" json:"-"`
	ObserverHostDeviceID *uint64 `gorm:"-" json:"-"`
	ObserverHostName     string  `gorm:"-" json:"-"`
}

func (NetworkDiscoveryCandidate) TableName() string { return "network_discovery_candidates" }

// NetworkIdentifier stores one external identity observed for a device.
// Values are intentionally not globally unique: a weak identifier such as
// an address may move between devices, while conflicts on strong identifiers
// must remain visible for manual resolution.
type NetworkIdentifier struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	DeviceID    uint64    `gorm:"column:device_id;not null;index:idx_device_identifiers_device"`
	Kind        string    `gorm:"column:kind;size:64;not null;index:idx_device_identifiers_lookup,priority:1"`
	Subtype     string    `gorm:"column:subtype;size:32;not null;default:'';index:idx_device_identifiers_lookup,priority:2"`
	Value       string    `gorm:"column:value;size:255;not null;index:idx_device_identifiers_lookup,priority:3"`
	Source      string    `gorm:"column:source;size:32;not null;default:''"`
	Confidence  uint8     `gorm:"column:confidence;not null;default:0"`
	FirstSeenAt time.Time `gorm:"column:first_seen_at"`
	LastSeenAt  time.Time `gorm:"column:last_seen_at;index"`
}

func (NetworkIdentifier) TableName() string { return "device_identifiers" }

// NetworkInterface is a latest-observation snapshot for one device port.
type NetworkInterface struct {
	ID            uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	DeviceID      uint64    `gorm:"column:device_id;not null;index:idx_network_interfaces_device"`
	IfIndex       int       `gorm:"column:if_index;not null;default:0"`
	Name          string    `gorm:"column:name;size:255;not null;default:''"`
	MAC           string    `gorm:"column:mac;size:32;not null;default:'';index:idx_network_interfaces_mac"`
	InterfaceKind string    `gorm:"column:interface_kind;size:32;not null;default:'physical'"`
	Description   string    `gorm:"column:description;size:255;not null;default:''"`
	AdminStatus   string    `gorm:"column:admin_status;size:32;not null;default:''"`
	OperStatus    string    `gorm:"column:oper_status;size:32;not null;default:''"`
	SpeedBps      uint64    `gorm:"column:speed_bps;not null;default:0"`
	InOctets      uint64    `gorm:"column:in_octets;not null;default:0"`
	OutOctets     uint64    `gorm:"column:out_octets;not null;default:0"`
	InErrors      uint64    `gorm:"column:in_errors;not null;default:0"`
	OutErrors     uint64    `gorm:"column:out_errors;not null;default:0"`
	AddressesJSON string    `gorm:"column:addresses_json;type:text"`
	Source        string    `gorm:"column:source;size:32;not null;default:''"`
	LastSeenAt    time.Time `gorm:"column:last_seen_at;index"`
}

func (NetworkInterface) TableName() string { return "network_interfaces" }

// NetworkLink records an observed adjacency. The same physical link may be
// reported by LLDP and SNMP, so source is part of the observation identity;
// reconciliation can later collapse equivalent observations for display.
type NetworkLink struct {
	ID                   uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	EndpointADeviceID    uint64    `gorm:"column:endpoint_a_device_id;not null;default:0;uniqueIndex:idx_network_links_identity,priority:1;index:idx_network_links_a"`
	EndpointAInterfaceID uint64    `gorm:"column:endpoint_a_interface_id;not null;default:0;uniqueIndex:idx_network_links_identity,priority:2"`
	EndpointBDeviceID    uint64    `gorm:"column:endpoint_b_device_id;not null;default:0;uniqueIndex:idx_network_links_identity,priority:3;index:idx_network_links_b"`
	EndpointBInterfaceID uint64    `gorm:"column:endpoint_b_interface_id;not null;default:0;uniqueIndex:idx_network_links_identity,priority:4"`
	LinkKind             string    `gorm:"column:link_kind;size:32;not null;default:'physical';uniqueIndex:idx_network_links_identity,priority:5"`
	Status               string    `gorm:"column:status;size:32;not null;default:''"`
	Confidence           uint8     `gorm:"column:confidence;not null;default:0"`
	PropertiesJSON       string    `gorm:"column:properties_json;type:text"`
	FirstSeenAt          time.Time `gorm:"column:first_seen_at;index"`
	LastSeenAt           time.Time `gorm:"column:last_seen_at;index"`
}

func (NetworkLink) TableName() string { return "network_links" }
