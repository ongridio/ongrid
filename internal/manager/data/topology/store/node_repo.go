package store

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	biz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// NodeRepo is the GORM-backed biz/topology.NodeRepo implementation.
type NodeRepo struct{ db *gorm.DB }

// NewNodeRepo constructs the repo.
func NewNodeRepo(db *gorm.DB) *NodeRepo { return &NodeRepo{db: db} }

var _ biz.NodeRepo = (*NodeRepo)(nil)

func (r *NodeRepo) Create(ctx context.Context, n *model.Node) error {
	return r.db.WithContext(ctx).Create(n).Error
}

func (r *NodeRepo) Update(ctx context.Context, id uint64, name, propsJSON string) error {
	res := r.db.WithContext(ctx).Model(&model.Node{}).Where("id = ?", id).Updates(map[string]any{
		"name":        name,
		"props_jsonb": propsJSON,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *NodeRepo) Get(ctx context.Context, id uint64) (*model.Node, error) {
	var n model.Node
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

func (r *NodeRepo) GetMany(ctx context.Context, ids []uint64) (map[uint64]*model.Node, error) {
	out := make(map[uint64]*model.Node, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []*model.Node
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, n := range rows {
		out[n.ID] = n
	}
	return out, nil
}

func (r *NodeRepo) List(ctx context.Context, f biz.NodeListFilter) ([]*model.Node, error) {
	q := r.db.WithContext(ctx).Model(&model.Node{})
	q = applyNodeFilter(q, f)
	q = q.Order("id DESC")
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if f.Offset > 0 {
		q = q.Offset(f.Offset)
	}
	var out []*model.Node
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *NodeRepo) Count(ctx context.Context, f biz.NodeListFilter) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.Node{})
	q = applyNodeFilter(q, f)
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

func (r *NodeRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var node model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&node).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return err
		}
		var references int64
		if err := tx.Model(&model.Relation{}).
			Where("src_id = ? OR dst_id = ?", id, id).
			Count(&references).Error; err != nil {
			return err
		}
		if references > 0 {
			return errs.ErrConflict
		}
		return tx.Delete(&node).Error
	})
}

func applyNodeFilter(q *gorm.DB, f biz.NodeListFilter) *gorm.DB {
	if f.Type != "" {
		q = q.Where("type = ?", f.Type)
	}
	if f.Q != "" {
		like := "%" + strings.ToLower(f.Q) + "%"
		q = q.Where("LOWER(name) LIKE ?", like)
	}
	return q
}

// EnsureForDevice 使用已有 devices.node_id 作为身份来源，不通过可变名称匹配。
// 与回填共用事务，避免并发注册或回填产生孤立节点、覆盖已有绑定。
func (r *NodeRepo) EnsureForDevice(ctx context.Context, deviceID uint64, name string) (*model.Node, error) {
	var node model.Node
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先获取写锁：MySQL 锁定设备行，SQLite 获取写事务锁，避免读后升级锁失败。
		// 不改变字段值，也不触发 GORM 的 updated_at 更新。
		if err := tx.Exec("UPDATE devices SET node_id = node_id WHERE id = ? AND deleted_at IS NULL", deviceID).Error; err != nil {
			return err
		}
		var device struct {
			ID     uint64
			NodeID *uint64
		}
		if err := tx.Table("devices").Select("id, node_id").Where("id = ? AND deleted_at IS NULL", deviceID).Take(&device).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return err
		}
		if device.NodeID != nil {
			if err := tx.First(&node, *device.NodeID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.ErrNotFound
				}
				return err
			}
			if node.Type != string(model.NodeTypeDevice) {
				return errs.ErrConflict
			}
			return nil
		}
		node = model.Node{Type: string(model.NodeTypeDevice), Name: name}
		if err := tx.Create(&node).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE devices SET node_id = ? WHERE id = ?", node.ID, deviceID).Error
	})
	if err != nil {
		return nil, err
	}
	return &node, nil
}
