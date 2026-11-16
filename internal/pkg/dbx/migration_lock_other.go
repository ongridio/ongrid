//go:build !unix

package dbx

import "os"

// The non-Unix fallback cannot take a cross-process file lock. The CMDB
// service is deployed on Linux; local Windows runs are single-process, which
// is the behavior this preserves.
func lockFile(*os.File) (bool, error) { return true, nil }

func unlockFile(*os.File) error { return nil }
