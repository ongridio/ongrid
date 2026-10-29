package dbx

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

type transactionKey struct{}

// WithoutTransaction detaches post-commit work from the closed SQL transaction.
func WithoutTransaction(ctx context.Context) context.Context {
	return context.WithValue(ctx, transactionKey{}, false)
}

type transaction struct {
	db    *gorm.DB
	after []func()
}

// WithContext joins an explicitly supplied unit of work, or uses the normal
// repository connection. A transaction never escapes its callback.
func WithContext(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		return tx.db.WithContext(ctx)
	}
	return db.WithContext(ctx)
}

// Transaction commits related resource, operation and audit writes together.
func Transaction(ctx context.Context, db *gorm.DB, fn func(context.Context) error) error {
	if _, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		return fn(ctx)
	}
	uow := &transaction{}
	var options []*sql.TxOptions
	if db.Dialector.Name() == "mysql" {
		options = []*sql.TxOptions{{Isolation: sql.LevelReadCommitted}}
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		uow.db = tx
		return fn(context.WithValue(ctx, transactionKey{}, uow))
	}, options...)
	if err == nil {
		for _, after := range uow.after {
			after()
		}
	}
	return err
}

// AfterCommit defers non-transactional effects until successful commit. With
// no unit of work it retains the existing immediate behavior.
func AfterCommit(ctx context.Context, fn func()) {
	if tx, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		tx.after = append(tx.after, fn)
		return
	}
	fn()
}
