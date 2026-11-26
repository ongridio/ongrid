package model

// Numeric column widths for the CMDB schema.
//
// These are NOT Ongrid's habitual widths (int, which GORM emits as bigint).
// The CMDB tables mirror nexus-cantor's schema, and matching its column types
// exactly is what makes an existing cantor database adoptable in place.
//
// Verified with GORM's own MySQL type mapper (gorm.io/driver/mysql v1.6.0,
// gorm.io/gorm v1.31.1):
//
//	Go int8  -> tinyint        cantor Boolean  -> tinyint(1)
//	Go int16 -> smallint       cantor SmallInteger
//	Go int32 -> int            cantor Integer
//	Go int64 -> bigint
//	Go int   -> bigint         <- the default trap
//	Go float32 -> float        cantor DOUBLE is therefore float64 -> double
//
// AutoMigrate compares the declared type against information_schema and emits
// ALTER when they differ. Using Go `int` or `uint64` where cantor has `int(11)`
// would therefore issue MODIFY on every such column. On a foreign-key child
// column that fails outright with MySQL error 3780 ("foreign key constraint is
// incorrectly formed", because the referenced parent stays signed int(11));
// everywhere else it rewrites tables for no benefit, including the hot
// c_value_index_* tables.
//
// The trade-off is a 2.1-billion-row ceiling per table, which no CMDB
// deployment approaches. It also narrows the `uid` columns, which hold ONGRID
// user ids rather than cantor's: Ongrid's users.ID is uint64, so a user id
// above math.MaxInt32 cannot be attributed. That is accepted because the column
// must stay int(11) for adoption, and it is recorded here rather than
// discovered later.

// Int32 is a 32-bit signed column, matching cantor's `Integer` (MySQL int(11)).
//
// It is a type ALIAS, not a defined type, so it stays freely assignable to and
// from int32. A defined type would force conversions at every GORM, JSON and
// SQL boundary for no correctness gain — the width is what matters, and the
// alias target enforces it.
type Int32 = int32

// SmallInt is a 16-bit signed column, matching cantor's `SmallInteger`
// (MySQL smallint). cantor uses it for the `order` columns that sequence
// groups and attributes.
type SmallInt = int16

// ID is the type for every CMDB identifier and foreign key.
//
// It is the same width as cantor's `Integer`, which is what every one of
// cantor's id and foreign-key columns is declared as.
type ID = Int32
