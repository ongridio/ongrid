//go:build linux || darwin

package host_files

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// fileOwner extracts owner/group names from FileInfo on Unix-like
// systems (Linux + Darwin). Uses syscall.Stat_t to get uid/gid, then
// resolves to names via os/user. Falls back to numeric string when
// LookupId fails (common in scratch containers without /etc/passwd).
//
// Windows uses ACL/SID for ownership (fundamentally different from
// uid/gid); see owner_windows.go for the MVP-1 stub that returns
// empty strings.
func fileOwner(fi os.FileInfo) (owner, group string) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10)); err == nil {
			owner = u.Username
		} else {
			owner = strconv.FormatUint(uint64(st.Uid), 10)
		}
		if g, err := user.LookupGroupId(strconv.FormatUint(uint64(st.Gid), 10)); err == nil {
			group = g.Name
		} else {
			group = strconv.FormatUint(uint64(st.Gid), 10)
		}
	}
	return owner, group
}
