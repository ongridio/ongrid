//go:build windows

package host_files

import "os"

// fileOwner is a stub on Windows. Windows uses ACLs/SIDs for file
// ownership (not uid/gid), and resolving SID → account name requires
// heavier Win32 API calls (LookupAccountSid). For MVP-1 the host_files
// response omits owner/group on Windows — full support is deferred
// until a Windows skill actually needs this data (YAGNI).
func fileOwner(_ os.FileInfo) (owner, group string) {
	return "", ""
}
