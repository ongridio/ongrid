package store

import "errors"

// errNilDB is returned by the migrator and constructors when they are handed a
// nil *gorm.DB. It is a programming error rather than an operational one, but
// it is reported instead of panicking because Migrate is also reachable from
// tests and from the CLI tooling.
var errNilDB = errors.New("cmdb ci store: nil db")
