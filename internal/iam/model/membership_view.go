package model

// MembershipWithUser / MembershipWithOrg 是成员查询的领域视图，不绑定存储实现。
type MembershipWithUser struct {
	OrgMembership
	User User `gorm:"-"`
}

type MembershipWithOrg struct {
	OrgMembership
	Org Org `gorm:"-"`
}
