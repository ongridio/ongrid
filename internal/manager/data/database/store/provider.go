package store

import (
	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/manager/biz/database"
)

// NewBizRepo is the wire-ready constructor. Returns the biz.Repo interface
// so the wiring layer stays free of the concrete type.
func NewBizRepo(db *gorm.DB) biz.Repo {
	return NewRepo(db)
}
