//go:build windows

package host_files

import (
	"os"
	"time"
)

// fileTimes returns mtime and atime for the given FileInfo on Windows.
// Windows file timestamps use a different syscall
// (windows.Win32FileAttributeData) than Unix syscall.Stat_t. For MVP-1
// we return mtime for both fields — accurate atime resolution on
// Windows is deferred until a skill needs it (YAGNI). handlers.go
// treats atime as optional metadata, so this stub keeps the handler
// compiling without altering its control flow.
func fileTimes(fi os.FileInfo) (time.Time, time.Time) {
	mtime := fi.ModTime().UTC()
	return mtime, mtime
}
